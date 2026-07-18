package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
)

// TaskTypeImportReindex embeds archived messages that lack vectors — used when
// embeddings were enabled after an import ran FTS-only.
const TaskTypeImportReindex = "import.reindex"

type reindexPayload struct {
	UserID string `json:"user_id"`
	JobID  string `json:"job_id"`
}

// ImportReindexHandler builds embeddings for any archived messages missing them.
type ImportReindexHandler struct {
	store  storage.Repository
	embed  llm.EmbeddingProvider
	bus    *eventbus.Bus
	logger *zap.Logger
}

// NewImportReindexHandler constructs the reindex handler.
func NewImportReindexHandler(store storage.Repository, embed llm.EmbeddingProvider, bus *eventbus.Bus, logger *zap.Logger) *ImportReindexHandler {
	return &ImportReindexHandler{store: store, embed: embed, bus: bus, logger: logger}
}

// Type returns the task type handled by this handler.
func (h *ImportReindexHandler) Type() string { return TaskTypeImportReindex }

// Handle embeds all pending archive messages, publishing import.* progress so the
// dashboard reuses the same live-progress UI as an import.
func (h *ImportReindexHandler) Handle(ctx context.Context, payload string) (string, error) {
	var p reindexPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("invalid reindex payload: %w", err)
	}
	if h.embed == nil {
		return "", errors.New("embeddings are not configured")
	}

	started := time.Now()
	var reclaimed atomic.Bool
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	startImportHeartbeat(ctx, hctx, cancel, &reclaimed, h.store, h.logger)

	h.writeRun(ctx, &p, "running", 0, started, "", false)
	h.publish(ctx, eventbus.ImportStarted, eventbus.ImportProgressPayload{JobID: p.JobID, UserID: p.UserID, Phase: "embedding"})

	chunks, err := embedPendingMessages(hctx, h.store, h.embed, h.logger, func(total int) {
		h.writeRun(ctx, &p, "running", total, time.Time{}, "", false)
		h.publish(ctx, eventbus.ImportProgress, eventbus.ImportProgressPayload{
			JobID: p.JobID, UserID: p.UserID, Phase: "embedding", Done: total,
		})
	})
	if err != nil {
		if reclaimed.Load() {
			// The reclaiming worker owns the run row; only balance the in-flight gauge.
			h.publish(ctx, eventbus.ImportAborted, eventbus.ImportProgressPayload{JobID: p.JobID, UserID: p.UserID})
			return "", err
		}
		h.writeRun(ctx, &p, "failed", chunks, time.Time{}, err.Error(), true)
		if _, pErr := h.store.PruneImportRuns(ctx, importRunsRetention); pErr != nil {
			h.logger.Warn("failed to prune import runs", zap.Error(pErr))
		}
		h.publish(ctx, eventbus.ImportFailed, eventbus.ImportFailedPayload{JobID: p.JobID, UserID: p.UserID, Error: err.Error()})
		return "", err
	}

	h.writeRun(ctx, &p, "done", chunks, started, "", true)
	if _, pErr := h.store.PruneImportRuns(ctx, importRunsRetention); pErr != nil {
		h.logger.Warn("failed to prune import runs", zap.Error(pErr))
	}
	h.publish(ctx, eventbus.ImportDone, eventbus.ImportDonePayload{
		JobID: p.JobID, UserID: p.UserID, ChunksEmbedded: chunks, DurationSeconds: time.Since(started).Seconds(),
	})
	h.logger.Info("archive reindex complete", zap.String("job_id", p.JobID), zap.Int("chunks", chunks), zap.Duration("took", time.Since(started)))
	return fmt.Sprintf(`{"chunks_embedded":%d}`, chunks), nil
}

func (h *ImportReindexHandler) writeRun(ctx context.Context, p *reindexPayload, status string, chunks int, started time.Time, errMsg string, finished bool) {
	run := &domain.ImportRun{
		JobID: p.JobID, UserID: p.UserID, Status: status,
		ChunksEmbedded: chunks, LastPhase: "embedding", LastDone: chunks, Error: errMsg,
	}
	if !started.IsZero() {
		run.StartedAt = started
	}
	if finished {
		run.FinishedAt = time.Now()
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.store.UpsertImportRun(wctx, run); err != nil {
		h.logger.Warn("failed to persist reindex run", zap.String("job_id", p.JobID), zap.Error(err))
	}
}

func (h *ImportReindexHandler) publish(ctx context.Context, evtType string, payload any) {
	if h.bus == nil {
		return
	}
	h.bus.Publish(ctx, eventbus.Event{Type: evtType, Payload: payload})
}

// SweepStagedImports removes staged export files that no longer back an active
// (pending/claimed/running) import task, plus any leftover upload temp files. It runs
// at startup, before the import worker claims anything, so it never races a live task.
func SweepStagedImports(ctx context.Context, store storage.Repository, dir string, logger *zap.Logger) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading imports dir: %w", err)
	}

	// Build the set of zip paths still referenced by an active import task. A high
	// Limit is required: ListTasks defaults to 100 rows, and if an active task fell
	// outside the newest 100 (many terminal tasks accumulated) its zip would be wrongly
	// treated as an orphan and deleted out from under a live import.
	keep := map[string]bool{}
	tasks, err := store.ListTasks(ctx, storage.TaskFilter{Type: TaskTypeImportClaudeExport, Limit: 1_000_000})
	if err != nil {
		return 0, fmt.Errorf("listing import tasks: %w", err)
	}
	for i := range tasks {
		t := &tasks[i]
		if t.Status != domain.TaskStatusPending && t.Status != domain.TaskStatusClaimed && t.Status != domain.TaskStatusRunning {
			continue
		}
		var pl importPayload
		if json.Unmarshal([]byte(t.Payload), &pl) == nil && pl.ZipPath != "" {
			keep[pl.ZipPath] = true
		}
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// Always reap leftover temp files; reap <sha>.zip only if no active task keeps it.
		if strings.HasSuffix(e.Name(), ".tmp") || !keep[path] {
			if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
				logger.Warn("sweep: failed to remove staged file", zap.String("path", path), zap.Error(rmErr))
				continue
			}
			removed++
		}
	}
	if removed > 0 {
		logger.Info("swept orphaned staged import files", zap.Int("removed", removed))
	}
	return removed, nil
}
