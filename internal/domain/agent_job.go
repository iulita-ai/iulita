package domain

import "time"

// AgentJob defines a user-created scheduled LLM task.
type AgentJob struct {
	ID             int64     `bun:",pk,autoincrement" json:"id"`
	Name           string    `bun:",notnull" json:"name"`
	Prompt         string    `bun:",notnull" json:"prompt"`
	Model          string    `bun:",notnull,default:''" json:"model"`            // "claude", "ollama", "" = default
	CronExpr       string    `bun:",notnull,default:''" json:"cron_expr"`        // cron expression (overrides interval)
	Interval       string    `bun:",notnull,default:'24h'" json:"interval"`      // Go duration fallback
	DeliveryChatID string    `bun:",notnull,default:''" json:"delivery_chat_id"` // chat to deliver results to
	Enabled        bool      `bun:",notnull,default:true" json:"enabled"`
	LastRun        time.Time `bun:",nullzero" json:"last_run,omitempty"`
	NextRun        time.Time `bun:",nullzero" json:"next_run,omitempty"`
	CreatedAt      time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"created_at"`
	UpdatedAt      time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"updated_at"`
}
