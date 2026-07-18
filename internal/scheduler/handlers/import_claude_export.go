package handlers

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/importer"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/scheduler"
	"github.com/iulita-ai/iulita/internal/storage"
)

// TaskTypeImportClaudeExport is the task type for a Claude.ai data export import.
const TaskTypeImportClaudeExport = "import.claude_export"

const (
	importMemoriesMember      = "memories.json"
	importConversationsMember = "conversations.json"

	maxConversationsBytes = 1 << 30 // 1GB uncompressed (owner-set zip-bomb cap)
	maxMemoriesBytes      = 8 << 20 // 8MB uncompressed
	maxConvElementBytes   = 64 << 20

	embedBatchMessages = 200
	embedChunkSubBatch = 16                     // max chunks per Embed call (bounds mutex hold time)
	embedThrottle      = 100 * time.Millisecond // keeps added latency on live Embed well under 500ms
	heartbeatInterval  = 60 * time.Second
	progressEveryConvs = 20

	importRunsRetention = 50 // owner-set: keep the newest N import runs
)

// importPayload is the task payload for an import job.
type importPayload struct {
	ZipPath        string `json:"zip_path"`
	UserID         string `json:"user_id"`
	JobID          string `json:"job_id"`
	SourceSHA      string `json:"source_sha,omitempty"`
	ImportMemories bool   `json:"import_memories"`
}

// importStats accumulates counters across the import phases.
type importStats struct {
	Conversations   int
	MessagesStored  int
	MessagesSkipped int
	Facts           int
	SkippedBinaries int
	ChunksEmbedded  int
	ParseErrors     int
}

// ImportClaudeExportHandler imports a Claude.ai export (memories → facts,
// conversations → isolated archive) as a durable background task.
type ImportClaudeExportHandler struct {
	store  storage.Repository
	embed  llm.EmbeddingProvider // optional; nil → FTS-only archive (no vectors)
	bus    *eventbus.Bus
	logger *zap.Logger
}

// NewImportClaudeExportHandler constructs the import handler. embed may be nil.
func NewImportClaudeExportHandler(store storage.Repository, embed llm.EmbeddingProvider, bus *eventbus.Bus, logger *zap.Logger) *ImportClaudeExportHandler {
	return &ImportClaudeExportHandler{store: store, embed: embed, bus: bus, logger: logger}
}

// Type returns the task type handled by this handler.
func (h *ImportClaudeExportHandler) Type() string { return TaskTypeImportClaudeExport }

// Handle runs the import. The staged zip is removed ONLY on successful completion;
// on any retryable failure or ctx cancellation (shutdown or heartbeat-detected
// reclaim) it is left in place so a re-claim resumes the remaining delta
// idempotently (ON CONFLICT + ImportedMessagesWithoutEmbeddings). Zips orphaned by a
// terminal failure (attempts exhausted) are reaped by the Phase-5 startup sweep.
func (h *ImportClaudeExportHandler) Handle(ctx context.Context, payload string) (string, error) {
	var p importPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("invalid import payload: %w", err)
	}
	if p.ZipPath == "" || p.JobID == "" {
		return "", errors.New("import payload missing zip_path or job_id")
	}

	started := time.Now()
	var stats importStats
	// reclaimed is set by the heartbeat when this worker loses ownership; in that case
	// the reclaiming worker owns the ImportRun row, so we must not clobber it on abort.
	var reclaimed atomic.Bool
	h.writeRun(ctx, &p, "running", "starting", 0, 0, &stats, started, "", false)
	h.publish(ctx, eventbus.ImportStarted, eventbus.ImportProgressPayload{JobID: p.JobID, UserID: p.UserID, Phase: "starting"})

	// Heartbeat: bump claimed_at so CleanupStaleTasks does not reclaim this long job.
	// If the heartbeat finds we no longer own the task, cancel so we abort promptly.
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	startImportHeartbeat(ctx, hctx, cancel, &reclaimed, h.store, h.logger)

	zr, err := zip.OpenReader(p.ZipPath)
	if err != nil {
		return "", h.fail(ctx, &p, &stats, fmt.Errorf("opening export zip: %w", err), &reclaimed)
	}
	defer zr.Close() //nolint:errcheck // read-only reader close

	members := map[string]*zip.File{}
	for _, f := range zr.File {
		members[f.Name] = f
	}
	if members[importConversationsMember] == nil {
		return "", h.fail(ctx, &p, &stats, fmt.Errorf("export missing %s", importConversationsMember), &reclaimed)
	}

	if p.ImportMemories {
		if err = h.importMemories(hctx, &p, members[importMemoriesMember], &stats); err != nil {
			return "", h.fail(ctx, &p, &stats, err, &reclaimed)
		}
	}

	if err = h.importConversations(hctx, &p, members[importConversationsMember], &stats); err != nil {
		return "", h.fail(ctx, &p, &stats, err, &reclaimed)
	}

	if h.embed != nil {
		if err = h.embedArchive(hctx, &p, &stats); err != nil {
			return "", h.fail(ctx, &p, &stats, err, &reclaimed)
		}
	}

	// Success: the staged zip is no longer needed.
	if rmErr := os.Remove(p.ZipPath); rmErr != nil && !os.IsNotExist(rmErr) {
		h.logger.Warn("failed to remove staged import zip", zap.String("path", p.ZipPath), zap.Error(rmErr))
	}

	h.writeRun(ctx, &p, "done", "done", stats.Conversations, stats.Conversations, &stats, started, "", true)
	h.pruneRuns(ctx) // bound import_runs growth (owner-set retention)
	h.publish(ctx, eventbus.ImportDone, eventbus.ImportDonePayload{
		JobID: p.JobID, UserID: p.UserID,
		Conversations: stats.Conversations, MessagesStored: stats.MessagesStored,
		MessagesSkipped: stats.MessagesSkipped, Facts: stats.Facts,
		SkippedBinaries: stats.SkippedBinaries, ChunksEmbedded: stats.ChunksEmbedded,
		ParseErrors: stats.ParseErrors, DurationSeconds: time.Since(started).Seconds(),
	})
	h.logger.Info("claude import complete",
		zap.String("job_id", p.JobID),
		zap.Int("conversations", stats.Conversations),
		zap.Int("messages_stored", stats.MessagesStored),
		zap.Int("messages_skipped", stats.MessagesSkipped),
		zap.Int("facts", stats.Facts),
		zap.Int("skipped_binaries", stats.SkippedBinaries),
		zap.Int("chunks_embedded", stats.ChunksEmbedded),
		zap.Int("parse_errors", stats.ParseErrors),
		zap.Duration("took", time.Since(started)),
	)

	result, err := json.Marshal(stats)
	if err != nil {
		return "", fmt.Errorf("marshaling import summary: %w", err)
	}
	return string(result), nil
}

// startImportHeartbeat bumps the task's claimed_at periodically so CleanupStaleTasks
// does not reclaim a long-running import/reindex job. On losing ownership it sets
// reclaimed and cancels so the handler aborts promptly. Shared by import and reindex.
func startImportHeartbeat(parent, hctx context.Context, cancel context.CancelFunc, reclaimed *atomic.Bool, store storage.Repository, logger *zap.Logger) {
	taskID := scheduler.TaskIDFrom(parent)
	if taskID <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
				owned, err := store.TouchTask(hctx, taskID)
				if err != nil {
					logger.Warn("import heartbeat failed", zap.Int64("task_id", taskID), zap.Error(err))
					continue
				}
				if !owned {
					logger.Warn("import task no longer owned; aborting", zap.Int64("task_id", taskID))
					reclaimed.Store(true)
					cancel()
					return
				}
			}
		}
	}()
}

// embedPendingMessages embeds all archived messages lacking vectors, in bounded
// sub-batches with throttling so the shared embedding pipeline stays responsive to
// the live assistant. onBatch (optional) is called after each fetched batch with the
// running chunk total. Shared by the import embed phase and the reindex handler.
func embedPendingMessages(ctx context.Context, store storage.Repository, embed llm.EmbeddingProvider, logger *zap.Logger, onBatch func(chunks int)) (int, error) {
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		batch, err := store.ImportedMessagesWithoutEmbeddings(ctx, embedBatchMessages)
		if err != nil {
			return total, fmt.Errorf("loading messages to embed: %w", err)
		}
		if len(batch) == 0 {
			return total, nil
		}
		progressed := 0
		for i := range batch {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			m := &batch[i]
			chunks := importer.Chunk(m.Content, importer.DefaultChunkChars)
			if len(chunks) == 0 {
				continue
			}
			vecs := make([][]float32, 0, len(chunks))
			for start := 0; start < len(chunks); start += embedChunkSubBatch {
				if err := ctx.Err(); err != nil {
					return total, err
				}
				end := start + embedChunkSubBatch
				if end > len(chunks) {
					end = len(chunks)
				}
				sub, eErr := embed.Embed(ctx, chunks[start:end])
				if eErr != nil {
					return total, fmt.Errorf("embedding message %d: %w", m.ID, eErr)
				}
				vecs = append(vecs, sub...)
				select {
				case <-ctx.Done():
					return total, ctx.Err()
				case <-time.After(embedThrottle):
				}
			}
			for ci, v := range vecs {
				if sErr := store.SaveImportedMessageVector(ctx, m.ID, ci, v); sErr != nil {
					return total, fmt.Errorf("saving vector: %w", sErr)
				}
				total++
			}
			progressed++
		}
		if onBatch != nil {
			onBatch(total)
		}
		if progressed == 0 {
			// Defensive: a non-empty batch where nothing embedded would otherwise
			// re-fetch forever. Cannot happen while stored content is non-empty.
			if logger != nil {
				logger.Warn("embedding made no progress; stopping")
			}
			return total, nil
		}
	}
}

func (h *ImportClaudeExportHandler) importMemories(ctx context.Context, p *importPayload, f *zip.File, stats *importStats) error {
	if f == nil {
		// import_memories was requested but the export has no memories.json. Non-fatal,
		// but surface it so the no-op is visible in logs and the run record.
		h.logger.Warn("memories.json not present in export; skipping memory import", zap.String("job_id", p.JobID))
		return nil
	}
	data, err := readMember(f, maxMemoriesBytes)
	if err != nil {
		return fmt.Errorf("reading %s: %w", importMemoriesMember, err)
	}
	facts, err := importer.MapMemories(data, p.UserID)
	if err != nil {
		return fmt.Errorf("mapping memories: %w", err)
	}
	for i := range facts {
		if err := ctx.Err(); err != nil {
			return err
		}
		mf := &facts[i]
		exists, err := h.store.ImportedFactKeyExists(ctx, mf.DedupKey)
		if err != nil {
			return fmt.Errorf("checking fact key: %w", err)
		}
		if exists {
			continue // insert-only idempotency; body-update on change is a future enhancement
		}
		fact := mf.Fact
		if err := h.store.SaveFact(ctx, &fact); err != nil {
			return fmt.Errorf("saving imported fact: %w", err)
		}
		if err := h.store.SaveImportedFactKey(ctx, &domain.ImportedFactKey{SourceUUID: mf.DedupKey, FactID: fact.ID}); err != nil {
			return fmt.Errorf("saving fact key: %w", err)
		}
		stats.Facts++
	}
	h.publish(ctx, eventbus.ImportProgress, eventbus.ImportProgressPayload{
		JobID: p.JobID, UserID: p.UserID, Phase: "memories", Done: len(facts), Total: len(facts), Stored: stats.Facts,
	})
	return nil
}

func (h *ImportClaudeExportHandler) importConversations(ctx context.Context, p *importPayload, f *zip.File, stats *importStats) error {
	if f.UncompressedSize64 > maxConversationsBytes {
		return fmt.Errorf("%s too large: %d bytes", importConversationsMember, f.UncompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("opening %s: %w", importConversationsMember, err)
	}
	defer rc.Close() //nolint:errcheck // read-only reader close

	reader := io.LimitReader(rc, maxConversationsBytes+1)
	err = importer.StreamConversations(reader, func(raw json.RawMessage) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		res, mapErr := importer.MapConversation(raw, p.UserID, maxConvElementBytes)
		if mapErr != nil {
			stats.ParseErrors++
			h.logger.Warn("skipping unparseable conversation", zap.String("job_id", p.JobID), zap.Error(mapErr))
			return nil
		}
		if res.Oversized {
			stats.ParseErrors++
			h.logger.Warn("skipping oversized conversation", zap.String("job_id", p.JobID), zap.String("uuid", res.Conversation.SourceUUID))
			return nil
		}
		if _, serr := h.store.SaveImportedConversation(ctx, &res.Conversation); serr != nil {
			return fmt.Errorf("saving conversation: %w", serr)
		}
		for i := range res.Messages {
			if _, serr := h.store.SaveImportedMessage(ctx, &res.Messages[i]); serr != nil {
				return fmt.Errorf("saving message: %w", serr)
			}
			stats.MessagesStored++
		}
		stats.Conversations++
		stats.MessagesSkipped += res.SkippedEmpty
		stats.SkippedBinaries += res.SkippedBinaries
		stats.ParseErrors += res.ParseErrors
		if stats.Conversations%progressEveryConvs == 0 {
			h.writeRun(ctx, p, "running", "conversations", stats.Conversations, 0, stats, time.Time{}, "", false)
			h.publish(ctx, eventbus.ImportProgress, eventbus.ImportProgressPayload{
				JobID: p.JobID, UserID: p.UserID, Phase: "conversations",
				Done: stats.Conversations, Stored: stats.MessagesStored, Skipped: stats.MessagesSkipped,
			})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("streaming conversations: %w", err)
	}
	return nil
}

func (h *ImportClaudeExportHandler) embedArchive(ctx context.Context, p *importPayload, stats *importStats) error {
	chunks, err := embedPendingMessages(ctx, h.store, h.embed, h.logger, func(total int) {
		stats.ChunksEmbedded = total
		h.writeRun(ctx, p, "running", "embedding", total, 0, stats, time.Time{}, "", false)
		h.publish(ctx, eventbus.ImportProgress, eventbus.ImportProgressPayload{
			JobID: p.JobID, UserID: p.UserID, Phase: "embedding", Done: total,
		})
	})
	stats.ChunksEmbedded = chunks
	return err
}

// pruneRuns trims import_runs to the retention limit under a detached, bounded context.
func (h *ImportClaudeExportHandler) pruneRuns(ctx context.Context) {
	pruneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if n, err := h.store.PruneImportRuns(pruneCtx, importRunsRetention); err != nil {
		h.logger.Warn("failed to prune import runs", zap.Error(err))
	} else if n > 0 {
		h.logger.Info("pruned old import runs", zap.Int("removed", n))
	}
}

// fail records a failed run (with partial counters), publishes the failure, and
// returns the error so the worker marks the task failed. The staged zip is left in
// place so a re-claim can resume. When this worker was reclaimed, the new owner owns
// the ImportRun row, so the terminal status is NOT persisted (avoids a backward flip).
func (h *ImportClaudeExportHandler) fail(ctx context.Context, p *importPayload, stats *importStats, cause error, reclaimed *atomic.Bool) error {
	if reclaimed != nil && reclaimed.Load() {
		// The reclaiming worker owns the run row; only balance the in-flight gauge.
		h.publish(ctx, eventbus.ImportAborted, eventbus.ImportProgressPayload{JobID: p.JobID, UserID: p.UserID})
		return cause
	}
	h.writeRun(ctx, p, "failed", "failed", stats.Conversations, stats.Conversations, stats, time.Time{}, cause.Error(), true)
	h.pruneRuns(ctx) // bound import_runs even when imports fail
	h.publish(ctx, eventbus.ImportFailed, eventbus.ImportFailedPayload{
		JobID: p.JobID, UserID: p.UserID, Error: cause.Error(),
		ConversationsDone: stats.Conversations, MessagesStored: stats.MessagesStored, Facts: stats.Facts,
	})
	return cause
}

func (h *ImportClaudeExportHandler) writeRun(ctx context.Context, p *importPayload, status, phase string, done, total int, stats *importStats, started time.Time, errMsg string, finished bool) {
	run := &domain.ImportRun{
		JobID:           p.JobID,
		UserID:          p.UserID,
		Status:          status,
		SourceSHA:       p.SourceSHA,
		Conversations:   stats.Conversations,
		MessagesStored:  stats.MessagesStored,
		MessagesSkipped: stats.MessagesSkipped,
		Facts:           stats.Facts,
		SkippedBinaries: stats.SkippedBinaries,
		ChunksEmbedded:  stats.ChunksEmbedded,
		ParseErrors:     stats.ParseErrors,
		LastPhase:       phase,
		LastDone:        done,
		LastTotal:       total,
		Error:           errMsg,
	}
	if !started.IsZero() {
		run.StartedAt = started
	}
	if finished {
		run.FinishedAt = time.Now()
	}
	// Persist status even if the job's context was canceled — use a fresh context so
	// the failure/final state is durably recorded for the dashboard.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.store.UpsertImportRun(wctx, run); err != nil {
		h.logger.Warn("failed to persist import run", zap.String("job_id", p.JobID), zap.Error(err))
	}
}

func (h *ImportClaudeExportHandler) publish(ctx context.Context, evtType string, payload any) {
	if h.bus == nil {
		return
	}
	h.bus.Publish(ctx, eventbus.Event{Type: evtType, Payload: payload})
}

// readMember reads a zip member fully with a size guard. maxBytes is a small positive
// compile-time constant, so the uint64 comparison cannot overflow.
func readMember(f *zip.File, maxBytes int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(maxBytes) { //nolint:gosec // maxBytes is a small positive const
		return nil, fmt.Errorf("member %s too large: %d bytes", f.Name, f.UncompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read-only reader close
	return io.ReadAll(io.LimitReader(rc, maxBytes+1))
}
