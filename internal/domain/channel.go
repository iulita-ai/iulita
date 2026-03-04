package domain

import "time"

// ChannelInstance represents a communication bot/integration (e.g., a Telegram bot, Discord bot, web chat).
// One channel instance can serve many users. Separate from UserChannel which binds a user to a channel.
type ChannelInstance struct {
	ID        string    `bun:",pk" json:"id"`                       // slug: "tg-personal", "webchat"
	Type      string    `bun:",notnull" json:"type"`                // "telegram", "discord", "web"
	Name      string    `bun:",notnull" json:"name"`                // display name
	Config    string    `bun:",notnull,default:'{}'" json:"config"` // JSON (token, allowed_ids, etc.)
	Source    string    `bun:",notnull" json:"source"`              // "config" | "dashboard"
	Enabled   bool      `bun:",notnull,default:true" json:"enabled"`
	CreatedAt time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"updated_at"`
}

// ChannelInstance sources.
const (
	ChannelSourceConfig    = "config"
	ChannelSourceDashboard = "dashboard"
)

// Channel types.
const (
	ChannelTypeTelegram = "telegram"
	ChannelTypeDiscord  = "discord"
	ChannelTypeWeb      = "web"
	ChannelTypeConsole  = "console"
)
