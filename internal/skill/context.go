package skill

import (
	"context"
	"sync"
)

type ctxKey int

const (
	ctxChatID ctxKey = iota
	ctxUserID
	ctxUserRole
	ctxDocuments
	ctxTurnTaint
)

// turnTaint is a per-turn mutable flag set carried by pointer in the context so
// one skill call can taint later skill calls in the SAME turn (context values
// are immutable, so a plain value can't flow back up the loop).
type turnTaint struct {
	mu                sync.Mutex
	slackSearchUsed   bool
	remoteOutputUsed  bool
	remoteOutputBytes int
}

// WithTurnTaint attaches a fresh per-turn taint holder. Call once at turn start.
func WithTurnTaint(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxTurnTaint, &turnTaint{})
}

// HasTurnTaint reports whether a taint holder is present. Entry points other than
// the main conversation loop (e.g. agent jobs) use this to install one only if
// absent, so MarkSlackSearchUsed never silently no-ops (fail-open) — while
// preserving the shared holder for orchestrate sub-agents.
func HasTurnTaint(ctx context.Context) bool {
	_, ok := ctx.Value(ctxTurnTaint).(*turnTaint)
	return ok
}

// MarkSlackSearchUsed records that slack_search ran this turn. This is a
// server-enforced signal — it does NOT depend on the LLM reporting provenance —
// so that content derived from (untrusted) Slack search results can be forced
// through draft approval even on an auto-post channel.
func MarkSlackSearchUsed(ctx context.Context) {
	if t, ok := ctx.Value(ctxTurnTaint).(*turnTaint); ok {
		t.mu.Lock()
		t.slackSearchUsed = true
		t.mu.Unlock()
	}
}

// SlackSearchUsedInTurn reports whether slack_search ran earlier this turn.
func SlackSearchUsedInTurn(ctx context.Context) bool {
	if t, ok := ctx.Value(ctxTurnTaint).(*turnTaint); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.slackSearchUsed
	}
	return false
}

// MarkRemoteOutputUsed taints this turn after approved SSH output enters model context.
// The shared flag is enforced by the main dispatcher, not by model instructions.
func MarkRemoteOutputUsed(ctx context.Context) {
	if t, ok := ctx.Value(ctxTurnTaint).(*turnTaint); ok {
		t.mu.Lock()
		t.remoteOutputUsed = true
		t.mu.Unlock()
	}
}

// RemoteOutputUsedInTurn reports whether this turn consumed untrusted SSH output.
func RemoteOutputUsedInTurn(ctx context.Context) bool {
	if t, ok := ctx.Value(ctxTurnTaint).(*turnTaint); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.remoteOutputUsed
	}
	return false
}

// ConsumeRemoteOutputBudget bounds all SSH model projections in a turn to 64 KiB.
// Missing turn state fails closed rather than allowing unlimited direct calls.
func ConsumeRemoteOutputBudget(ctx context.Context, requested int) int {
	if requested <= 0 {
		return 0
	}
	if t, ok := ctx.Value(ctxTurnTaint).(*turnTaint); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		available := 64*1024 - t.remoteOutputBytes
		if requested > available {
			requested = available
		}
		t.remoteOutputBytes += requested
		return requested
	}
	return 0
}

// DocumentAttachment holds a file received from a channel, available to skills via context.
type DocumentAttachment struct {
	Data     []byte
	MimeType string
	Filename string
}

// WithChatID returns a context with the chat ID set.
func WithChatID(ctx context.Context, chatID string) context.Context {
	return context.WithValue(ctx, ctxChatID, chatID)
}

// ChatIDFrom extracts the chat ID from the context, or returns empty string.
func ChatIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxChatID).(string)
	return v
}

// WithUserID returns a context with the user ID set.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxUserID, userID)
}

// UserIDFrom extracts the user ID from the context, or returns empty string.
func UserIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserID).(string)
	return v
}

// WithUserRole returns a context with the user role set.
func WithUserRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, ctxUserRole, role)
}

// UserRoleFrom extracts the user role from the context, or returns empty string.
func UserRoleFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserRole).(string)
	return v
}

// WithDocuments returns a context with document attachments set.
func WithDocuments(ctx context.Context, docs []DocumentAttachment) context.Context {
	return context.WithValue(ctx, ctxDocuments, docs)
}

// DocumentsFrom extracts document attachments from the context, or returns nil.
func DocumentsFrom(ctx context.Context) []DocumentAttachment {
	v, _ := ctx.Value(ctxDocuments).([]DocumentAttachment)
	return v
}

// remoteCallerKey is deliberately private: only trusted adapters set this context.
type remoteCallerKey struct{}

// WithRemoteCaller binds a trusted execution entry point; tool input cannot set it.
func WithRemoteCaller(ctx context.Context, caller string) context.Context {
	return context.WithValue(ctx, remoteCallerKey{}, caller)
}

// RemoteCallerFrom returns the trusted delegate or scheduler scope.
func RemoteCallerFrom(ctx context.Context) string {
	v, ok := ctx.Value(remoteCallerKey{}).(string)
	if !ok {
		return ""
	}
	return v
}
