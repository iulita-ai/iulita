// Package interact provides the PromptAsker interface for interactive skills
// that need to ask the user questions or present choices during execution.
package interact

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors for prompt operations.
var (
	ErrNoPrompter    = errors.New("no prompt asker in context")
	ErrPromptTimeout = errors.New("prompt timed out")
	ErrCancelled     = errors.New("prompt cancelled")
)

// DefaultTimeout is the default time to wait for a user response.
// Kept short to avoid blocking the agentic loop; skill falls back gracefully on timeout.
// DefaultTimeout is how long non-Slack prompters wait for a user's response to an
// interactive prompt (button click or free-text). It must be long enough for a
// human to react — 30s was too short, especially for slack_post draft approvals
// delivered to a Telegram/console/web conversation. (Slack's own prompter uses a
// longer, separate window.)
const DefaultTimeout = 5 * time.Minute

// MaxOptions is the hard limit on selectable choices (excluding free-text).
const MaxOptions = 5

// Option represents one selectable choice presented to the user.
type Option struct {
	ID    string // machine-readable key returned when selected
	Label string // human-readable text shown to user
}

// PromptAsker presents interactive choices or free-text requests to the user.
// Implementations are channel-specific and injected via context.
type PromptAsker interface {
	// Ask presents the question and up to MaxOptions options.
	// An implicit "Enter manually" option is always appended by the implementation.
	// If options is empty, it's a pure free-text request.
	// Returns the selected option ID, or the raw typed text if free-text is chosen.
	Ask(ctx context.Context, question string, options []Option) (string, error)
}

// PromptAskerFactory creates a PromptAsker for a given chatID.
// Each channel implements this to provide channel-specific prompt handling.
type PromptAskerFactory interface {
	// PrompterFor returns a PromptAsker for the given chatID, or nil if the
	// chatID does not belong to this channel.
	PrompterFor(chatID string) PromptAsker
}
