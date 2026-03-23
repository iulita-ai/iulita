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

// Audit action constants.
const (
	CredentialAuditCreated = "created"
	CredentialAuditUpdated = "updated"
	CredentialAuditRotated = "rotated"
	CredentialAuditDeleted = "deleted"
	CredentialAuditBound   = "bound"
	CredentialAuditUnbound = "unbound"
)
