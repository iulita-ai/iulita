package domain

import "time"

// Reminder represents a one-time reminder.
type Reminder struct {
	ID        int64     `bun:",pk,autoincrement"`
	ChatID    string    `bun:",notnull"`
	UserID    string    `bun:",notnull"`
	Title     string    `bun:",notnull"`
	DueAt     time.Time `bun:",notnull"`
	Timezone  string    `bun:",notnull,default:'UTC'"`
	Status    string    `bun:",notnull,default:'pending'"` // pending, fired, cancelled
	CreatedAt time.Time `bun:",nullzero,notnull,default:current_timestamp"`
}
