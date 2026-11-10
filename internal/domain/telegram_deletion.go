package domain

import (
	"errors"
	"time"
)

// ErrConversationDeleted rejects work for a deleting or deleted conversation.
var ErrConversationDeleted = errors.New("conversation deleted")

// TelegramTopicDeletionScope pins the exact private route and approved deletion or binding intent.
type TelegramTopicDeletionScope struct {
	BotID          int64
	OperationID    string // required only for approved command initiation
	Digest         string
	ConversationID string
	InstanceID     string
	OwnerID        string
	ChatID         int64
	ThreadID       int
}

// TelegramTopicDeletion retains scoped cleanup counts and durable deletion/notice state.
type TelegramTopicDeletion struct {
	BotID          int64  `bun:",notnull,default:0"`
	ConversationID string `bun:",pk"`
	InstanceID     string
	OwnerID        string
	ChatID         int64
	ThreadID       int
	Source         string
	Messages       int
	Reminders      int
	Jobs           int
	Tasks          int
	Status         string
	NoticeClaimed  bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
