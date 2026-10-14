package httpfetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/web"
)

// runStep executes one step end-to-end and returns its bounded projection.
func (e *Engine) runStep(ctx context.Context, step Request, state *runState, scope Scope, idx, total int) StepResult {
	sr := StepResult{ID: step.ID, ProxyUsed: true, BodyComplete: false}
	start := time.Now()
	state.currentStep = step.ID

	method, merr := validateMethod(step.Method)
	if merr != nil {
		return stepError(sr, OutcomeValidationError, "method", merr)
	}
	sr.Method = method

	// Feature toggle is re-checked per step: a mid-run disable stops the chain.
	if !e.policy.Enabled {
		return stepError(sr, OutcomePolicyDenied, "http_disabled", fmt.Errorf("HTTP tool disabled"))
	}

	if !e.policy.MethodAllowed(method) {
		return stepError(sr, OutcomePolicyDenied, "method_not_allowed", fmt.Errorf("method %s is not allowed by policy", method))
	}

	// Resolve the target URL (literal or url_from) with full policy control.
	target, sanQuery, err := e.resolveTargetURL(ctx, step, state, scope)
	if err != nil {
		return stepError(sr, OutcomeValidationError, "url", err)
	}
	if step.URLFrom == nil {
		sr.URL = sanitizedURLString(target, sanQuery)
	}

	if verr := web.ValidateTarget(ctx, target.String(), e.resolver); verr != nil {
		return stepError(sr, OutcomePolicyDenied, "ssrf", verr)
	}
	origin := ParseOrigin(target)
	if !e.policy.PortAllowed(portOf(target)) {
		return stepError(sr, OutcomePolicyDenied, "port_not_allowed", fmt.Errorf("port %s is not allowed by policy", target.Port()))
	}

	// Bind inline secrets to this origin for egress tracking.
	secrets := e.collectSecrets(step, state, origin)

	// Plaintext (http) origin carrying secrets requires explicit consent.
	if origin.Scheme != "https" && len(secrets) > 0 {
		if !state.insecureOK[origin.String()] {
			decision, ferr := e.askConsent(ctx, state, scope, ConfirmInsecureCredential,
				fmt.Sprintf("credentials would be sent over plaintext http to %s", origin), 0)
			if ferr != nil {
				return *ferr
			}
			state.insecureOK[origin.String()] = true
			_ = decision
		}
	}

	// Cross-origin transfer of sensitive values requires dedicated consent.
	if ferr := e.checkEgress(ctx, state, scope, secrets, origin); ferr != nil {
		return *ferr
	}

	// Mutating methods need an identity-bound confirmation bound to the
	// plan — unless a standing grant ("always allow") already covers it.
	if !isSafeMethod(method) {
		if e.hasStandingGrant(ctx, scope, origin) {
			e.metrics.ConsentAsked(string(ConfirmMutation), "standing")
		} else if state.mutGrant == nil {
			preview := e.mutationPreview(state, idx, total)
			decision, ferr := e.askConsent(ctx, state, scope, ConfirmMutation, preview, 0)
			if ferr != nil {
				return *ferr
			}
			// A standing choice (always-for-origin / always) persists the
			// user's own approval for future mutations.
			if decision.Standing != "" {
				e.saveStandingGrant(ctx, scope, origin, decision.Standing, decision.Actor.UserID)
			}
			state.mutGrant = &decision
		}
	}

	// Per-step deadline: the lesser of the server step timeout, the step's
	// own (already validated ≤ server max), and the remaining chain budget.
	stepTimeout := e.limits.RequestTimeout
	if step.TimeoutMs > 0 {
		stepTimeout = time.Duration(step.TimeoutMs) * time.Millisecond
	}
	if remaining := e.limits.ChainBudget - state.chainUsed; remaining < stepTimeout {
		stepTimeout = remaining
	}
	if stepTimeout <= 0 {
		return stepError(sr, OutcomeTimedOut, "chain_budget", fmt.Errorf("non-download chain budget exhausted"))
	}

	release, err := e.acquireTransfer(ctx, scope)
	if err != nil {
		return stepError(sr, OutcomeQuotaExceeded, "transfers_busy", err)
	}
	defer release()

	stepStart := time.Now()
	resp, hops, rerr := e.doWithRedirects(ctx, step, state, scope, method, target, origin)
	active := time.Since(stepStart)
	state.chainUsed += active

	if rerr != nil {
		outcome, code := classifyTransportError(method, rerr)
		sr.DurationMs = time.Since(start).Milliseconds()
		return e.stepErrorObserved(sr, outcome, code, sanitizeTransportErr(rerr), method)
	}

	sr.Protocol = resp.Proto
	sr.Status = resp.StatusCode
	sr.URL = sanitizedURLString(resp.Request.URL, sanQuery)
	for _, h := range hops {
		sr.Redirects = append(sr.Redirects, RedirectView{Status: h.status, Location: h.location, URL: sanitizeDisplayURL(h.url)})
	}

	// Record final response metadata for url_from and cookie provenance.
	finalCopy := *resp.Request.URL
	state.internal[step.ID] = &stepInternal{
		finalURL: &finalCopy,
		headers:  resp.Header.Clone(),
		status:   resp.StatusCode,
	}

	// Cookie jar bookkeeping (chain mode): record provenance before serving.
	if state.jar != nil {
		state.jar.recordResponse(origin, resp)
	}

	// Body handling per response mode.
	mode := ResponseInline
	if step.Response != nil && step.Response.Mode != "" {
		mode = step.Response.Mode
	}
	var entity []byte
	switch mode {
	case ResponseDiscard:
		// Close without application reads — no drain, no connection reuse.
		closeBody(resp)
		sr.BodyComplete = true
		sr.Body = nil
		sr.TrailersComplete = false // unread body → trailers unknowable
	case ResponseDownload:
		av, bodyInfo, derr := e.downloadResponse(ctx, step, state, scope, resp, origin)
		if derr != nil {
			return *derr
		}
		sr.Artifact = av
		sr.BytesRead = bodyInfo.bytesRead
		sr.BytesDeclared = bodyInfo.bytesDeclared
		sr.BodyComplete = bodyInfo.complete
		sr.Body = nil
	case ResponseInline:
		ino := e.readInline(ctx, step, resp)
		sr.Body = ino.view
		sr.BytesRead = ino.info.bytesRead
		sr.BytesDeclared = ino.info.bytesDeclared
		sr.BodyComplete = ino.info.complete
		sr.Trailers = ino.trailers
		sr.TrailersComplete = ino.info.complete
		if ino.err != nil {
			return stepError(sr, classifyReadError(ino.err), "body_read", ino.err)
		}
		entity = ino.entity
		// Owner-private raw copy of the bounded response (§8.2): only the
		// already-read body, never an extra read.
		if step.Response != nil && step.Response.IncludeRawArtifact {
			e.storeRawArtifact(ctx, step, &sr, resp, ino, scope, state.opID)
		}
	}

	// Header projection (bounded, masked).
	sr.Headers = e.projectHeaders(resp, e.limits.HeaderPreviewBytes)

	// Expect-status evaluation (default 200-299).
	if !statusExpected(step.ExpectStatus, resp.StatusCode) {
		sr.Outcome = OutcomeHTTPError
	} else {
		sr.Outcome = OutcomeCompleted
	}

	// Variable extraction (after the body is available; JSON extraction is
	// skipped on truncated bodies with an explicit error unless defaulted).
	extractErr := e.runExtract(step, resp, sr, entity, sr.BodyComplete, state, origin)
	if extractErr != nil {
		return stepError(sr, OutcomeValidationError, "extract", extractErr)
	}

	sr.DurationMs = time.Since(start).Milliseconds()
	modeNow := mode == ResponseDownload
	e.observeStep(sr, method, time.Since(start), modeNow)
	return sr
}

// stepError fills the error info on a copy of the step result.
func stepError(sr StepResult, outcome Outcome, code string, err error) StepResult {
	sr.Outcome = outcome
	sr.Error = &ErrorInfo{Outcome: outcome, Code: code, Message: err.Error()}
	return sr
}

// stepErrorObserved is stepError plus a metrics observation for failures that
// bypass the normal completion path.
func (e *Engine) stepErrorObserved(sr StepResult, outcome Outcome, code string, err error, method string) StepResult {
	e.observeStep(sr, method, 0, false)
	return stepError(sr, outcome, code, err)
}

// resolveTargetURL builds the absolute target: base URL + path segments +
// ordered query parameters, or resolves url_from against a previous step.
// sanQuery marks sensitive query positions for sanitized projections.
func (e *Engine) resolveTargetURL(ctx context.Context, step Request, state *runState, scope Scope) (*url.URL, map[int]bool, error) {
	if step.URLFrom != nil {
		prev, ok := state.internal[step.URLFrom.Step]
		if !ok {
			return nil, nil, fmt.Errorf("url_from references unknown or not-yet-executed step %q", step.URLFrom.Step)
		}
		values := lookupHeader(prev.headers, step.URLFrom.Header)
		if step.URLFrom.Index >= len(values) {
			return nil, nil, fmt.Errorf("url_from header %q has no value at index %d", step.URLFrom.Header, step.URLFrom.Index)
		}
		loc, err := url.Parse(values[step.URLFrom.Index])
		if err != nil {
			return nil, nil, fmt.Errorf("url_from header value: %w", err)
		}
		resolved := prev.finalURL.ResolveReference(loc)
		if resolved.Scheme != "http" && resolved.Scheme != "https" {
			return nil, nil, fmt.Errorf("resolved URL scheme %q not allowed", resolved.Scheme)
		}
		return resolved, nil, nil
	}

	u, err := url.Parse(step.URL)
	if err != nil {
		return nil, nil, err
	}
	if len(step.PathSegments) > 0 {
		// Path holds the decoded form; RawPath the exact wire form so each
		// segment stays a single segment ("/" inside a value becomes %2F).
		// Variables resolve to scalars; credential refs were rejected at
		// validation time.
		origin := ParseOrigin(u)
		var decoded, escaped strings.Builder
		decoded.WriteString(u.Path)
		escaped.WriteString(u.EscapedPath())
		for _, seg := range step.PathSegments {
			if !strings.HasSuffix(decoded.String(), "/") {
				decoded.WriteByte('/')
				escaped.WriteByte('/')
			}
			val, _, rerr := e.resolveValue(ctx, state, scope, origin, seg)
			if rerr != nil {
				return nil, nil, fmt.Errorf("path_segments: %w", rerr)
			}
			decoded.WriteString(val)
			escaped.WriteString(url.PathEscape(val))
		}
		u.Path = decoded.String()
		u.RawPath = escaped.String()
	}

	// Query values may reference variables and credentials; the origin is
	// known from the base URL, so refs resolve before the query is encoded.
	if len(step.Query) > 0 {
		if u.RawQuery != "" {
			return nil, nil, fmt.Errorf("URL carries its own query while the query field is also set; use one source")
		}
		origin := ParseOrigin(u)
		type resolvedParam struct{ name, value string }
		resolved := make([]resolvedParam, 0, len(step.Query))
		sanQuery := make(map[int]bool)
		for i, qp := range step.Query {
			val, sensitive, rerr := e.resolveValue(ctx, state, scope, origin, qp.Value)
			if rerr != nil {
				return nil, nil, fmt.Errorf("query[%d]: %w", i, rerr)
			}
			if sensitive {
				sanQuery[i] = true
			}
			resolved = append(resolved, resolvedParam{name: qp.Name, value: val})
		}
		var b strings.Builder
		for i, rp := range resolved {
			if i > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(rp.name))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(rp.value))
		}
		u.RawQuery = b.String()
		return u, sanQuery, nil
	}
	return u, nil, nil
}

func portOf(u *url.URL) int {
	p := u.Port()
	if p == "" {
		if u.Scheme == "https" {
			return 443
		}
		return 80
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return 0
	}
	return port
}

// statusExpected applies the default 200-299 or the declared matches.
func statusExpected(matches []StatusMatch, status int) bool {
	if len(matches) == 0 {
		return status >= 200 && status <= 299
	}
	for _, m := range matches {
		if m.Matches(status) {
			return true
		}
	}
	return false
}

// classifyTransportError maps Go transport failures onto the outcome
// taxonomy. A failure after a mutating request was sent is outcome_unknown:
// the remote side may have applied the change.
func classifyTransportError(method string, err error) (outcome Outcome, code string) {
	if err == nil {
		return OutcomeCompleted, ""
	}
	if errors.Is(err, context.Canceled) {
		return OutcomeCancelled, "cancelled" //nolint:misspell // contract value
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return OutcomeTimedOut, "timeout"
	}
	if !isSafeMethod(method) {
		return OutcomeOutcomeUnknown, "transport_error_after_send"
	}
	return OutcomeTransportError, "transport_error"
}

// sanitizeTransportErr strips URLs (userinfo, query, path) from error text —
// *url.Error embeds the full request URL including secret query values.
func sanitizeTransportErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	var uerr *url.Error
	if errors.As(err, &uerr) {
		msg = fmt.Sprintf("%s: %v", strings.ToUpper(uerr.Op[:1])+uerr.Op[1:], uerr.Err)
		// Fall through to generic scrubbing below for nested URLs.
	}
	msg = scrubURLs(msg)
	return fmt.Errorf("%s", msg)
}

// scrubURLs replaces any embedded http(s) URL with scheme://host only.
func scrubURLs(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "http://")
		j := strings.Index(s, "https://")
		k := i
		if k == -1 || (j != -1 && j < k) {
			k = j
		}
		if k == -1 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:k])
		rest := s[k:]
		end := len(rest)
		for cut, ch := range rest {
			if ch == ' ' || ch == '\'' || ch == '"' || ch == '\n' || ch == '\t' {
				end = cut
				break
			}
		}
		b.WriteString(urlOriginOnly(rest[:end]))
		s = rest[end:]
	}
}

func urlOriginOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<url>"
	}
	return u.Scheme + "://" + u.Host
}

// sensitiveQueryNames are parameter names whose values are masked in every
// URL projection — our own secrets and common token echoes.
var sensitiveQueryNames = []string{"token", "access_token", "secret", "password", "passwd", "api_key", "apikey", "key", "auth", "signature", "sig", "session", "code"}

// sanitizeDisplayURL redacts sensitive-named query values in a raw URL string.
func sanitizeDisplayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return scrubURLs(raw)
	}
	return sanitizeURLString(u)
}

// sanitizeURLString produces a display-safe URL for server-provided URLs
// (redirects, url_from): values of sensitive-named query parameters are
// redacted; the full URL stays engine-internal for resolution.
func sanitizeURLString(u *url.URL) string {
	if u == nil {
		return ""
	}
	cp := *u
	cp.RawQuery = redactSensitiveQuery(cp.RawQuery, nil)
	return cp.String()
}

// sanitizedURLString redacts the declared sensitive query positions of a
// caller-built target URL.
func sanitizedURLString(u *url.URL, sanQuery map[int]bool) string {
	if u == nil {
		return ""
	}
	if len(sanQuery) == 0 {
		return sanitizeURLString(u)
	}
	cp := *u
	cp.RawQuery = redactSensitiveQuery(cp.RawQuery, sanQuery)
	return cp.String()
}

// redactSensitiveQuery masks query values by sensitive name or by index.
func redactSensitiveQuery(rawQuery string, byIndex map[int]bool) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	changed := false
	for i, p := range parts {
		if byIndex[i] {
			if k, _, ok := strings.Cut(p, "="); ok {
				parts[i] = k + "=[redacted]"
				changed = true
			}
			continue
		}
		k, _, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		lower := strings.ToLower(k)
		for _, s := range sensitiveQueryNames {
			if lower == s || strings.HasPrefix(lower, s+"_") {
				parts[i] = k + "=[redacted]"
				changed = true
				break
			}
		}
	}
	if !changed {
		return rawQuery
	}
	return strings.Join(parts, "&")
}

func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close() //nolint:errcheck // discard path
	}
}
