package domain

import "time"

// Insight represents a synthesized insight generated from cross-cluster fact analysis.
// Insights belong to a user (shared across channels).
type Insight struct {
	ID             int64      `bun:",pk,autoincrement" json:"id"`
	ChatID         string     `bun:",notnull" json:"chat_id"`            // source channel
	UserID         string     `bun:",notnull,default:''" json:"user_id"` // owner user UUID
	Content        string     `bun:",notnull" json:"content"`
	FactIDs        string     `bun:",notnull" json:"fact_ids"` // comma-separated fact IDs used
	Quality        int        `bun:",default:0" json:"quality"`
	AccessCount    int        `bun:",default:0" json:"access_count"`
	LastAccessedAt time.Time  `bun:",nullzero" json:"last_accessed_at"`
	ExpiresAt      *time.Time `bun:",nullzero" json:"expires_at"`
	CreatedAt      time.Time  `bun:",nullzero,notnull,default:current_timestamp" json:"created_at"`
}
