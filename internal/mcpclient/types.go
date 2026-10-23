// Package mcpclient owns MCP connections, authorization and durable invocations.
// External descriptors never enter the global skill registry or system prompt.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

var (
	// ErrDenied reports missing owner authority without disclosing a foreign record.
	ErrDenied = errors.New("MCP access denied")
	// ErrInvalid reports rejected input without reflecting private argument values.
	ErrInvalid = errors.New("invalid MCP request")
	// ErrUnavailable reports a closed or unavailable admission or transport dependency.
	ErrUnavailable = errors.New("MCP unavailable; check connection settings")
	// ErrConflict requires renewed review after an authority or schema change.
	ErrConflict = errors.New("MCP settings changed; review the request again")
	// ErrApproval requires verified one-shot owner consent before execution.
	ErrApproval = errors.New("MCP operation requires owner confirmation")
)

// Repository persists encrypted documents with revision fencing and atomic safe audit.
type Repository interface {
	LoadMCPRecords(context.Context) ([]domain.MCPRecord, error)
	ListMCPEvents(context.Context, string) ([]domain.MCPRecordEvent, error)
	PruneMCPEvents(context.Context, time.Time) error
	GetMCPRecord(context.Context, string, string, string) (domain.MCPRecord, error)
	GetMCPOAuthFlow(context.Context, string) (domain.MCPRecord, error)
	SaveMCPRecord(context.Context, *domain.MCPRecord, int64) error
	DeleteMCPRecord(context.Context, string, string) error
}

// Vault provides mandatory authenticated encryption without credential fallback.
type Vault interface {
	EncryptionEnabled() bool
	SealPrivate(string) (string, error)
	OpenPrivate(string) (string, error)
}

// Scope comes from an authenticated adapter, never from model JSON.
type Scope struct {
	OwnerID, CallerID, ConversationID string
	Private                           bool
}
type scopeKey struct{}

// WithScope attaches authority created by a trusted authenticated adapter.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFrom extracts a proven private caller scope; model arguments cannot provide it.
func ScopeFrom(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(scopeKey{}).(Scope)
	return s, ok && s.OwnerID != "" && s.Private
}

// Connection describes a private connection without exporting authentication material.
type Connection struct {
	ID         string            `json:"id"`
	OwnerID    string            `json:"-"`
	Label      string            `json:"label"`
	Domain     string            `json:"data_domain"`
	Endpoint   string            `json:"endpoint,omitempty"`
	Transport  string            `json:"transport"`
	Profile    string            `json:"profile,omitempty"`
	AuthMode   string            `json:"auth_mode"`
	Token      string            `json:"-"`
	Headers    map[string]string `json:"-"`
	Desired    string            `json:"desired_state"`
	Health     string            `json:"runtime_state"`
	AuthState  string            `json:"auth_state"`
	Revision   int64             `json:"revision"`
	Generation int64             `json:"generation"`
	CheckedAt  time.Time         `json:"checked_at"`
	ErrorCode  string            `json:"error_code,omitempty"`
	ResultMode string            `json:"result_mode"`
	Items      []Item            `json:"items"`
}

// storedConnection explicitly includes secrets only inside the encrypted record.
type storedConnection struct {
	Connection
	Owner         string            `json:"owner"`
	Secret        string            `json:"secret,omitempty"`
	SecretHeaders map[string]string `json:"secret_headers,omitempty"`
	OAuth         *OAuthBinding     `json:"oauth,omitempty"`
}

// Draft contains owner-editable, non-secret connection settings.
type Draft struct {
	Label      string `json:"label"`
	Endpoint   string `json:"endpoint"`
	Domain     string `json:"data_domain"`
	Transport  string `json:"transport"`
	Profile    string `json:"profile,omitempty"`
	ResultMode string `json:"result_mode"`
}

// Item is an untrusted server descriptor with an explicit owner-reviewed revision.
type Item struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	URI         string          `json:"uri,omitempty"`
	Revision    string          `json:"revision"`
	Descriptor  json.RawMessage `json:"descriptor"`
	Effect      string          `json:"effect"`
	Allowed     bool            `json:"allowed"`
	ErrorCode   string          `json:"error_code,omitempty"`
}

// Review binds an owner access decision to an exact descriptor hash.
type Review struct {
	ItemID   string `json:"item_id"`
	Revision string `json:"revision"`
	Effect   string `json:"effect"`
	Allowed  bool   `json:"allowed"`
}

// Call identifies a reviewed remote operation and bounded schema-validated arguments.
type Call struct {
	ConnectionID       string          `json:"connection_id"`
	ItemID             string          `json:"item_id"`
	DescriptorRevision string          `json:"descriptor_revision"`
	Arguments          json.RawMessage `json:"arguments"`
	TimeoutSeconds     int             `json:"timeout_seconds,omitempty"`
}

// Plan freezes the operation and authority to which one-shot approval applies.
type Plan struct {
	ID                   string    `json:"id"`
	Owner                string    `json:"-"`
	Caller               string    `json:"-"`
	Conversation         string    `json:"-"`
	Call                 Call      `json:"call"`
	Generation           int64     `json:"generation"`
	ConnectionRevision   int64     `json:"connection_revision"`
	Hash                 string    `json:"hash"`
	Preview              string    `json:"preview"`
	ExpiresAt            time.Time `json:"expires_at"`
	RequiresConfirmation bool      `json:"requires_confirmation"`
	Approved             bool      `json:"-"`
	Consumed             bool      `json:"-"`
}

// Execution contains safe durable metadata and an optional bounded delivery projection.
type Execution struct {
	ID              string    `json:"id"`
	Owner           string    `json:"-"`
	ConnectionID    string    `json:"connection_id"`
	ItemID          string    `json:"item_id"`
	PlanHash        string    `json:"plan_hash"`
	State           string    `json:"state"`
	ErrorCode       string    `json:"error_code,omitempty"`
	SideEffectState string    `json:"side_effect_state"`
	CreatedAt       time.Time `json:"created_at"`
	FinishedAt      time.Time `json:"finished_at"`
	ArtifactID      string    `json:"artifact_id,omitempty"`
	Text            string    `json:"text,omitempty"`
	Truncated       bool      `json:"truncated"`
}
type storedExecution struct {
	Execution
	OwnerID string `json:"owner_id"`
}

// Artifact represents private result bytes and their expiration and access metadata.
type Artifact struct {
	ID          string          `json:"id"`
	Owner       string          `json:"owner"`
	ExecutionID string          `json:"execution_id"`
	Payload     json.RawMessage `json:"payload"`
	ExpiresAt   time.Time       `json:"expires_at"`
}

// Policy describes installation admission, approved origins and the policy epoch.
type Policy struct {
	UpdatedBy      string   `json:"updated_by,omitempty"`
	Enabled        bool     `json:"enabled"`
	AllowedOrigins []string `json:"allowed_origins"`
	Revision       int64    `json:"revision"`
}

// Snapshot returns only the authenticated owner connection and operation metadata.
type Snapshot struct {
	StdioEnabled bool         `json:"stdio_enabled"`
	Enabled      bool         `json:"enabled"`
	Connections  []Connection `json:"connections"`
	Executions   []Execution  `json:"executions"`
}

// Protocol separates wire transport from authorization and durable dispatch.
type Protocol interface {
	Discover(context.Context, Connection) ([]Item, error)
	Invoke(context.Context, Connection, Item, json.RawMessage, func() error) (json.RawMessage, bool, error)
}

// Options supplies deployment-owned dependencies and destination restrictions.
type Options struct {
	// StdioEnabled is reserved for isolated broker integrations. The application
	// does not enable it: its released control plane is remote HTTPS only.
	StdioEnabled     bool
	Repository       Repository
	Vault            Vault
	Protocol         Protocol
	LockPath         string
	Root             context.Context
	Enabled          bool
	AllowedOrigins   []string
	PrincipalChecker func(context.Context, string) bool
}

// ErrBusy defines the err busy MCP contract.
var ErrBusy = errors.New("MCP connection has active operations; finish or cancel them first")
