package domain

import "time"

// UserRole represents the authorization level of a user.
type UserRole string

const (
	RoleAdmin   UserRole = "admin"
	RoleRegular UserRole = "user"
)

// User represents a registered iulita user.
// A user can have multiple channels (Telegram accounts, future Discord, etc.).
// All knowledge (facts, insights, directives, techfacts) belongs to the user,
// while message history belongs to a specific channel/chat.
type User struct {
	ID             string    `bun:",pk" json:"id"`                                                 // UUIDv7
	Username       string    `bun:",notnull,unique" json:"username"`                               // login name
	PasswordHash   string    `bun:",notnull" json:"-"`                                             // bcrypt hash
	Role           UserRole  `bun:",notnull,default:'user'" json:"role"`                           // admin or user
	DisplayName    string    `bun:",notnull,default:''" json:"display_name"`                       // human-readable name
	Timezone       string    `bun:",notnull,default:'UTC'" json:"timezone"`                        // IANA timezone
	SystemPrompt   string    `bun:",notnull,default:''" json:"system_prompt"`                      // per-user system prompt override
	MustChangePass bool      `bun:",notnull,default:false" json:"must_change_pass"`                // force password change on next login
	CreatedAt      time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"created_at"` // registration time
	UpdatedAt      time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"updated_at"` // last profile update
}

// UserChannel binds a user to a specific channel identity (e.g., a Telegram account).
// Multiple channels can belong to the same user. The user's knowledge is shared across
// all their channels.
type UserChannel struct {
	ID                int64     `bun:",pk,autoincrement" json:"id"`
	UserID            string    `bun:",notnull" json:"user_id"`                        // FK → users.id
	ChannelInstanceID string    `bun:",notnull,default:''" json:"channel_instance_id"` // FK → channel_instances.id
	ChannelType       string    `bun:",notnull" json:"channel_type"`                   // "telegram" (future: "discord", "web")
	ChannelID         string    `bun:",notnull" json:"channel_id"`                     // platform chat ID (e.g., Telegram chat_id)
	ChannelUserID     string    `bun:",notnull" json:"channel_user_id"`                // platform user ID (e.g., Telegram user_id)
	ChannelUsername   string    `bun:",notnull,default:''" json:"channel_username"`    // platform username
	Enabled           bool      `bun:",notnull,default:true" json:"enabled"`           // can be disabled without deletion
	Locale            string    `bun:",notnull,default:'en'" json:"locale"`            // BCP-47 language tag (e.g. "en", "ru", "zh")
	CreatedAt         time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"created_at"`
}
