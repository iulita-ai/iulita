package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/storage"
)

// SkillEnabledChecker checks if a skill is enabled in the registry.
type SkillEnabledChecker interface {
	IsEnabled(name string) bool
}

// TodoSyncHandler syncs tasks from external providers into the local todo_items table.
// It implements scheduler.TaskHandler.
type TodoSyncHandler struct {
	store        storage.Repository
	providers    []TodoProvider
	skillChecker SkillEnabledChecker
	logger       *zap.Logger
}

// NewTodoSyncHandler creates a new sync handler.
func NewTodoSyncHandler(store storage.Repository, providers []TodoProvider, skillChecker SkillEnabledChecker, logger *zap.Logger) *TodoSyncHandler {
	return &TodoSyncHandler{
		store:        store,
		providers:    providers,
		skillChecker: skillChecker,
		logger:       logger,
	}
}

// Type returns the scheduler task type.
func (h *TodoSyncHandler) Type() string { return "todo.sync" }

// Handle executes the sync. Payload is JSON with optional "user_id" field.
// If user_id is empty, syncs for all users that have data.
// Skips execution if the "tasks" skill is disabled.
func (h *TodoSyncHandler) Handle(ctx context.Context, payload string) (string, error) {
	if h.skillChecker != nil && !h.skillChecker.IsEnabled("tasks") {
		h.logger.Debug("todo sync skipped: tasks skill is disabled")
		return "skipped: tasks skill disabled", nil
	}

	var params struct {
		UserID string `json:"user_id"`
	}
	if payload != "" {
		_ = json.Unmarshal([]byte(payload), &params)
	}

	var userIDs []string
	if params.UserID != "" {
		userIDs = []string{params.UserID}
	} else {
		// Sync only for admin users (they configured the API tokens).
		// In a multi-user setup, per-user tokens would be needed.
		ids, err := h.store.GetUserIDs(ctx)
		if err != nil {
			return "", fmt.Errorf("getting user IDs: %w", err)
		}
		// Use only the first user (admin) to avoid copying tasks to all users.
		if len(ids) > 0 {
			userIDs = ids[:1]
		}
	}

	var results []string
	totalSynced := 0
	totalDeleted := 0

	for _, provider := range h.providers {
		if !provider.IsAvailable() {
			continue
		}

		provID := provider.ProviderID()
		for _, userID := range userIDs {
			if userID == "" {
				continue
			}

			items, err := provider.FetchAll(ctx, userID)
			if err != nil {
				h.logger.Error("todo sync: fetch failed",
					zap.String("provider", provID),
					zap.String("user_id", userID),
					zap.Error(err))
				results = append(results, fmt.Sprintf("%s/%s: fetch error: %v", provID, userID, err))
				continue
			}

			// Upsert all fetched items.
			externalIDs := make([]string, 0, len(items))
			synced := 0
			for i := range items {
				items[i].UserID = userID
				items[i].Provider = provID
				if err := h.store.UpsertTodoItemByExternal(ctx, &items[i]); err != nil {
					h.logger.Error("todo sync: upsert failed",
						zap.String("provider", provID),
						zap.String("external_id", items[i].ExternalID),
						zap.Error(err))
					continue
				}
				externalIDs = append(externalIDs, items[i].ExternalID)
				synced++
			}

			// Only delete stale items if ALL upserts succeeded (no partial failures).
			// If some upserts failed, we'd incorrectly delete items that exist externally.
			deleted := 0
			if synced == len(items) && len(items) > 0 {
				var err error
				deleted, err = h.store.DeleteSyncedTodoItemsNotIn(ctx, userID, provID, externalIDs)
				if err != nil {
					h.logger.Error("todo sync: cleanup failed",
						zap.String("provider", provID),
						zap.String("user_id", userID),
						zap.Error(err))
				}
			}

			totalSynced += synced
			totalDeleted += deleted

			h.logger.Info("todo sync completed",
				zap.String("provider", provID),
				zap.String("user_id", userID),
				zap.Int("synced", synced),
				zap.Int("deleted", deleted))
		}
	}

	result := fmt.Sprintf("synced %d items, deleted %d stale items", totalSynced, totalDeleted)
	if len(results) > 0 {
		result += "; warnings: " + strings.Join(results, "; ")
	}
	return result, nil
}
