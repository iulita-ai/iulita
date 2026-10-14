package httpfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Outcome is the typed result taxonomy (plan §4.3). HTTP 4xx/5xx statuses are
// NOT Go errors: they produce outcome http_error with full status/headers/body.
type Outcome string

const (
	// OutcomeCompleted marks a successful step or run.
	OutcomeCompleted Outcome = "completed"
	// OutcomeHTTPError preserves 3xx/4xx/5xx diagnostics.
	OutcomeHTTPError Outcome = "http_error"
	// OutcomeValidationError rejects input before any network activity.
	OutcomeValidationError Outcome = "validation_error"
	// OutcomePolicyDenied blocks a request by server policy.
	OutcomePolicyDenied Outcome = "policy_denied"
	// OutcomeConfirmationRequired awaits an unavailable human decision.
	OutcomeConfirmationRequired Outcome = "confirmation_required"
	// OutcomeDeclined records a human refusal.
	OutcomeDeclined Outcome = "declined"
	// OutcomeTimedOut marks exhausted time budgets.
	OutcomeTimedOut Outcome = "timed_out"
	// OutcomeCancelled marks user or shutdown cancellation.
	OutcomeCancelled Outcome = "cancelled" //nolint:misspell // contract value (plan §4.3)
	// OutcomeTransportError marks proxy/network failures.
	OutcomeTransportError Outcome = "transport_error"
	// OutcomeQuotaExceeded marks limit enforcement.
	OutcomeQuotaExceeded Outcome = "quota_exceeded"
	// OutcomeOutcomeUnknown marks sent-but-unverifiable mutations.
	OutcomeOutcomeUnknown Outcome = "outcome_unknown"
	// OutcomeSkipped marks steps never executed.
	OutcomeSkipped Outcome = "skipped"
)

// Terminal reports whether a step outcome stops the chain (unless the step
// explicitly continues on http errors).
func (o Outcome) Terminal() bool {
	switch o {
	case OutcomeCompleted, OutcomeHTTPError:
		return false
	default:
		return true
	}
}

// ErrorInfo carries a safe, non-sensitive explanation for non-success outcomes.
type ErrorInfo struct {
	Outcome   Outcome `json:"outcome"`
	Code      string  `json:"code,omitempty"`
	Message   string  `json:"message,omitempty"`
	StepID    string  `json:"step_id,omitempty"`
	Retryable bool    `json:"retryable,omitempty"`
}

// RunResult is the single envelope for single requests and chains.
type RunResult struct {
	Version         int          `json:"version"`
	OperationID     string       `json:"operation_id"`
	Outcome         Outcome      `json:"outcome"`
	Steps           []StepResult `json:"steps"`
	StopReason      string       `json:"stop_reason,omitempty"`
	ExternalContent bool         `json:"external_content"`
	Error           *ErrorInfo   `json:"error,omitempty"`
}

// StepResult is one executed step with bounded, sanitized projections.
type StepResult struct {
	ID               string         `json:"id"`
	Outcome          Outcome        `json:"outcome"`
	Method           string         `json:"method,omitempty"`
	URL              string         `json:"url,omitempty"` // sanitized: query secret values masked
	Status           int            `json:"status,omitempty"`
	Protocol         string         `json:"protocol,omitempty"`
	Headers          []HeaderView   `json:"headers,omitempty"`
	Trailers         []HeaderView   `json:"trailers,omitempty"`
	TrailersComplete bool           `json:"trailers_complete,omitempty"`
	Body             *BodyView      `json:"body,omitempty"`
	BytesRead        int64          `json:"bytes_read"`
	BytesDeclared    int64          `json:"bytes_declared,omitempty"` // Content-Length, -1 when unknown
	BodyComplete     bool           `json:"body_complete,omitempty"`
	Artifact         *ArtifactView  `json:"artifact,omitempty"`
	DurationMs       int64          `json:"duration_ms"`
	Redirects        []RedirectView `json:"redirects,omitempty"`
	ProxyUsed        bool           `json:"proxy_used"`
	Vars             []VarView      `json:"vars,omitempty"` // extracted names/types only, never values
	Error            *ErrorInfo     `json:"error,omitempty"`
}

// HeaderView is a projected header. Sensitive headers keep their name and
// value count but have values masked; full copies live only in owner-scoped
// raw artifacts.
type HeaderView struct {
	Name      string   `json:"name"`
	Values    []string `json:"values"`
	Sensitive bool     `json:"sensitive,omitempty"`
}

// BodyView is the bounded inline body projection. Text bodies are truncated
// per preview limits; binary bodies get a small base64 preview.
type BodyView struct {
	Encoding  string `json:"encoding,omitempty"` // utf-8, latin-1, ...
	MIME      string `json:"mime,omitempty"`
	Text      string `json:"text,omitempty"`
	Base64    string `json:"base64,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ArtifactView describes a stored download artifact.
type ArtifactView struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename,omitempty"`
	Bytes     int64     `json:"bytes"`
	SHA256    string    `json:"sha256,omitempty"`
	MIME      string    `json:"mime,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Sensitive bool      `json:"sensitive,omitempty"`
}

// RedirectView records one redirect hop.
type RedirectView struct {
	Status   int    `json:"status"`
	Location string `json:"location,omitempty"`
	URL      string `json:"url,omitempty"` // resolved absolute target (sanitized)
}

// VarView reports extracted variable metadata without the value.
type VarView struct {
	Name      string  `json:"name"`
	Type      VarType `json:"type,omitempty"`
	Sensitive bool    `json:"sensitive"`
}

// JSON renders the result envelope.
func (r RunResult) JSON() (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(b), nil
}

// Fingerprint computes a stable SHA-256 of a canonicalized plan, used to bind
// confirmations to the exact operation.
func Fingerprint(plan Plan) string {
	b, err := json.Marshal(canonicalPlan(plan))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// canonicalPlan projects a plan onto its fingerprint-relevant structure.
// Inline secrets are replaced by length markers so the fingerprint never
// depends on (or leaks) secret content, while still distinguishing plans.
func canonicalPlan(plan Plan) Plan {
	cp := plan
	cp.Request = canonicalRequest(plan.Request)
	cp.Steps = make([]Request, len(plan.Steps))
	for i := range plan.Steps {
		if cr := canonicalRequest(&plan.Steps[i]); cr != nil {
			cp.Steps[i] = *cr
		}
	}
	return cp
}

func canonicalRequest(r *Request) *Request {
	if r == nil {
		return nil
	}
	cr := *r
	if r.Auth != nil {
		auth := *r.Auth
		switch auth.Type {
		case AuthBasic:
			// Usernames are part of identity and stay; passwords are marked.
			auth.Password = secretMarker(auth.Password)
		case AuthBearer:
			auth.Token = secretMarker(auth.Token)
		}
		cr.Auth = &auth
	}
	if r.Body != nil {
		body := *r.Body
		switch body.Type {
		case BodyText:
			// Text may embed user-typed secrets; fingerprint hashes content shape only.
			body.Value = secretMarker(body.Value)
		case BodyBase64:
			body.Value = secretMarker(body.Value)
		}
		cr.Body = &body
	}
	if len(cr.Headers) > 0 {
		headers := make([]HeaderSpec, len(cr.Headers))
		copy(headers, cr.Headers)
		cr.Headers = headers
	}
	return &cr
}

func secretMarker(s string) string {
	if s == "" {
		return ""
	}
	return fmt.Sprintf("<secret:%d>", len(s))
}
