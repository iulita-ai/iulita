package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/bookmark"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
)

const refineSystemPrompt = `You are a note-taking assistant. Extract the key information from this AI assistant response.
Output 1-3 concise sentences capturing the essential facts or answers.
Do not include greetings, filler, meta-commentary, or markdown formatting.
Output ONLY the extracted facts, nothing else.`

// RefineBookmarkHandler refines bookmarked facts using LLM summarization.
type RefineBookmarkHandler struct {
	store    storage.Repository
	provider llm.Provider
	logger   *zap.Logger
}

// NewRefineBookmarkHandler creates a new handler.
func NewRefineBookmarkHandler(store storage.Repository, provider llm.Provider, logger *zap.Logger) *RefineBookmarkHandler {
	return &RefineBookmarkHandler{store: store, provider: provider, logger: logger}
}

func (h *RefineBookmarkHandler) Type() string { return bookmark.TaskTypeRefineBookmark }

func (h *RefineBookmarkHandler) Handle(ctx context.Context, payload string) (string, error) {
	var p bookmark.RefinePayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("invalid refine payload: %w", err)
	}

	if p.FactID == 0 || p.Content == "" {
		return "", fmt.Errorf("missing fact_id or content in payload")
	}

	// Call LLM to extract key facts.
	resp, err := h.provider.Complete(ctx, llm.Request{
		SystemPrompt: refineSystemPrompt,
		Message:      p.Content,
	})
	if err != nil {
		return "", fmt.Errorf("LLM refinement failed: %w", err)
	}

	refined := strings.TrimSpace(resp.Content)
	if refined == "" {
		h.logger.Debug("LLM returned empty refinement, keeping original",
			zap.Int64("fact_id", p.FactID))
		return `{"fact_id":` + fmt.Sprint(p.FactID) + `,"refined":false,"reason":"empty_response"}`, nil
	}

	// Skip if refinement is not meaningfully shorter (less than 90% of original).
	if len(refined) > len(p.Content)*9/10 {
		h.logger.Debug("refinement not shorter than original, keeping original",
			zap.Int64("fact_id", p.FactID),
			zap.Int("original_len", len(p.Content)),
			zap.Int("refined_len", len(refined)))
		return `{"fact_id":` + fmt.Sprint(p.FactID) + `,"refined":false,"reason":"no_improvement"}`, nil
	}

	// Update the fact content (triggers FTS update + re-embedding).
	if err := h.store.UpdateFactContent(ctx, p.FactID, refined); err != nil {
		// Fact may have been deleted — treat as non-fatal.
		if strings.Contains(err.Error(), "not found") {
			h.logger.Debug("fact already deleted, skipping refinement", zap.Int64("fact_id", p.FactID))
			return `{"fact_id":` + fmt.Sprint(p.FactID) + `,"refined":false,"reason":"deleted"}`, nil
		}
		return "", fmt.Errorf("updating fact content: %w", err)
	}

	result, _ := json.Marshal(map[string]any{
		"fact_id":      p.FactID,
		"refined":      true,
		"original_len": len(p.Content),
		"refined_len":  len(refined),
	})
	return string(result), nil
}
