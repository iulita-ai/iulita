package console

import (
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// streamChunkMsg delivers an incremental streaming update (full text so far).
type streamChunkMsg string

// streamDoneMsg delivers the final response text.
type streamDoneMsg string

// statusMsg delivers a processing status event (thinking, skill start/done).
type statusMsg channel.StatusEvent

// responseMsg delivers a non-streaming response.
type responseMsg struct {
	text string
	err  error
}

// proactiveMsg delivers a push message (reminder, notification).
type proactiveMsg string

// sendMsg is dispatched when the user presses Enter to send a message.
type sendMsg string

// compactResultMsg delivers the result of an async /compact operation.
type compactResultMsg struct {
	removed int
	err     error
}

// promptMsg delivers an interactive prompt to the TUI for user selection.
type promptMsg struct {
	question string
	options  []interact.Option
}
