package interact

import "context"

// NoopAsker is a PromptAsker that always returns ErrNoPrompter.
// Used as a fallback when no channel-specific prompter is available.
type NoopAsker struct{}

func (NoopAsker) Ask(_ context.Context, _ string, _ []Option) (string, error) {
	return "", ErrNoPrompter
}
