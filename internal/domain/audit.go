package domain

import (
	"time"

	"github.com/uptrace/bun"
)

// AuditEntry records a skill execution or other auditable action.
type AuditEntry struct {
	bun.BaseModel `bun:"table:audit_log"`

	ID         int64     `bun:"id,pk,autoincrement"`
	ChatID     string    `bun:"chat_id,notnull"`
	UserID     string    `bun:"user_id,notnull,default:''"` // iulita user UUID
	Action     string    `bun:"action,notnull"`             // e.g. "skill.executed"
	Detail     string    `bun:"detail,notnull"`             // e.g. skill name
	Success    bool      `bun:"success"`
	DurationMs int64     `bun:"duration_ms"`
	CreatedAt  time.Time `bun:"created_at,notnull,default:current_timestamp"`
}

// UsageRecord tracks per-chat LLM token usage, aggregated hourly.
type UsageRecord struct {
	bun.BaseModel `bun:"table:usage_stats"`

	ID                   int64     `bun:"id,pk,autoincrement"`
	ChatID               string    `bun:"chat_id,notnull"`
	UserID               string    `bun:"user_id,notnull,default:''"` // iulita user UUID
	Model                string    `bun:"model,notnull,default:''"`
	Provider             string    `bun:"provider,notnull,default:''"`
	Hour                 time.Time `bun:"hour,notnull"` // truncated to hour
	InputTokens          int64     `bun:"input_tokens,notnull,default:0"`
	OutputTokens         int64     `bun:"output_tokens,notnull,default:0"`
	CacheReadTokens      int64     `bun:"cache_read_tokens,notnull,default:0"`
	CacheCreationTokens  int64     `bun:"cache_creation_tokens,notnull,default:0"`
	Requests             int64     `bun:"requests,notnull,default:0"`
	CostUSD              float64   `bun:"cost_usd,notnull,default:0"`
	CostKnownRequests    int64     `bun:"cost_known_requests,notnull,default:0"`
	CostUnknownRequests  int64     `bun:"cost_unknown_requests,notnull,default:0"`
	UsageUnknownRequests int64     `bun:"usage_unknown_requests,notnull,default:0"`
}

// LLMUsageAttempt stores only identity and accounting metadata. Raw requests,
// completions, tool payloads, reasoning and errors never enter this ledger.
type LLMUsageAttempt struct {
	bun.BaseModel       `bun:"table:llm_usage_attempts"`
	AttemptID           string    `bun:"attempt_id,pk"`
	ChatID              string    `bun:"chat_id,notnull"`
	UserID              string    `bun:"user_id,notnull,default:''"`
	Provider            string    `bun:"provider,notnull"`
	RequestedModel      string    `bun:"requested_model,notnull"`
	Model               string    `bun:"model,notnull"`
	ModelVerified       bool      `bun:"model_verified"`
	ProfileID           string    `bun:"profile_id,notnull,default:''"`
	Role                string    `bun:"role,notnull,default:''"`
	Operation           string    `bun:"operation,notnull,default:''"`
	PolicyRevision      uint64    `bun:"policy_revision"`
	StartedAt           time.Time `bun:"started_at,notnull"`
	CompletedAt         time.Time `bun:"completed_at,notnull"`
	Status              string    `bun:"status,notnull"`
	UsageAvailable      bool      `bun:"usage_available"`
	ChargeUnknown       bool      `bun:"charge_unknown"`
	Cached              bool      `bun:"cached"`
	InputTokens         int64     `bun:"input_tokens"`
	OutputTokens        int64     `bun:"output_tokens"`
	CacheReadTokens     int64     `bun:"cache_read_tokens"`
	CacheCreationTokens int64     `bun:"cache_creation_tokens"`
	EstimatedUSD        *float64  `bun:"estimated_usd"`
	CostStatus          string    `bun:"cost_status,notnull"`
	PriceVersion        string    `bun:"price_version,notnull"`
	PriceSource         string    `bun:"price_source,notnull"`
}
