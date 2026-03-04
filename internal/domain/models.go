package domain

import "time"

// Role represents who sent a message in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
	ID        int64     `bun:",pk,autoincrement"`
	ChatID    string    `bun:",notnull"`
	UserID    string    `bun:",notnull,default:''"` // iulita user UUID (owner)
	Role      Role      `bun:",notnull"`
	Content   string    `bun:",notnull"`
	CreatedAt time.Time `bun:",nullzero,notnull,default:current_timestamp"`
}
