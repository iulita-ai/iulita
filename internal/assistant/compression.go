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
func (a *Assistant) compressIfNeeded(ctx context.Context, chatID string, history []domain.ChatMessage, lastInputTokens int64) ([]domain.ChatMessage, error) {
	if a.contextWindow <= 0 || lastInputTokens <= 0 {
		return history, nil
	}

	threshold := int64(float64(a.contextWindow) * compressionThreshold)
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
	}

	summaryResp, err := a.provider.Complete(ctx, summaryReq)
	if err != nil {
		a.logger.Error("compression summarization failed", zap.Error(err))
		return history, nil // fail gracefully
	}

	prefix := i18n.T(ctx, "AssistantSummaryPrefix")
	if prefix == "AssistantSummaryPrefix" {
		prefix = defaultSummaryPrefix
	}
	summary := prefix + summaryResp.Content

	// Delete old messages from DB and insert summary.
	if splitIdx > 0 {
		lastOldID := oldMessages[splitIdx-1].ID
		if err := a.store.DeleteMessagesBefore(ctx, chatID, lastOldID+1); err != nil {
			a.logger.Error("failed to delete old messages", zap.Error(err))
			return history, nil
		}

		// Insert summary as a system-like user message at the beginning.
		summaryMsg := &domain.ChatMessage{
			ChatID:    chatID,
			Role:      domain.RoleAssistant,
			Content:   summary,
			CreatedAt: oldMessages[0].CreatedAt,
		}
		if err := a.store.SaveMessage(ctx, summaryMsg); err != nil {
			a.logger.Error("failed to save summary message", zap.Error(err))
		}
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
	compressed, err := a.compressIfNeeded(ctx, chatID, history, int64(a.contextWindow)*2)
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
	compressed, err := a.compressIfNeeded(ctx, chatID, history, int64(a.contextWindow)*2)
	if err != nil {
		return 0, err
	}

	// If compression didn't happen (same slice returned), report 0.
	if len(compressed) == len(history) {
		return 0, nil
	}
	return splitIdx, nil
}
