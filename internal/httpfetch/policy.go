package httpfetch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// ExecMode distinguishes interactive turns from unattended background work.
type ExecMode string

const (
	// ModeInteractive marks turns with a reachable human.
	ModeInteractive ExecMode = "interactive"
	// ModeBackground marks unattended execution (consent unavailable).
	ModeBackground ExecMode = "background"
)

// CallerKind classifies who is invoking the engine.
type CallerKind string

const (
	// CallerTool marks an LLM tool call via http_request.
	CallerTool CallerKind = "tool"
	// CallerNativeSkill marks another Go skill using the Factory.
	CallerNativeSkill CallerKind = "native_skill"
	// CallerSystem marks trusted host wiring (e.g. importer).
	CallerSystem CallerKind = "system"
)

// Caller identifies the invoking component. It is set by host wiring, never
// by model JSON.
type Caller struct {
	ID   string     // e.g. "http_request", "weather", "importer"
	Kind CallerKind `json:"kind"`
}

// Scope is the trusted execution scope derived from context values: domain
// user ID, chat, channel instance, caller, and mode. No scope → fail closed
// for credentials, artifacts, and interactive consent.
// NativeArtifactPrefix separates native attachments from legacy dashboard artifacts.
const NativeArtifactPrefix = "native-http:"

type Scope struct {
	NativeConversation string
	NativeGeneration   uint64
	UserID             string
	ChatID             string
	ChannelInstance    string
	Caller             Caller
	Mode               ExecMode
}

type ctxScopeKey struct{}

// WithScope attaches the trusted scope to a context (host-side only).
func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, ctxScopeKey{}, scope)
}

// ScopeFrom extracts the scope; ok=false when absent.
func ScopeFrom(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(ctxScopeKey{}).(Scope)
	return s, ok
}

// ArtifactBinding is metadata only; it is never a legacy channel route.
func (s Scope) ArtifactBinding() string {
	if s.NativeConversation != "" {
		return fmt.Sprintf("%s%s:%d", NativeArtifactPrefix, s.NativeConversation, s.NativeGeneration)
	}
	return s.ChatID
}

// Interactive reports whether a human is reachable for confirmations.
func (s Scope) Interactive() bool { return s.Mode == ModeInteractive }

// Origin is an exact origin: scheme + canonical host + effective port.
type Origin struct {
	Scheme string
	Host   string // canonical lowercase hostname (no port)
	Port   string // effective port (explicit or scheme default)
}

// ParseOrigin computes the exact origin of a URL.
func ParseOrigin(u *url.URL) Origin {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return Origin{Scheme: u.Scheme, Host: host, Port: port}
}

func (o Origin) String() string {
	return o.Scheme + "://" + o.Host + ":" + o.Port
}

// Equal reports exact-origin equality (scheme+host+effective port).
func (o Origin) Equal(other Origin) bool {
	return o.Scheme == other.Scheme && equalFoldASCII(o.Host, other.Host) && o.Port == other.Port
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// Policy is the runtime policy state consulted on every step.
type Policy struct {
	Enabled        bool
	AllowedMethods map[string]bool // empty = all supported methods
	AllowedPorts   map[int]bool    // empty = all ports 1-65535
}

// MethodAllowed checks the method against the configured allowlist.
func (p *Policy) MethodAllowed(method string) bool {
	if len(p.AllowedMethods) == 0 {
		return true
	}
	return p.AllowedMethods[normalizeMethod(method)]
}

// PortAllowed checks the effective port (1-65535 by default).
func (p *Policy) PortAllowed(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	if len(p.AllowedPorts) == 0 {
		return true
	}
	return p.AllowedPorts[port]
}

// ConfirmReason explains why a confirmation is required.
type ConfirmReason string

const (
	// ConfirmLargeDownload asks consent for reading past the byte threshold.
	ConfirmLargeDownload ConfirmReason = interact.ConfirmLargeDownload
	// ConfirmMutation asks consent for a mutating HTTP method.
	ConfirmMutation ConfirmReason = interact.ConfirmMutation
	// ConfirmInsecureCredential asks consent for secrets over plaintext.
	ConfirmInsecureCredential ConfirmReason = interact.ConfirmInsecureCredential
	// ConfirmSensitiveEgress asks consent for cross-origin secret transfer.
	ConfirmSensitiveEgress ConfirmReason = interact.ConfirmSensitiveEgress
)

// Confirmation / Decision / Actor are canonical in the interact package so
// channel implementations never import the engine.
type Confirmation = interact.Confirmation

// Decision is the canonical interact confirmation decision.
type Decision = interact.Decision

// Actor is the canonical interact verified responder identity.
type Actor = interact.Actor

// Confirmer asks a verified human for consent. Implementations must bind the
// answer to the nonce, the actor identity, and the chat/channel context, and
// must fail closed on timeout/unknown/cancel.
type Confirmer = interact.ConfirmationAsker

// ErrConfirmUnavailable is returned when no safe confirmer exists for the
// channel; the engine surfaces outcome confirmation_required, never a
// chat-text fallback.
var ErrConfirmUnavailable = errors.New("no identity-bound confirmer available")

// CredentialUsage describes what the credential will be used for.
type CredentialUsage string

const (
	// CredUsageBasic grants use as HTTP Basic material.
	CredUsageBasic CredentialUsage = "basic"
	// CredUsageBearer grants use as a bearer token.
	CredUsageBearer CredentialUsage = "bearer"
	// CredUsageHeader grants use as a secret header value.
	CredUsageHeader CredentialUsage = "header"
	// CredUsageMultipart grants use inside multipart bodies.
	CredUsageMultipart CredentialUsage = "multipart"
)

// ResolvedCredential is a narrow, usage-scoped credential value resolved by
// trusted host code (never from model JSON).
type ResolvedCredential struct {
	Kind     string // basic|bearer|secret
	Username string
	Password string
	Token    string
	Secret   string
}

// CredentialResolver is the narrow HTTP credential interface. Unlike
// credential.ResolveForUser it must NOT fall back to global/env/config
// secrets: origin grants are checked by the implementation.
type CredentialResolver interface {
	ResolveHTTP(ctx context.Context, scope Scope, ref string, origin Origin, usage CredentialUsage) (*ResolvedCredential, error)
}

// ErrCredentialDenied mirrors credential.ErrCredentialDenied for engine-side
// checks; the authoritative sentinel lives in the credential package.
var ErrCredentialDenied = errors.New("credential not granted for this origin/caller")

// ArtifactWriter is a scoped handle for streaming artifact output.
type ArtifactWriter interface {
	Write(p []byte) (int, error)
	Commit(hash string, size int64) error
	Abort() error
}

// ArtifactReader is a scoped handle for reading a stored artifact.
type ArtifactReader interface {
	Read(p []byte) (int, error)
	Close() error
	Size() int64
}

// ArtifactSpec describes the artifact to create.
type ArtifactSpec struct {
	Filename    string
	MIME        string
	ContentEnc  string
	Sensitive   bool
	OwnerUserID string
	ChatID      string
	MaxBytes    int64
}

// ArtifactStore stores and serves run artifacts with owner scoping, quota
// reservations, and TTL cleanup.
type ArtifactStore interface {
	Begin(ctx context.Context, spec ArtifactSpec) (ArtifactWriter, error)
	Open(ctx context.Context, ownerUserID, id string) (ArtifactReader, error)
	Delete(ctx context.Context, ownerUserID, id string) error
}

// NewNonce creates a cryptographically random one-shot nonce.
func NewNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("nonce-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// NewOperationID creates a server-opaque operation identifier.
func NewOperationID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return "op_" + hex.EncodeToString(b[:])
}

// StandingGrantStore persists user-issued standing mutation approvals.
// Engine-side narrow interface; the SQLite implementation lives in storage.
type StandingGrantStore interface {
	// HasStandingGrant reports whether an unrevoked, unexpired grant covers
	// the exact origin (or globally) for this owner.
	HasStandingGrant(ctx context.Context, scope Scope, origin Origin) (bool, error)
	// SaveStandingGrant records a grant issued by this owner's decision.
	SaveStandingGrant(ctx context.Context, scope Scope, origin Origin, standing string, expiresAt time.Time) error
}
