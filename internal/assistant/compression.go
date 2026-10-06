package assistant

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/llm"
)

const (
	compressionThreshold = 0.8 // 80% of context window
	defaultSummaryPrefix = "[Summary of earlier conversation]\n"
	defaultContextWindow = 200000
)

// compressIfNeeded checks if the context is getting too large and summarizes
// older messages to make room. Returns the (possibly compressed) history.
func (a *Assistant) compressIfNeeded(ctx context.Context, chatID string, history []domain.ChatMessage, lastInputTokens int64, routing ...llm.Request) ([]domain.ChatMessage, error) {
	window := a.compressionContextWindow(routing...)
	if window <= 0 || lastInputTokens <= 0 {
		return history, nil
	}

	threshold := int64(float64(window) * compressionThreshold)
	if lastInputTokens < threshold {
		return history, nil
	}

	if len(history) < 4 {
		return history, nil
	}

	a.logger.Info("context compression triggered",
		zap.Int64("input_tokens", lastInputTokens),
		zap.Int64("threshold", threshold),
		zap.Int("history_len", len(history)),
	)

	// Split: first half → summarize, second half → keep.
	splitIdx := len(history) / 2
	oldMessages := history[:splitIdx]
	keepMessages := history[splitIdx:]

	// Build conversation text for summarization.
	var convText strings.Builder
	for _, msg := range oldMessages {
		fmt.Fprintf(&convText, "%s: %s\n", msg.Role, msg.Content)
	}

	summaryReq := llm.Request{
		SystemPrompt: i18n.T(ctx, "AssistantCompressionPrompt"),
		Message:      convText.String(),
		RouteHint:    llm.RouteHintCheap,
		ChatID:       chatID, UserID: oldMessages[0].UserID, Operation: "compression",
	}

	if len(routing) > 0 {
		summaryReq.RoutingSnapshot = routing[0].RoutingSnapshot
	}
	summaryResp, err := a.provider.Complete(ctx, summaryReq)
	if err != nil {
		a.logger.Error("compression summarization failed", zap.Error(err))
		return history, fmt.Errorf("generate conversation summary: %w", err) // Preserve history on failure.
	}

	prefix := i18n.T(ctx, "AssistantSummaryPrefix")
	if prefix == "AssistantSummaryPrefix" {
		prefix = defaultSummaryPrefix
	}
	summary := prefix + summaryResp.Content

	// Summary replacement must be atomic: a failed insert preserves old history.
	replacer, ok := a.store.(interface {
		ReplaceMessagesWithSummary(context.Context, string, int64, *domain.ChatMessage) error
	})
	if !ok || strings.TrimSpace(summaryResp.Content) == "" {
		return history, nil
	}
	summaryMsg := &domain.ChatMessage{ChatID: chatID, UserID: oldMessages[0].UserID, Role: domain.RoleAssistant, Content: summary, CreatedAt: oldMessages[0].CreatedAt}
	if err := replacer.ReplaceMessagesWithSummary(ctx, chatID, oldMessages[len(oldMessages)-1].ID, summaryMsg); err != nil {
		a.logger.Error("failed to replace compressed history", zap.Error(err))
		return history, fmt.Errorf("replace conversation history with summary: %w", err)
	}

	// Return compressed history: summary + kept messages.
	compressed := make([]domain.ChatMessage, 0, 1+len(keepMessages))
	compressed = append(compressed, domain.ChatMessage{
		Role:    domain.RoleAssistant,
		Content: summary,
	})
	compressed = append(compressed, keepMessages...)

	a.logger.Info("context compressed",
		zap.Int("old_msgs", splitIdx),
		zap.Int("kept_msgs", len(keepMessages)),
	)

	return compressed, nil
}

// forceCompressRequest compresses history and updates the LLM request in-place.
// Used by the overflow recovery path in the agentic loop.
func (a *Assistant) forceCompressRequest(ctx context.Context, chatID string, req *llm.Request, history []domain.ChatMessage) []domain.ChatMessage {
	compressed, err := a.compressIfNeeded(ctx, chatID, history, int64(a.compressionContextWindow(*req))*2, *req)
	if err != nil {
		a.logger.Error("forced compression failed", zap.Error(err))
		return history
	}
	if len(compressed) > 1 {
		req.History = compressed[:len(compressed)-1]
	} else {
		req.History = nil
	}
	return compressed
}

// CompressNow forces context compression for the given chat, regardless of token threshold.
// Returns the number of messages that were compressed, or an error.
func (a *Assistant) CompressNow(ctx context.Context, chatID string) (int, error) {
	history, err := a.store.GetHistory(ctx, chatID, 0)
	if err != nil {
		return 0, fmt.Errorf("load history: %w", err)
	}
	if len(history) < 4 {
		return 0, fmt.Errorf("not enough messages to compress (%d, need at least 4)", len(history))
	}

	// Force compression by passing a value that always exceeds the threshold.
	splitIdx := len(history) / 2
	compressed, err := a.compressIfNeeded(ctx, chatID, history, int64(a.compressionContextWindow())*2)
	if err != nil {
		return 0, err
	}

	// If compression didn't happen (same slice returned), report 0.
	if len(compressed) == len(history) {
		return 0, nil
	}
	return splitIdx, nil
}

func (a *Assistant) compressionContextWindow(request ...llm.Request) int {
	req := llm.Request{}
	if len(request) > 0 {
		req = request[0]
	}
	snapshot := req.RoutingSnapshot
	if snapshot == nil {
		if p, ok := a.provider.(llm.ProfileInvoker); ok {
			snapshot = p.AcquireProfileSnapshot()
		}
	}
	if window := snapshot.ContextWindow(req); window > 0 {
		return window
	}
	return a.contextWindow
}
