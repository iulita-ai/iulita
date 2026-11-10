package domain

import "time"

// TelegramTopicState records the local lifecycle state of a conversation.
type TelegramTopicState struct {
	ConversationID string `bun:",pk"`
	Name           string
	Status         string // open, paused, deleted (closed is a legacy local state)
	UpdatedAt      time.Time
}

// TelegramTopicOperation pins one requested operation before calling Telegram.
// ThreadID records a successful create before local route materialization.
type TelegramTopicOperation struct {
	BotID      int64  `bun:",notnull,default:0"`
	ID         string `bun:",pk"`
	Digest     string
	OwnerID    string
	InstanceID string
	ChatID     int64
	ThreadID   int
	Action     string
	Name       string
	Status     string
	UpdatedAt  time.Time
}

// TelegramTopicSeed pins the separate non-idempotent stages of a root turn.
// Uncertain stages are never retried automatically after a crash/network error.
type TelegramTopicSeed struct {
	ID         string `bun:",pk"`
	OwnerID    string
	InstanceID string
	ChatID     int64
	MessageID  int
	ThreadID   int
	CopyID     int
	AlbumID    string
	Status     string
	UpdatedAt  time.Time
}

// TelegramTopicListItem is an owner-scoped route summary for listing known topics.
type TelegramTopicListItem struct {
	BotID          int64
	ConversationID string
	ThreadID       int
	Name           string
	Status         string
}
