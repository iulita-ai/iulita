package translation

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/modelruntime"
)

// Input limits bound text independently by Unicode code points and UTF-8 bytes.
const (
	MaxTextRunes     = 20000
	MaxTextBytes     = 64 << 10
	maxRequestBytes  = 128 << 10
	maxResponseBytes = 512 << 10
	maxOutputRunes   = 100000
)

// Service executes single-attempt translations with shared admission and limits.
// Applications should share one instance to enforce process-wide concurrency.
type Service struct {
	runtime Runtime
	client  *http.Client
	observe llm.AttemptObserver
	mu      sync.Mutex
	users   map[string]bool
	active  int
}

// New reuses the base transport while applying the agent's own deadline and route.
func New(runtime Runtime, base *http.Client, observe llm.AttemptObserver) *Service {
	return &Service{runtime: runtime, client: newClient(base), observe: observe, users: map[string]bool{}}
}

// Status reports nonsecret admission readiness without contacting the vendor.
func (s *Service) Status() string {
	if s == nil || s.runtime == nil {
		return "runtime_unavailable"
	}
	return s.runtime.OperationAvailability("zai")
}
func (s *Service) acquire(user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active >= 2 || s.users[user] {
		return false
	}
	s.active++
	s.users[user] = true
	return true
}
func (s *Service) release(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.users, user)
	s.active--
}

func validate(r *Request) error {
	if r.UserID == "" || !utf8.ValidString(r.Text) || strings.TrimSpace(r.Text) == "" {
		return fail("invalid_input")
	}
	if len(r.Text) > MaxTextBytes || utf8.RuneCountInString(r.Text) > MaxTextRunes {
		return fail("text_too_long")
	}
	if r.SourceLanguage == "" {
		r.SourceLanguage = "auto"
	}
	if !contains(SourceLanguages(), r.SourceLanguage) || !contains(TargetLanguages(), r.TargetLanguage) {
		return fail("unsupported_language")
	}
	if r.SourceLanguage == r.TargetLanguage {
		return fail("same_language")
	}
	return nil
}
func contains(items []string, wanted string) bool {
	for _, v := range items {
		if v == wanted {
			return true
		}
	}
	return false
}

// Translate submits at most one upstream request. The caller owns the per-turn
// terminal guard; this service independently enforces process/user concurrency.
func (s *Service) Translate(ctx context.Context, r Request) (out Response, err error) {
	a := llm.Attempt{AttemptMetadata: llm.AttemptMetadata{Provider: "zai", RequestedModel: AgentID}, AttemptID: rand.Text(), UserID: r.UserID, ChatID: r.ChatID, Operation: "translation", Model: AgentID, StartedAt: time.Now().UTC(), Status: "rejected", UsageAvailable: true}
	submitted := false
	defer func() {
		a.CompletedAt = time.Now().UTC()
		a.Usage = out.Usage
		if submitted {
			a.UsageAvailable = out.UsageAvailable
			a.ChargeUnknown = !out.UsageAvailable
			a.Status = "success"
			if err != nil {
				a.Status = "error"
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				a.Status = "cancelled"
			}
			var typed *Error
			if errors.As(err, &typed) && typed.Code == "incomplete_result" {
				a.Status = "incomplete"
			}
		}
		if err != nil && !submitted {
			var typed *Error
			if errors.As(err, &typed) {
				typed.unsent = true
			}
		}
		if s != nil && s.observe != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			s.observe(cleanup, a)
		}
	}()
	if validationErr := validate(&r); validationErr != nil {
		return out, validationErr
	}
	if ctx.Err() != nil {
		return out, contextFailure(ctx.Err())
	}
	if s == nil || s.runtime == nil {
		return out, fail("runtime_unavailable")
	}
	if !s.acquire(r.UserID) {
		return out, fail("busy")
	}
	defer s.release(r.UserID)
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	body, marshalErr := json.Marshal(struct {
		AgentID   string            `json:"agent_id"`
		Messages  []wireMessage     `json:"messages"`
		Stream    bool              `json:"stream"`
		Variables map[string]string `json:"custom_variables"`
	}{AgentID, []wireMessage{{Role: "user", Content: []textContent{{Type: "text", Text: r.Text}}}}, false, map[string]string{"source_lang": r.SourceLanguage, "target_lang": r.TargetLanguage, "strategy": "general"}})
	if marshalErr != nil || len(body) > maxRequestBytes {
		return out, fail("text_too_long")
	}
	err = s.runtime.WithOperationConnection(ctx, "zai", "translation", func(admitted context.Context, c modelruntime.Connection, revision uint64) (operationErr error) {
		a.PolicyRevision = revision
		if c.Provider != "zai" || !c.Enabled || c.APIKey == "" {
			return fail("not_configured")
		}
		if _, e := llm.OfficialModelEndpoint("zai", c.Endpoint); e != nil {
			return fail("not_configured")
		}
		if admitted.Err() != nil {
			return contextFailure(admitted.Err())
		}
		req, e := http.NewRequestWithContext(admitted, http.MethodPost, endpoint, bytes.NewReader(body))
		if e != nil {
			return fail("invalid_input")
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		submitted = true
		resp, e := s.client.Do(req)
		if e != nil {
			if admitted.Err() != nil {
				return contextFailure(admitted.Err())
			}
			if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
				return contextFailure(e)
			}
			return fail("provider_unavailable")
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil && operationErr == nil {
				// Keep any reported usage, but do not claim a clean completion
				// when the transport failed to release its response body.
				out.Text = ""
				operationErr = fail("provider_unavailable")
			}
		}()
		data, e := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if e != nil {
			if admitted.Err() != nil {
				return contextFailure(admitted.Err())
			}
			if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
				return contextFailure(e)
			}
			return fail("provider_unavailable")
		}
		if len(data) > maxResponseBytes {
			return fail("protocol_error")
		}
		out, e = normalize(data)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			out.Text = ""
			switch resp.StatusCode {
			case 401:
				return fail("auth_failed")
			case 403:
				return fail("permission_denied")
			case 429:
				return fail("rate_limited")
			default:
				return fail("provider_unavailable")
			}
		}
		return e
	})
	if err != nil {
		out.Text = ""
		var te *Error
		if !errors.As(err, &te) {
			err = runtimeFailure(err)
		}
	}
	return out, err
}
func contextFailure(err error) *Error {
	// Retain cancellation identity without retaining url.Error or upstream text.
	cause := context.DeadlineExceeded
	if errors.Is(err, context.Canceled) {
		cause = context.Canceled
	}
	return &Error{Code: "timeout", cause: cause}
}
func runtimeFailure(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return contextFailure(err)
	}
	var re *modelruntime.Error
	if errors.As(err, &re) {
		switch re.Code {
		case "forbidden_provider":
			return fail("forbidden")
		case "credential_revoked":
			return fail("credential_revoked")
		case "credential_required":
			return fail("not_configured")
		case "runtime_unavailable", "runtime_stopped", "encryption_required":
			return fail("runtime_unavailable")
		}
	}
	return fail("provider_unavailable")
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type wireMessage struct {
	Role    string        `json:"role"`
	Content []textContent `json:"content"`
}
