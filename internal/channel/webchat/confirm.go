package webchat

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

var confirmCounter atomic.Int64

// webchatConfirmer implements interact.ConfirmationAsker over the
// authenticated websocket: the answer arrives on the same JWT-authenticated
// chat socket that requested the operation, so it is bound to that
// principal. Free-text or unknown answers deny (fail closed).
type webchatConfirmer struct {
	channel *Channel
	chatID  string
}

// Confirm pushes the prompt over the authenticated socket and waits.
func (wc *webchatConfirmer) Confirm(ctx context.Context, action interact.Confirmation, expect interact.Actor) (interact.Decision, error) {
	wc.channel.mu.RLock()
	conn, ok := wc.channel.clients[wc.chatID]
	wc.channel.mu.RUnlock()
	if !ok {
		return interact.Decision{}, fmt.Errorf("webchat client not connected for %s", wc.chatID)
	}

	promptID := fmt.Sprintf("confirm_%d_%d", time.Now().UnixMilli(), confirmCounter.Add(1))
	replyCh := make(chan string, 1)
	wc.channel.pendingPrompts.Store(wc.chatID+":"+promptID, replyCh)
	defer wc.channel.pendingPrompts.Delete(wc.chatID + ":" + promptID)

	question := fmt.Sprintf("🔐 %s\n\n%s\n\nApprove this exact operation (%s)?",
		action.Reason, action.Preview, action.OperationID)
	if action.MaxBytes > 0 {
		question += fmt.Sprintf("\nLimit: %d bytes.", action.MaxBytes)
	}

	wc.channel.sendToConn(conn, wsOutgoingMessage{
		Type:      "prompt",
		Text:      question,
		PromptID:  promptID,
		Options:   webchatConfirmOptions(action.Reason),
		Timestamp: time.Now().Format(time.RFC3339),
	})

	timer := time.NewTimer(time.Until(action.ExpiresAt))
	defer timer.Stop()

	select {
	case answer := <-replyCh:
		switch strings.TrimSpace(answer) {
		case "confirm_approve":
			return interact.Decision{Approved: true, Actor: expect, MaxBytes: action.MaxBytes}, nil
		case "confirm_always_origin":
			return interact.Decision{Approved: true, Actor: expect, MaxBytes: action.MaxBytes, Standing: interact.StandingOrigin}, nil
		case "confirm_always":
			return interact.Decision{Approved: true, Actor: expect, MaxBytes: action.MaxBytes, Standing: interact.StandingGlobal}, nil
		default:
			return interact.Decision{Approved: false, Actor: expect}, interact.ErrConfirmRejected
		}
	case <-timer.C:
		return interact.Decision{}, interact.ErrPromptTimeout
	case <-ctx.Done():
		return interact.Decision{}, interact.ErrCancelled
	}
}

// webchatConfirmOptions renders the consent choices; standing options only
// for mutations (downloads always re-ask).
func webchatConfirmOptions(reason string) []wsPromptOption {
	opts := []wsPromptOption{
		{ID: "confirm_approve", Label: "✅ Approve"},
		{ID: "confirm_deny", Label: "❌ Deny"},
	}
	if reason == interact.ConfirmMutation {
		opts = append(opts,
			wsPromptOption{ID: "confirm_always_origin", Label: "🔁 Always for this origin"},
			wsPromptOption{ID: "confirm_always", Label: "♾️ Always"},
		)
	}
	return opts
}

// ConfirmerFor returns a ConfirmationAsker for the authenticated web chat.
func (c *Channel) ConfirmerFor(chatID string) interact.ConfirmationAsker {
	if !strings.HasPrefix(chatID, "web:") {
		return nil
	}
	return &webchatConfirmer{channel: c, chatID: chatID}
}
