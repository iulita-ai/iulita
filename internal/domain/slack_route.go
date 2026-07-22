package domain

import "time"

// SlackChatRoute persists the real Slack coordinates behind a composite chatID
// ("slack:D..." for DMs, "slack:C...:U..." for channels) so that proactive
// delivery (reminders, agent jobs, heartbeat, insights) survives eviction of
// the in-memory chatMeta cache and process restarts.
//
// The row is a routing hint only: it never confers write permission — the
// channel's own permission gate is re-checked on every send.
type SlackChatRoute struct {
	InstanceID  string    `bun:",pk" json:"instance_id"`                   // owning channel instance (part of PK)
	ChatID      string    `bun:",pk" json:"chat_id"`                       // "slack:D..." | "slack:C...:U..."
	ChannelID   string    `bun:",notnull" json:"channel_id"`               // real Slack channel (C.../D...)
	SlackUserID string    `bun:",notnull,default:''" json:"slack_user_id"` // Slack user (for DM re-open)
	ThreadTS    string    `bun:",notnull,default:''" json:"thread_ts"`     // parent thread ts (empty for DMs)
	Locale      string    `bun:",notnull,default:''" json:"locale"`        // captured user locale
	UpdatedAt   time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"updated_at"`
}
