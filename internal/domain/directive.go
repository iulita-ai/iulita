package domain

import "time"

// Directive stores a user's custom instructions for the assistant.
// Directives belong to a user (shared across channels).
type Directive struct {
	ID        int64     `bun:",pk,autoincrement"`
	ChatID    string    `bun:",notnull,unique"`
	UserID    string    `bun:",notnull,default:''"` // owner user UUID
	Content   string    `bun:",notnull"`
	CreatedAt time.Time `bun:",nullzero,notnull,default:current_timestamp"`
	UpdatedAt time.Time `bun:",nullzero,notnull,default:current_timestamp"`
}
