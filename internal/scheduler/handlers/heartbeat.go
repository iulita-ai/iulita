package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
)

const TaskTypeHeartbeat = "heartbeat.check"

type heartbeatPayload struct {
	ChatID string `json:"chat_id"`
}

// HeartbeatHandler reviews recent memory and sends a proactive message if needed.
type HeartbeatHandler struct {
	store    storage.Repository
	provider llm.Provider
	sender   channel.MessageSender
	logger   *zap.Logger
}

func NewHeartbeatHandler(store storage.Repository, provider llm.Provider, sender channel.MessageSender, logger *zap.Logger) *HeartbeatHandler {
	return &HeartbeatHandler{store: store, provider: provider, sender: sender, logger: logger}
}

func (h *HeartbeatHandler) Type() string { return TaskTypeHeartbeat }

func (h *HeartbeatHandler) Handle(ctx context.Context, payload string) (string, error) {
	var p heartbeatPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("invalid payload: %w", err)
	}

	// Gather context for review.
	var contextParts []string

	if facts, err := h.store.GetRecentFacts(ctx, p.ChatID, 5); err == nil && len(facts) > 0 {
		var fb strings.Builder
		fb.WriteString("Recent facts:\n")
		for _, f := range facts {
			fmt.Fprintf(&fb, "- %s\n", f.Content)
		}
		contextParts = append(contextParts, fb.String())
	}

	if insights, err := h.store.GetRecentInsights(ctx, p.ChatID, 3); err == nil && len(insights) > 0 {
		var ib strings.Builder
		ib.WriteString("Recent insights:\n")
		for _, ins := range insights {
			fmt.Fprintf(&ib, "- %s\n", ins.Content)
		}
		contextParts = append(contextParts, ib.String())
	}

	if reminders, err := h.store.ListReminders(ctx, p.ChatID); err == nil && len(reminders) > 0 {
		var rb strings.Builder
		rb.WriteString("Upcoming reminders:\n")
		for _, r := range reminders {
			if r.Status == "pending" {
				fmt.Fprintf(&rb, "- %s (due: %s)\n", r.Title, r.DueAt.Format("2006-01-02 15:04"))
			}
		}
		if rb.Len() > 20 {
			contextParts = append(contextParts, rb.String())
		}
	}

	if len(contextParts) == 0 {
		return `{"action":"skip","reason":"no context"}`, nil
	}

	prompt := strings.Join(contextParts, "\n") +
		"\n\nReview the above and compose a brief friendly message if anything needs attention, follow-up, or is interesting to share. " +
		"If nothing warrants a message, respond with exactly: HEARTBEAT_OK"

	resp, err := h.provider.Complete(ctx, llm.Request{
		SystemPrompt: "You are a proactive personal assistant. Review the user's memory context and decide if a brief check-in message is warranted.",
		Message:      prompt,
	})
	if err != nil {
		return "", fmt.Errorf("heartbeat LLM call: %w", err)
	}

	content := strings.TrimSpace(resp.Content)
	if content == "" || content == "HEARTBEAT_OK" {
		return `{"action":"skip","reason":"nothing to report"}`, nil
	}

	if err := h.sender.SendMessage(ctx, p.ChatID, content); err != nil {
		return "", fmt.Errorf("sending heartbeat: %w", err)
	}

	return `{"action":"sent"}`, nil
}
