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

	ID           int64     `bun:"id,pk,autoincrement"`
	ChatID       string    `bun:"chat_id,notnull"`
	UserID       string    `bun:"user_id,notnull,default:''"` // iulita user UUID
	Hour         time.Time `bun:"hour,notnull"`               // truncated to hour
	InputTokens  int64     `bun:"input_tokens,notnull,default:0"`
	OutputTokens int64     `bun:"output_tokens,notnull,default:0"`
	Requests     int64     `bun:"requests,notnull,default:0"`
	CostUSD      float64   `bun:"cost_usd,notnull,default:0"`
}
