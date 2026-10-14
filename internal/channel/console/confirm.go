package console

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// consoleConfirmer implements interact.ConfirmationAsker for the local TUI.
// The console is a single-user local channel: the person at the terminal is
// the principal, and the pending-prompt mechanism serializes answers so a
// stale prompt can never decide a newer operation.
type consoleConfirmer struct {
	channel *Channel
}

// Confirm renders the confirmation in the local TUI and waits for a choice.
func (cc *consoleConfirmer) Confirm(ctx context.Context, action interact.Confirmation, expect interact.Actor) (interact.Decision, error) {
	p := cc.channel.getProgram()
	if p == nil {
		return interact.Decision{}, fmt.Errorf("console program not running")
	}

	replyCh := make(chan string, 1)
	pending := &pendingConsolePrompt{
		question: fmt.Sprintf("🔐 %s\n\n%s\n\nApprove this exact operation (%s)?",
			action.Reason, action.Preview, action.OperationID),
		options: []interact.Option{
			{ID: "confirm_approve", Label: "✅ Approve"},
			{ID: "confirm_deny", Label: "❌ Deny"},
		},
		replyCh: replyCh,
	}

	cc.channel.promptMu.Lock()
	// A still-pending prompt is superseded: it must not answer later.
	cc.channel.pendingPrompt = pending
	cc.channel.promptMu.Unlock()

	p.Send(promptMsg{question: pending.question, options: pending.options})

	defer func() {
		cc.channel.promptMu.Lock()
		if cc.channel.pendingPrompt == pending {
			cc.channel.pendingPrompt = nil
		}
		cc.channel.promptMu.Unlock()
	}()

	timer := time.NewTimer(time.Until(action.ExpiresAt))
	defer timer.Stop()

	select {
	case answer := <-replyCh:
		if answer == "confirm_approve" {
			return interact.Decision{Approved: true, Actor: expect, MaxBytes: action.MaxBytes}, nil
		}
		return interact.Decision{Approved: false, Actor: expect}, interact.ErrConfirmRejected
	case <-timer.C:
		return interact.Decision{}, interact.ErrPromptTimeout
	case <-ctx.Done():
		return interact.Decision{}, interact.ErrCancelled
	}
}

// ConfirmerFor returns a ConfirmationAsker for the console channel.
func (c *Channel) ConfirmerFor(chatID string) interact.ConfirmationAsker {
	if chatID != "console" && chatID != c.chatID {
		return nil
	}
	return &consoleConfirmer{channel: c}
}
