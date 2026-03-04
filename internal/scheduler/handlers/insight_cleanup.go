package handlers

import (
	"context"

	"github.com/iulita-ai/iulita/internal/storage"
)

const TaskTypeInsightCleanup = "insight.cleanup"

// InsightCleanupHandler deletes expired insights.
type InsightCleanupHandler struct {
	store storage.Repository
}

func NewInsightCleanupHandler(store storage.Repository) *InsightCleanupHandler {
	return &InsightCleanupHandler{store: store}
}

func (h *InsightCleanupHandler) Type() string { return TaskTypeInsightCleanup }

func (h *InsightCleanupHandler) Handle(ctx context.Context, _ string) (string, error) {
	if err := h.store.DeleteExpiredInsights(ctx); err != nil {
		return "", err
	}
	return `{"status":"ok"}`, nil
}
