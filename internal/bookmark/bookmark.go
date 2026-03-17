// Package bookmark provides a service for saving assistant messages as bookmarked facts
// with optional background LLM refinement.
package bookmark

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// TaskTypeRefineBookmark is the scheduler task type for background LLM refinement.
const TaskTypeRefineBookmark = "bookmark.refine"

// RefinePayload is the JSON payload for the refine_bookmark task.
type RefinePayload struct {
	FactID  int64  `json:"fact_id"`
	Content string `json:"content"`
	ChatID  string `json:"chat_id"`
	UserID  string `json:"user_id,omitempty"`
}

// Service saves assistant messages as bookmarked facts.
type Service interface {
	// Save persists the full content as a bookmark fact and enqueues
	// a background LLM refinement task. Returns the saved fact ID.
	Save(ctx context.Context, chatID, userID, content string) (factID int64, err error)
}

type service struct {
	store  storage.Repository
	logger *zap.Logger
}

// New creates a new bookmark Service.
func New(store storage.Repository, logger *zap.Logger) Service {
	return &service{store: store, logger: logger}
}

func (s *service) Save(ctx context.Context, chatID, userID, content string) (int64, error) {
	if content == "" {
		return 0, fmt.Errorf("bookmark content is empty")
	}

	now := time.Now()
	fact := &domain.Fact{
		ChatID:         chatID,
		UserID:         userID,
		Content:        content,
		SourceType:     "bookmark",
		CreatedAt:      now,
		LastAccessedAt: now,
		AccessCount:    0,
	}

	if err := s.store.SaveFact(ctx, fact); err != nil {
		return 0, fmt.Errorf("saving bookmark fact: %w", err)
	}

	// Enqueue background refinement task.
	payload, err := json.Marshal(RefinePayload{
		FactID:  fact.ID,
		Content: content,
		ChatID:  chatID,
		UserID:  userID,
	})
	if err != nil {
		s.logger.Error("failed to marshal refine payload", zap.Error(err), zap.Int64("fact_id", fact.ID))
		return fact.ID, nil // fact is saved, refinement is best-effort
	}

	task := &domain.Task{
		Type:           TaskTypeRefineBookmark,
		Payload:        string(payload),
		Capabilities:   "llm,storage",
		MaxAttempts:    2,
		UniqueKey:      fmt.Sprintf("bookmark.refine:%d", fact.ID),
		DeleteAfterRun: true,
		ScheduledAt:    now,
	}

	if _, err := s.store.CreateTaskIfNotExists(ctx, task); err != nil {
		s.logger.Error("failed to enqueue refine task", zap.Error(err), zap.Int64("fact_id", fact.ID))
		// Non-fatal: the fact is already saved.
	}

	return fact.ID, nil
}
