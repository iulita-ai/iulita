package domain

import "time"

// TechFact represents auto-generated metadata about user behavior.
// Unlike Facts (user-managed memories), TechFacts are system-generated
// and not directly editable by the user.
type TechFact struct {
	ID          int64     `bun:",pk,autoincrement" json:"id"`
	ChatID      string    `bun:",notnull" json:"chat_id"`            // source channel
	UserID      string    `bun:",notnull,default:''" json:"user_id"` // owner user UUID
	Category    string    `bun:",notnull" json:"category"`           // "language", "topic", "pattern", "style"
	Key         string    `bun:",notnull" json:"key"`                // e.g. "primary_language", "topic:cooking"
	Value       string    `bun:",notnull" json:"value"`              // e.g. "Russian", "high"
	Confidence  float64   `bun:",default:0" json:"confidence"`       // 0.0–1.0
	UpdateCount int       `bun:",default:1" json:"update_count"`
	CreatedAt   time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"created_at"`
	UpdatedAt   time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"updated_at"`
}
