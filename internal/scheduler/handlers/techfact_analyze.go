package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
)

const TaskTypeTechFactAnalyze = "techfact.analyze"

type techFactPayload struct {
	ChatID string `json:"chat_id"`
}

type techFactEntry struct {
	Category   string  `json:"category"`
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

// TechFactAnalyzeHandler performs deep LLM analysis of user messages.
type TechFactAnalyzeHandler struct {
	store    storage.Repository
	provider llm.Provider
	sender   channel.MessageSender // optional, for delivery notifications
	logger   *zap.Logger
}

func NewTechFactAnalyzeHandler(store storage.Repository, provider llm.Provider, logger *zap.Logger) *TechFactAnalyzeHandler {
	return &TechFactAnalyzeHandler{store: store, provider: provider, logger: logger}
}

// SetSender configures a MessageSender for delivery notifications.
func (h *TechFactAnalyzeHandler) SetSender(s channel.MessageSender) {
	h.sender = s
}

func (h *TechFactAnalyzeHandler) Type() string { return TaskTypeTechFactAnalyze }

func (h *TechFactAnalyzeHandler) Handle(ctx context.Context, payload string) (string, error) {
	var p techFactPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("invalid payload: %w", err)
	}

	messages, err := h.store.GetHistory(ctx, p.ChatID, 100)
	if err != nil {
		return "", fmt.Errorf("loading messages: %w", err)
	}

	if len(messages) < 10 {
		return `{"upserted":0,"reason":"not enough messages"}`, nil
	}

	var mb strings.Builder
	count := 0
	for _, m := range messages {
		if m.Role == "user" {
			fmt.Fprintf(&mb, "- %s\n", m.Content)
			count++
		}
	}
	if count < 5 {
		return `{"upserted":0,"reason":"not enough user messages"}`, nil
	}

	prompt := fmt.Sprintf(`Analyze these user messages and extract behavioral metadata.
Return a JSON array of objects with fields: category, key, value, confidence (0.0-1.0).

Categories to extract:
- "topic": key="topic:<name>", value="high"/"medium"/"low" (frequency). Extract up to 10 topics.
- "style": key="communication_style", value=description (e.g. "direct and concise", "detailed and technical")
- "pattern": key="<pattern_name>", value=description (e.g. key="asks_followups", value="frequently asks clarifying questions")

Respond ONLY with the JSON array, no other text.

Messages:
%s`, mb.String())

	resp, err := h.provider.Complete(ctx, llm.Request{
		SystemPrompt: "You are a behavioral analysis system. Extract structured metadata from user messages. Respond only with valid JSON.",
		Message:      prompt,
		RouteHint:    llm.RouteHintCheap,
	})
	if err != nil {
		return "", fmt.Errorf("LLM analysis: %w", err)
	}

	content := strings.TrimSpace(resp.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var entries []techFactEntry
	if err := json.Unmarshal([]byte(content), &entries); err != nil {
		h.logger.Warn("tech facts: failed to parse LLM response",
			zap.Error(err), zap.String("response", resp.Content))
		return `{"upserted":0,"reason":"parse error"}`, nil
	}

	upserted := 0
	for _, e := range entries {
		if e.Category == "" || e.Key == "" || e.Value == "" {
			continue
		}
		if err := h.store.UpsertTechFact(ctx, &domain.TechFact{
			ChatID:     p.ChatID,
			Category:   e.Category,
			Key:        e.Key,
			Value:      e.Value,
			Confidence: e.Confidence,
		}); err != nil {
			continue
		}
		upserted++
	}

	if upserted > 0 && h.sender != nil {
		summary := fmt.Sprintf("Updated %d profile metadata entries.", upserted)
		if err := h.sender.SendMessage(ctx, p.ChatID, summary); err != nil {
			h.logger.Error("failed to deliver techfact summary", zap.Error(err))
		}
	}

	result, _ := json.Marshal(map[string]int{"upserted": upserted})
	return string(result), nil
}
