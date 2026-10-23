package interact

import (
	"context"
	"errors"
	"time"
)

// Confirmation carries everything a channel needs to render a security
// confirmation AND verify the answer: the server-created one-shot nonce, the
// operation fingerprint it is bound to, and the safe preview text. The
// nonce/fingerprint must never originate from tool input or message text.
type Confirmation struct {
	Nonce       string    `json:"nonce"`
	OperationID string    `json:"operation_id"`
	Fingerprint string    `json:"fingerprint"`
	Reason      string    `json:"reason"`  // large_download | mutation | insecure_credentials | sensitive_egress
	Preview     string    `json:"preview"` // safe, sanitized human text
	MaxBytes    int64     `json:"max_bytes,omitempty"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// ConfirmReason values (shared with the HTTP engine).
const (
	// MCP always uses exact one-shot approvals; HTTP standing-origin/global
	// consent buttons must never be offered for MCP operations.
	ConfirmMCP                = "mcp_operation"
	ConfirmLargeDownload      = "large_download"
	ConfirmMutation           = "mutation"
	ConfirmInsecureCredential = "insecure_credentials"
	ConfirmSensitiveEgress    = "sensitive_egress"
)

// Actor is the verified responder identity. Channel implementations MUST
// fill it from their platform-native authenticated source (callback query
// sender, authenticated socket, local OS user) — never from message text.
type Actor struct {
	UserID          string `json:"user_id"` // domain user ID
	Channel         string `json:"channel"` // telegram|slack|webchat|console
	ChannelInstance string `json:"channel_instance,omitempty"`
	ExternalID      string `json:"external_id,omitempty"` // platform-native ID
}

// Decision is the outcome of a confirmation. Approved decisions carry the
// verified actor; the engine cross-checks it against the requesting user.
// Standing converts a one-shot approval into a persistent grant — the
// engine decides which reasons may earn one (mutations only).
type Decision struct {
	Approved bool
	Actor    Actor
	MaxBytes int64  // granted byte limit (≤ requested)
	Standing string // "" (one-shot) | StandingOrigin | StandingGlobal
}

// Standing-grant choices.
const (
	StandingOrigin = "origin" // always allow this exact origin
	StandingGlobal = "global" // always allow any origin
)

// ErrConfirmRejected signals an explicit denial.
var ErrConfirmRejected = errors.New("confirmation rejected")

// ConfirmationAsker presents identity-bound security confirmations. Unlike
// PromptAsker, implementations must bind the answer to the nonce AND the
// responder's platform identity, consume the nonce exactly once, and fail
// closed on timeout/unknown/overwrite. Free-text approval is not accepted
// unless the implementation can verify the sender the same way.
type ConfirmationAsker interface {
	Confirm(ctx context.Context, action Confirmation, expect Actor) (Decision, error)
}

// ConfirmationAskerFactory provides chat-scoped confirmation askers.
// Implemented by channel prompter factories.
type ConfirmationAskerFactory interface {
	ConfirmerFor(chatID string) ConfirmationAsker
}

type ctxConfirmerKey struct{}

// WithConfirmer attaches a ConfirmationAsker to the context.
func WithConfirmer(ctx context.Context, asker ConfirmationAsker) context.Context {
	return context.WithValue(ctx, ctxConfirmerKey{}, asker)
}

// ConfirmerFrom extracts the ConfirmationAsker; nil when absent (fail closed).
func ConfirmerFrom(ctx context.Context) ConfirmationAsker {
	if v, ok := ctx.Value(ctxConfirmerKey{}).(ConfirmationAsker); ok && v != nil {
		return v
	}
	return nil
}
