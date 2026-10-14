package domain

import (
	"time"

	"github.com/uptrace/bun"
)

// CredentialType classifies a secret's structure and intended use.
type CredentialType string

// Credential type constants.
const (
	CredentialTypeAPIKey             CredentialType = "api_key"
	CredentialTypeBearer             CredentialType = "bearer"
	CredentialTypeHTTPBasic          CredentialType = "http_basic"    // JSON {"username","password"} for the HTTP tool
	CredentialTypeOAuth2Client       CredentialType = "oauth2_client" //nolint:gosec // G101 false positive: these are type constants not credentials
	CredentialTypeOAuth2Tokens       CredentialType = "oauth2_tokens" //nolint:gosec // G101 false positive: these are type constants not credentials
	CredentialTypeServiceAccountJSON CredentialType = "service_account_json"
	CredentialTypeBotToken           CredentialType = "bot_token"
)

// CredentialScope determines access pattern: global (admin-set) or user (per-user).
type CredentialScope string

// Credential scope constants.
const (
	CredentialScopeGlobal CredentialScope = "global"
	CredentialScopeUser   CredentialScope = "user"
)

// Credential is a named secret with its encrypted value.
type Credential struct {
	bun.BaseModel `bun:"table:credentials"`

	ID          int64           `bun:"id,pk,autoincrement" json:"id"`
	Name        string          `bun:"name,notnull" json:"name"`
	Type        CredentialType  `bun:"type,notnull,default:'api_key'" json:"type"`
	Scope       CredentialScope `bun:"scope,notnull,default:'global'" json:"scope"`
	OwnerID     string          `bun:"owner_id,notnull,default:''" json:"owner_id,omitempty"`
	Value       string          `bun:"value,notnull,default:''" json:"-"`
	Encrypted   bool            `bun:"encrypted,notnull,default:false" json:"encrypted"`
	Description string          `bun:"description,notnull,default:''" json:"description"`
	Tags        string          `bun:"tags,notnull,default:'[]'" json:"tags"`
	CreatedBy   string          `bun:"created_by,notnull,default:''" json:"created_by"`
	UpdatedBy   string          `bun:"updated_by,notnull,default:''" json:"updated_by"`
	CreatedAt   time.Time       `bun:"created_at,nullzero,notnull,default:current_timestamp" json:"created_at"`
	UpdatedAt   time.Time       `bun:"updated_at,nullzero,notnull,default:current_timestamp" json:"updated_at"`
	RotatedAt   *time.Time      `bun:"rotated_at" json:"rotated_at,omitempty"`
	ExpiresAt   *time.Time      `bun:"expires_at" json:"expires_at,omitempty"`
}

// CredentialBinding links a credential to a named consumer.
type CredentialBinding struct {
	bun.BaseModel `bun:"table:credential_bindings"`

	ID           int64     `bun:"id,pk,autoincrement" json:"id"`
	CredentialID int64     `bun:"credential_id,notnull" json:"credential_id"`
	ConsumerType string    `bun:"consumer_type,notnull" json:"consumer_type"`
	ConsumerID   string    `bun:"consumer_id,notnull" json:"consumer_id"`
	CreatedAt    time.Time `bun:"created_at,nullzero,notnull,default:current_timestamp" json:"created_at"`
	CreatedBy    string    `bun:"created_by,notnull,default:''" json:"created_by"`
}

// Consumer type constants.
const (
	CredentialConsumerConfigKey       = "config_key"
	CredentialConsumerChannelInstance = "channel_instance"
	CredentialConsumerLLMProvider     = "llm_provider"
	CredentialConsumerSkill           = "skill"
)

// CredentialAudit is an immutable audit record for credential operations.
type CredentialAudit struct {
	bun.BaseModel `bun:"table:credential_audit"`

	ID             int64     `bun:"id,pk,autoincrement" json:"id"`
	CredentialID   *int64    `bun:"credential_id" json:"credential_id,omitempty"`
	CredentialName string    `bun:"credential_name,notnull" json:"credential_name"`
	Action         string    `bun:"action,notnull" json:"action"`
	Actor          string    `bun:"actor,notnull,default:''" json:"actor"`
	Detail         string    `bun:"detail,notnull,default:''" json:"detail"`
	CreatedAt      time.Time `bun:"created_at,nullzero,notnull,default:current_timestamp" json:"created_at"`
}

// HTTPCredentialGrant binds a credential to an exact HTTP origin
// (scheme+host+effective port) for a principal and optionally a caller.
// The universal HTTP tool NEVER resolves credentials without a matching
// grant — global credentials become usable only through an explicit
// published grant with allowed origins (plan §8.1).
type HTTPCredentialGrant struct {
	bun.BaseModel `bun:"table:http_credential_grants"`

	ID            int64      `bun:"id,pk,autoincrement" json:"id"`
	CredentialID  int64      `bun:"credential_id,notnull" json:"credential_id"`
	PrincipalType string     `bun:"principal_type,notnull,default:'user'" json:"principal_type"` // user | global
	PrincipalID   string     `bun:"principal_id,notnull,default:''" json:"principal_id"`         // domain user ID or policy ID
	CallerID      string     `bun:"caller_id,notnull,default:''" json:"caller_id"`               // "" = any caller
	Scheme        string     `bun:"scheme,notnull" json:"scheme"`                                // http | https
	Host          string     `bun:"host,notnull" json:"host"`                                    // canonical lowercase
	Port          string     `bun:"port,notnull" json:"port"`                                    // effective port
	Usages        string     `bun:"usages,notnull,default:'[]'" json:"usages"`                   // JSON array of usages
	ExpiresAt     *time.Time `bun:"expires_at" json:"expires_at,omitempty"`
	CreatedAt     time.Time  `bun:"created_at,nullzero,notnull,default:current_timestamp" json:"created_at"`
	CreatedBy     string     `bun:"created_by,notnull,default:''" json:"created_by"`
}

// HTTP grant principal types.
const (
	HTTPGrantPrincipalUser   = "user"
	HTTPGrantPrincipalGlobal = "global"
)

// Audit action constants.
const (
	CredentialAuditCreated = "created"
	CredentialAuditUpdated = "updated"
	CredentialAuditRotated = "rotated"
	CredentialAuditDeleted = "deleted"
	CredentialAuditBound   = "bound"
	CredentialAuditUnbound = "unbound"
)
