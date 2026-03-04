package domain

import "time"

// Fact represents a piece of information remembered by the assistant.
// Facts belong to a user (shared across all their channels) but track
// the source channel via ChatID.
type Fact struct {
	ID             int64     `bun:",pk,autoincrement"`
	ChatID         string    `bun:",notnull"`            // source channel chat ID
	UserID         string    `bun:",notnull,default:''"` // owner user UUID
	Content        string    `bun:",notnull"`
	SourceType     string    `bun:",notnull,default:'user'"` // user, system
	CreatedAt      time.Time `bun:",nullzero,notnull,default:current_timestamp"`
	LastAccessedAt time.Time `bun:",nullzero,notnull,default:current_timestamp"`
	AccessCount    int       `bun:",notnull,default:0"`
}
