package httpfetch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// secretUse describes one sensitive value about to be sent, with the origin
// it is currently bound to. Cross-origin transfers need explicit consent.
type secretUse struct {
	kind       string // inline_basic | inline_bearer | credential | var | cookie
	ref        string // human-safe label (credential/var name), never the value
	bindOrigin Origin
}

// collectSecrets enumerates sensitive values in play for one step. Inline
// secrets are bound to the first origin they are sent to.
func (e *Engine) collectSecrets(step Request, state *runState, origin Origin) []secretUse {
	var out []secretUse
	add := func(kind, ref string, bound Origin) {
		out = append(out, secretUse{kind: kind, ref: ref, bindOrigin: bound})
	}

	if step.Auth != nil {
		switch step.Auth.Type {
		case AuthBasic:
			if step.Auth.CredentialRef != "" {
				add("credential", "auth:"+step.Auth.CredentialRef, state.secretBinding("cred:"+step.Auth.CredentialRef, origin))
			} else {
				add("inline_basic", "inline credentials", state.secretBinding("inline-basic", origin))
			}
		case AuthBearer:
			if step.Auth.CredentialRef != "" {
				add("credential", "auth:"+step.Auth.CredentialRef, state.secretBinding("cred:"+step.Auth.CredentialRef, origin))
			} else {
				add("inline_bearer", "inline token", state.secretBinding("inline-bearer", origin))
			}
		}
	}

	for _, h := range step.Headers {
		for _, v := range h.Values {
			switch v.Kind {
			case ValueCredential:
				add("credential", "header:"+v.Ref, state.secretBinding("cred:"+v.Ref, origin))
			case ValueVar:
				if val, ok := state.vars[v.Ref]; ok && val.Sensitive {
					add("var", "header:$var:"+v.Ref, val.Origin)
				}
			}
		}
	}
	for _, qp := range step.Query {
		switch qp.Value.Kind {
		case ValueCredential:
			add("credential", "query:"+qp.Value.Ref, state.secretBinding("cred:"+qp.Value.Ref, origin))
		case ValueVar:
			if val, ok := state.vars[qp.Value.Ref]; ok && val.Sensitive {
				add("var", "query:$var:"+qp.Value.Ref, val.Origin)
			}
		}
	}
	for _, seg := range step.PathSegments {
		if seg.Kind == ValueVar {
			if val, ok := state.vars[seg.Ref]; ok && val.Sensitive {
				add("var", "path:$var:"+seg.Ref, val.Origin)
			}
		}
	}
	if step.Body != nil {
		switch step.Body.Type {
		case BodyForm:
			for _, f := range step.Body.Fields {
				switch f.Value.Kind {
				case ValueCredential:
					add("credential", "form:"+f.Value.Ref, state.secretBinding("cred:"+f.Value.Ref, origin))
				case ValueVar:
					if val, ok := state.vars[f.Value.Ref]; ok && val.Sensitive {
						add("var", "form:$var:"+f.Value.Ref, val.Origin)
					}
				}
			}
		case BodyJSON:
			refs, _ := walkJSONRefs(step.Body.RawJSON) //nolint:errcheck // decision unused beyond approval
			for _, ref := range refs {
				if ref.IsVar {
					if val, ok := state.vars[ref.Value]; ok && val.Sensitive {
						add("var", "json:$var:"+ref.Value, val.Origin)
					}
				} else {
					add("credential", "json:"+ref.Value, state.secretBinding("cred:"+ref.Value, origin))
				}
			}
		}
	}
	return out
}

// secretBinding returns the origin a secret is bound to, recording the first
// origin it is used with.
func (s *runState) secretBinding(key string, origin Origin) Origin {
	if prev, ok := s.secretOrigins[key]; ok {
		return prev
	}
	if s.secretOrigins == nil {
		s.secretOrigins = make(map[string]Origin)
	}
	s.secretOrigins[key] = origin
	return origin
}

// checkEgress enforces origin-bound transfer of sensitive values: sending a
// secret to an origin other than the one it is bound to requires explicit
// consent (sensitive_egress). Returns a terminal StepResult when denied.
func (e *Engine) checkEgress(ctx context.Context, state *runState, scope Scope, secrets []secretUse, origin Origin) *StepResult {
	for _, su := range secrets {
		if su.bindOrigin.Equal(origin) {
			continue
		}
		pair := su.bindOrigin.String() + "→" + origin.String()
		if state.egressOK[pair] {
			continue
		}
		preview := fmt.Sprintf("Send %s (set at %s) to a different origin %s. This discloses the value to that server.",
			su.ref, su.bindOrigin, origin)
		_, ferr := e.askConsent(ctx, state, scope, ConfirmSensitiveEgress, preview, 0)
		if ferr != nil {
			return ferr
		}
		state.egressOK[pair] = true
	}
	return nil
}

// askConsent runs the identity-bound confirmation flow. Consent waits are
// excluded from active budgets but bounded by ConsentWait and the wall clock.
// The channel-bound asker from the context takes precedence over the
// engine-wide default; no asker at all fails closed.
func (e *Engine) askConsent(ctx context.Context, state *runState, scope Scope, reason ConfirmReason, preview string, maxBytes int64) (Decision, *StepResult) {
	fail := func(outcome Outcome, code, msg string) (Decision, *StepResult) {
		return Decision{}, &StepResult{
			ID: state.currentStep, Outcome: outcome, ProxyUsed: true,
			Error: &ErrorInfo{Outcome: outcome, Code: code, Message: msg},
		}
	}
	confirmer := e.confirmer
	if c := interact.ConfirmerFrom(ctx); c != nil {
		confirmer = c
	}
	if confirmer == nil {
		return fail(OutcomeConfirmationRequired, "no_confirmer", "this action requires confirmation but no safe confirmation channel is available")
	}
	if !scope.Interactive() {
		return fail(OutcomeConfirmationRequired, "background_mode", "confirmations require an interactive channel")
	}

	action := Confirmation{
		Nonce:       NewNonce(),
		OperationID: state.opID,
		Fingerprint: state.fingerprint,
		Reason:      string(reason),
		Preview:     preview,
		MaxBytes:    maxBytes,
		IssuedAt:    time.Now(),
		ExpiresAt:   time.Now().Add(e.limits.ConsentWait),
	}
	expect := Actor{
		UserID:          scope.UserID,
		ChannelInstance: scope.ChannelInstance,
	}

	confirmCtx, cancel := context.WithTimeout(ctx, e.limits.ConsentWait)
	defer cancel()

	decision, err := confirmer.Confirm(confirmCtx, action, expect)
	if err != nil {
		if errors.Is(err, ErrConfirmUnavailable) {
			e.metrics.ConsentAsked(string(reason), "unavailable")
			return fail(OutcomeConfirmationRequired, "no_confirmer", err.Error())
		}
		if errors.Is(err, interact.ErrConfirmRejected) {
			e.metrics.ConsentAsked(string(reason), "declined")
			return fail(OutcomeDeclined, "declined", "user declined")
		}
		if errors.Is(err, context.Canceled) {
			e.metrics.ConsentAsked(string(reason), "cancelled")               //nolint:misspell // contract value
			return fail(OutcomeCancelled, "cancelled", "operation cancelled") //nolint:misspell // contract value
		}
		if errors.Is(err, context.DeadlineExceeded) {
			e.metrics.ConsentAsked(string(reason), "timeout")
			return fail(OutcomeTimedOut, "consent_timeout", "confirmation timed out")
		}
		e.metrics.ConsentAsked(string(reason), "error")
		return fail(OutcomeDeclined, "consent_error", "confirmation failed")
	}
	// The confirmer must return a verified actor; the engine additionally
	// cross-checks the actor against the turn's scope user when both exist.
	if scope.UserID != "" && decision.Actor.UserID != "" && decision.Actor.UserID != scope.UserID {
		e.metrics.ConsentAsked(string(reason), "actor_mismatch")
		return fail(OutcomeDeclined, "actor_mismatch", "confirmation actor does not match the requesting user")
	}
	if !decision.Approved {
		e.metrics.ConsentAsked(string(reason), "declined")
		return fail(OutcomeDeclined, "declined", "user declined")
	}
	e.metrics.ConsentAsked(string(reason), "approved")
	if maxBytes > 0 && decision.MaxBytes > maxBytes {
		decision.MaxBytes = maxBytes
	}
	return decision, nil
}

// hasStandingGrant reports whether the owner issued a covering "always
// allow" grant. Storage failures fail CLOSED (ask again) — never silently
// mutate on an unreadable grants table.
func (e *Engine) hasStandingGrant(ctx context.Context, scope Scope, origin Origin) bool {
	if e.standing == nil || scope.UserID == "" {
		return false
	}
	ok, err := e.standing.HasStandingGrant(ctx, scope, origin)
	if err != nil {
		e.log.Warn("standing grant lookup failed; asking anyway", zap.Error(err))
		return false
	}
	return ok
}

// saveStandingGrant persists a standing approval. Failures are logged but
// never invalidate the one-shot approval the user just gave.
func (e *Engine) saveStandingGrant(ctx context.Context, scope Scope, origin Origin, standing, actor string) {
	if e.standing == nil || scope.UserID == "" {
		return
	}
	ttl := e.standingOriginTTL
	if standing == interact.StandingGlobal {
		ttl = e.standingGlobalTTL
	}
	if ttl <= 0 {
		if standing == interact.StandingGlobal {
			ttl = 7 * 24 * time.Hour
		} else {
			ttl = 30 * 24 * time.Hour
		}
	}
	if err := e.standing.SaveStandingGrant(ctx, scope, origin, standing, timeNow().Add(ttl)); err != nil {
		e.log.Warn("failed to persist standing grant; one-shot approval still applies",
			zap.Error(err), zap.String("standing", standing))
	}
	_ = actor
}

// mutationPreview renders the safe description of all remaining mutating
// steps for a single chain-wide confirmation.
func (e *Engine) mutationPreview(state *runState, idx, total int) string {
	lines := make([]string, 0, total)
	if idx < 0 {
		idx = 0
	}
	for i := idx; i < len(state.steps) && i < total; i++ {
		st := state.steps[i]
		method, err := validateMethod(st.Method)
		if err != nil || isSafeMethod(method) {
			continue
		}
		target := st.URL
		if st.URLFrom != nil {
			target = "(resolved from step " + st.URLFrom.Step + ")"
		} else if u, perr := url.Parse(st.URL); perr == nil {
			target = u.Scheme + "://" + u.Host + u.EscapedPath()
		}
		lines = append(lines, fmt.Sprintf("• %s %s", method, target))
	}
	if len(lines) == 0 {
		return "Perform the requested HTTP mutation."
	}
	out := "Approve the following HTTP operations (data comes from this request's plan):\n"
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

// acquireTransfer takes a network-transfer permit before the request is
// sent: global slots wait bounded and cancellably; per-user overflow fails
// fast with busy.
func (e *Engine) acquireTransfer(ctx context.Context, scope Scope) (func(), error) {
	select {
	case e.transferSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for a transfer slot: %w", ctx.Err())
	}
	e.metrics.TransferAcquired()
	e.mu.Lock()
	user := scope.UserID
	if user == "" {
		user = "(anonymous)"
	}
	if e.userTransfer[user] >= e.limits.MaxTransfersUser {
		e.mu.Unlock()
		<-e.transferSlots
		e.metrics.TransferReleased()
		return nil, fmt.Errorf("user transfer limit reached (%d)", e.limits.MaxTransfersUser)
	}
	e.userTransfer[user]++
	e.mu.Unlock()

	return func() {
		e.mu.Lock()
		e.userTransfer[user]--
		if e.userTransfer[user] <= 0 {
			delete(e.userTransfer, user)
		}
		e.mu.Unlock()
		<-e.transferSlots
		e.metrics.TransferReleased()
	}, nil
}
