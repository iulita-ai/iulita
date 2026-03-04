package console

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// pendingConsolePrompt tracks an active prompt awaiting user input in the TUI.
type pendingConsolePrompt struct {
	question  string
	options   []interact.Option
	replyCh   chan string
	otherMode bool // true when "Enter manually" was selected, next input = free text
}

// consolePrompter implements interact.PromptAsker for the Console TUI.
type consolePrompter struct {
	channel *Channel
}

func (cp *consolePrompter) Ask(ctx context.Context, question string, options []interact.Option) (string, error) {
	p := cp.channel.getProgram()
	if p == nil {
		return "", fmt.Errorf("console program not running")
	}

	replyCh := make(chan string, 1)
	pending := &pendingConsolePrompt{
		question: question,
		options:  options,
		replyCh:  replyCh,
	}

	cp.channel.promptMu.Lock()
	cp.channel.pendingPrompt = pending
	cp.channel.promptMu.Unlock()

	// Send prompt message to the TUI model.
	p.Send(promptMsg{
		question: question,
		options:  options,
	})

	defer func() {
		cp.channel.promptMu.Lock()
		cp.channel.pendingPrompt = nil
		cp.channel.promptMu.Unlock()
	}()

	timeout := interact.DefaultTimeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case answer := <-replyCh:
		return answer, nil
	case <-timer.C:
		return "", interact.ErrPromptTimeout
	case <-ctx.Done():
		return "", interact.ErrCancelled
	}
}

// PrompterFor creates a PromptAsker for the console channel.
// Returns nil if the chatID is not "console".
func (c *Channel) PrompterFor(chatID string) interact.PromptAsker {
	if chatID != "console" && chatID != c.chatID {
		return nil
	}
	return &consolePrompter{channel: c}
}

// ResolvePromptInput routes user input to a pending prompt if one exists.
// Returns the option ID or typed text, and true if input was consumed.
func (c *Channel) ResolvePromptInput(input string) (string, bool) {
	c.promptMu.Lock()
	pending := c.pendingPrompt
	c.promptMu.Unlock()

	if pending == nil {
		return "", false
	}

	// If otherMode is active, any input is free text.
	if pending.otherMode {
		select {
		case pending.replyCh <- input:
		default:
		}
		return input, true
	}

	// Try to match a numbered option (1-based).
	if len(input) == 1 && input[0] >= '1' && input[0] <= '9' {
		idx := int(input[0] - '1')
		totalOptions := len(pending.options) + 1 // +1 for "Enter manually"
		if idx < len(pending.options) {
			select {
			case pending.replyCh <- pending.options[idx].ID:
			default:
			}
			return pending.options[idx].ID, true
		}
		// Last number = "Enter manually" — switch to free text mode.
		if idx == totalOptions-1 {
			c.promptMu.Lock()
			pending.otherMode = true
			c.promptMu.Unlock()
			// Return consumed=true but don't send to replyCh yet.
			// The prompt stays active; next input will be free text.
			return "__other__", true
		}
	}

	// Unrecognized input treated as free text.
	select {
	case pending.replyCh <- input:
	default:
	}
	return input, true
}

var _ interact.PromptAskerFactory = (*Channel)(nil)
