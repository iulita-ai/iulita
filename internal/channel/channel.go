package channel

import "context"

// ImageAttachment holds raw image data received from a channel.
type ImageAttachment struct {
	Data      []byte
	MediaType string // e.g. "image/jpeg", "image/png"
}

// DocumentAttachment holds a file received from a channel (PDF, text, etc.).
type DocumentAttachment struct {
	Data     []byte
	MimeType string // e.g. "application/pdf", "text/plain"
	Filename string
}

// AudioAttachment holds voice/audio data received from a channel.
type AudioAttachment struct {
	Data     []byte
	Format   string // e.g. "ogg", "mp3", "wav"
	Duration int    // duration in seconds (if known)
}

// IncomingMessage represents a message received from an input channel.
type IncomingMessage struct {
	ChatID            string
	UserID            string // platform-specific user ID (e.g., Telegram user_id)
	ResolvedUserID    string // iulita user UUID (set after user resolution)
	ChannelInstanceID string // channel instance slug (e.g., "tg-config")
	UserName          string
	Text              string
	LanguageCode      string               // IETF language tag from the channel (e.g. "ru", "en")
	Locale            string               // channel-stored locale preference (BCP-47, e.g. "ru", "en")
	Images            []ImageAttachment    // nil for text-only messages
	Documents         []DocumentAttachment // nil for messages without files
	Audio             []AudioAttachment    // nil for messages without voice/audio
	MessageID         int                  // platform-specific message ID for reply threading
	Caps              ChannelCaps          // capabilities of the originating channel
}

// MessageHandler is a callback invoked for each incoming message.
// It returns the response text to send back to the channel.
type MessageHandler func(ctx context.Context, msg IncomingMessage) (string, error)

// InputChannel receives messages from an external source and forwards them to a handler.
type InputChannel interface {
	Start(ctx context.Context, handler MessageHandler) error
}

// MessageSender sends proactive messages to a channel.
type MessageSender interface {
	SendMessage(ctx context.Context, chatID string, text string) error
}

// StreamingSender supports incremental message editing for streaming responses.
type StreamingSender interface {
	MessageSender
	// StartStream sends an initial placeholder message and returns functions to edit and finalize it.
	// editFn updates the message content; doneFn sends the final version.
	StartStream(ctx context.Context, chatID string, replyTo int) (editFn func(text string), doneFn func(text string), err error)
}

// BookmarkStreamingSender extends StreamingSender to attach a "remember" button
// to streamed responses. Channels that support this implement it alongside StreamingSender.
type BookmarkStreamingSender interface {
	StreamingSender
	// StartStreamWithBookmark is like StartStream but attaches a bookmark button
	// to the final message. userID is the iulita user UUID for scoping the saved fact.
	StartStreamWithBookmark(ctx context.Context, chatID string, replyTo int, userID string) (editFn func(text string), doneFn func(text string), err error)
}

// StatusNotifier sends processing status updates to a specific chat.
// Used for real-time feedback (thinking, skill execution) in web chat.
type StatusNotifier interface {
	NotifyStatus(ctx context.Context, chatID string, event StatusEvent) error
}

// StatusEvent represents a processing status update.
type StatusEvent struct {
	Type       string            `json:"type"` // "processing", "skill_start", "skill_done", "stream_start", "error", "locale_changed"
	SkillName  string            `json:"skill_name,omitempty"`
	Success    bool              `json:"success,omitempty"`
	DurationMs int64             `json:"duration_ms,omitempty"`
	Error      string            `json:"error,omitempty"`
	Data       map[string]string `json:"data,omitempty"` // extra payload (e.g. locale for locale_changed)
}

// UserResolver maps a channel-specific identity to an iulita user.
// Returns nil if the user is not found. Implementations may auto-create
// users when registration is enabled.
type UserResolver interface {
	ResolveUser(ctx context.Context, channelType, channelUserID, channelUsername string, chatID string) (userID string, err error)
}
