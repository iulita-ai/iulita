package domain

import "time"

// TelegramConversationRoute separates history identity from transport coordinates.
// A route is transport metadata, never an authorization grant.
type TelegramConversationRoute struct {
	BotID          int64     `bun:",notnull,default:0"` // immutable observed getMe identity; zero is unproven legacy
	ConversationID string    `bun:",pk"`
	InstanceID     string    `bun:",notnull"`
	ChatID         int64     `bun:",notnull"`
	ThreadID       int       `bun:",notnull,default:0"`
	OwnerID        string    `bun:",notnull"`
	Private        bool      `bun:",notnull"`
	UpdatedAt      time.Time `bun:",notnull"`
}
