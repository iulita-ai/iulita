package webchat

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

var promptCounter atomic.Int64

// wsPromptOption is a selectable option sent to the browser.
type wsPromptOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// webchatPrompter implements interact.PromptAsker for WebChat.
type webchatPrompter struct {
	channel *Channel
	chatID  string
}

func (wp *webchatPrompter) Ask(ctx context.Context, question string, options []interact.Option) (string, error) {
	wp.channel.mu.RLock()
	conn, ok := wp.channel.clients[wp.chatID]
	wp.channel.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("webchat client not connected for %s", wp.chatID)
	}

	// Generate unique prompt ID using atomic counter.
	promptID := fmt.Sprintf("prompt_%d_%d", time.Now().UnixMilli(), promptCounter.Add(1))
	replyCh := make(chan string, 1)

	// Register pending prompt.
	wp.channel.pendingPrompts.Store(wp.chatID+":"+promptID, replyCh)
	defer wp.channel.pendingPrompts.Delete(wp.chatID + ":" + promptID)

	// Build options for the client.
	wsOpts := make([]wsPromptOption, 0, len(options)+1)
	for i, opt := range options {
		if i >= interact.MaxOptions {
			break
		}
		wsOpts = append(wsOpts, wsPromptOption{ID: opt.ID, Label: opt.Label})
	}
	wsOpts = append(wsOpts, wsPromptOption{ID: "__other__", Label: "Enter manually"})

	// Send prompt to client.
	wp.channel.sendToConn(conn, wsOutgoingMessage{
		Type:      "prompt",
		Text:      question,
		PromptID:  promptID,
		Options:   wsOpts,
		Timestamp: time.Now().Format(time.RFC3339),
	})

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

// PrompterFor creates a PromptAsker for the given chatID.
// Returns nil if the chatID doesn't look like a webchat ID.
func (c *Channel) PrompterFor(chatID string) interact.PromptAsker {
	if !strings.HasPrefix(chatID, "web:") {
		return nil
	}
	return &webchatPrompter{channel: c, chatID: chatID}
}

var _ interact.PromptAskerFactory = (*Channel)(nil)
