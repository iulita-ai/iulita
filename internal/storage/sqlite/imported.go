package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/uptrace/bun"

	"github.com/iulita-ai/iulita/internal/domain"
)

// SaveImportedConversation upserts an archived conversation keyed by source_uuid.
// On a delta re-import the export snapshot is the source of truth for the mutable
// header (title/summary/message_count/updated_at), so those are refreshed from the
// new snapshot while created_at/imported_at are preserved. Returns created=true only
// when the conversation did not previously exist (SQLite's ON CONFLICT DO UPDATE
// cannot distinguish insert from update via RowsAffected, hence the existence check).
func (s *Store) SaveImportedConversation(ctx context.Context, c *domain.ImportedConversation) (bool, error) {
	if c.ImportedAt.IsZero() {
		c.ImportedAt = time.Now()
	}
	existed, err := s.db.NewSelect().
		Model((*domain.ImportedConversation)(nil)).
		Where("source_uuid = ?", c.SourceUUID).
		Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("checking imported conversation: %w", err)
	}
	_, err = s.db.NewInsert().
		Model(c).
		On("CONFLICT (source_uuid) DO UPDATE").
		Set("title = EXCLUDED.title").
		Set("summary = EXCLUDED.summary").
		Set("message_count = EXCLUDED.message_count").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("upserting imported conversation: %w", err)
	}
	return !existed, nil
}

// SaveImportedMessage inserts an archived message, ignoring duplicates by source_uuid.
// Returns inserted=false when the message already exists. When inserted=true the
// message's ID is populated (used to enqueue embedding). Embedding is NOT triggered
// here — it runs as a separate batched pass to serialize the shared embedding pipeline.
func (s *Store) SaveImportedMessage(ctx context.Context, m *domain.ImportedMessage) (bool, error) {
	if m.ImportedAt.IsZero() {
		m.ImportedAt = time.Now()
	}
	res, err := s.db.NewInsert().
		Model(m).
		On("CONFLICT (source_uuid) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("inserting imported message: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("imported message rows affected: %w", err)
	}
	return rows > 0, nil
}

// ImportedMessagesWithoutEmbeddings returns archived messages that have no vector
// embedding yet (delta for the embedding pass). Uses NOT IN rather than a join —
// modernc handles this form well and does not support UPDATE..FROM.
func (s *Store) ImportedMessagesWithoutEmbeddings(ctx context.Context, limit int) ([]domain.ImportedMessage, error) {
	if limit <= 0 {
		limit = 200
	}
	var msgs []domain.ImportedMessage
	err := s.db.NewSelect().
		Model(&msgs).
		Where("id NOT IN (SELECT message_id FROM imported_message_vectors)").
		Order("id ASC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying imported messages without embeddings: %w", err)
	}
	return msgs, nil
}

// SaveImportedMessageVector stores one chunk embedding for an archived message.
// Keyed by (message_id, chunk_index) so re-embedding is idempotent.
func (s *Store) SaveImportedMessageVector(ctx context.Context, messageID int64, chunkIndex int, embedding []float32) error {
	data := encodeVector(embedding)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO imported_message_vectors (message_id, chunk_index, embedding, created_at) VALUES (?, ?, ?, ?)`,
		messageID, chunkIndex, data, time.Now())
	if err != nil {
		return fmt.Errorf("saving imported message vector: %w", err)
	}
	return nil
}

// searchImportedMessagesFTS returns user-scoped archived messages matching the query
// via FTS5, newest first. Empty sanitized query yields no FTS candidates.
func (s *Store) searchImportedMessagesFTS(ctx context.Context, userID, query string, limit int) ([]domain.ImportedMessage, error) {
	match := sanitizeFTS5Query(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	var msgs []domain.ImportedMessage
	err := s.db.NewSelect().
		Model(&msgs).
		Where("user_id = ?", userID).
		Where("id IN (SELECT rowid FROM imported_messages_fts WHERE imported_messages_fts MATCH ?)", match).
		OrderExpr("id DESC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("searching imported messages: %w", err)
	}
	return msgs, nil
}

// SearchImportedMessagesHybrid combines FTS and vector search over the isolated
// archive, scoped to a single user. queryVec==nil (or vectorWeight<=0) falls back to
// FTS-only. Per-message vector score is max-pooled across the message's chunks. This
// path is deliberately NOT wired into the assistant's live retrieval.
func (s *Store) SearchImportedMessagesHybrid(ctx context.Context, userID, query string, queryVec []float32, limit int, vectorWeight float64) ([]domain.ImportedMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	ftsResults, err := s.searchImportedMessagesFTS(ctx, userID, query, limit*2)
	if err != nil {
		return nil, err
	}

	if queryVec == nil || vectorWeight <= 0 {
		if len(ftsResults) > limit {
			return ftsResults[:limit], nil
		}
		return ftsResults, nil
	}

	// Load all archive vectors for this user (chunk rows joined to their messages).
	// This is a brute-force full scan + in-process cosine, mirroring SearchFactsHybrid.
	// The vector-only path (empty FTS query, non-nil queryVec) fundamentally needs
	// every vector, so we cannot pre-narrow to FTS candidates without breaking pure
	// semantic search. Acceptable at the current archive scale (thousands of messages);
	// replace with an ANN/index-backed strategy before the archive grows large. Seam.
	var rows []struct {
		MessageID int64  `bun:"message_id"`
		Embedding []byte `bun:"embedding"`
	}
	err = s.db.NewSelect().
		TableExpr("imported_message_vectors v").
		Join("JOIN imported_messages m ON m.id = v.message_id").
		Where("m.user_id = ?", userID).
		ColumnExpr("v.message_id, v.embedding").
		Scan(ctx, &rows)
	if err != nil {
		// Fall back to FTS-only on vector-load failure (degrade, don't fail the
		// search) — mirrors SearchFactsHybrid.
		if len(ftsResults) > limit {
			return ftsResults[:limit], nil
		}
		return ftsResults, nil
	}

	// Max-pool cosine similarity per message across its chunks.
	vecScores := make(map[int64]float64)
	for _, row := range rows {
		emb, decErr := decodeVector(row.Embedding)
		if decErr != nil {
			continue
		}
		sc := cosineSimilarity(queryVec, emb)
		if cur, ok := vecScores[row.MessageID]; !ok || sc > cur {
			vecScores[row.MessageID] = sc
		}
	}

	// FTS score map (rank by position).
	ftsScores := make(map[int64]float64)
	for i := range ftsResults {
		ftsScores[ftsResults[i].ID] = 1.0 - float64(i)/float64(len(ftsResults)+1)
	}

	// Merge candidate IDs.
	allIDs := make(map[int64]struct{})
	for id := range ftsScores {
		allIDs[id] = struct{}{}
	}
	for id := range vecScores {
		allIDs[id] = struct{}{}
	}

	type scored struct {
		id    int64
		score float64
	}
	var candidates []scored
	for id := range allIDs {
		combined := (1-vectorWeight)*ftsScores[id] + vectorWeight*vecScores[id]
		candidates = append(candidates, scored{id, combined})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
	}

	var msgs []domain.ImportedMessage
	err = s.db.NewSelect().
		Model(&msgs).
		Where("id IN (?)", bun.List(ids)).
		Scan(ctx)
	if err != nil {
		return ftsResults, nil //nolint:nilerr // intentional degrade to FTS on fetch failure
	}

	// Reorder by combined score.
	idOrder := make(map[int64]int)
	for i, c := range candidates {
		idOrder[c.id] = i
	}
	sort.Slice(msgs, func(i, j int) bool {
		return idOrder[msgs[i].ID] < idOrder[msgs[j].ID]
	})
	return msgs, nil
}

// ListImportedConversations lists a user's archived conversations, newest first.
func (s *Store) ListImportedConversations(ctx context.Context, userID string, limit, offset int) ([]domain.ImportedConversation, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var convs []domain.ImportedConversation
	err := s.db.NewSelect().
		Model(&convs).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing imported conversations: %w", err)
	}
	return convs, nil
}

// GetImportedConversationMessages returns a conversation's messages in order,
// scoped to the owning user (IDOR guard).
func (s *Store) GetImportedConversationMessages(ctx context.Context, userID, conversationUUID string) ([]domain.ImportedMessage, error) {
	var msgs []domain.ImportedMessage
	err := s.db.NewSelect().
		Model(&msgs).
		Where("user_id = ?", userID).
		Where("conversation_uuid = ?", conversationUUID).
		Order("seq ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting imported conversation messages: %w", err)
	}
	return msgs, nil
}

// ImportedFactKeyExists reports whether a memory fact with this dedup key was imported.
func (s *Store) ImportedFactKeyExists(ctx context.Context, sourceUUID string) (bool, error) {
	exists, err := s.db.NewSelect().
		Model((*domain.ImportedFactKey)(nil)).
		Where("source_uuid = ?", sourceUUID).
		Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("checking imported fact key: %w", err)
	}
	return exists, nil
}

// SaveImportedFactKey records a memory→fact dedup key, ignoring duplicates.
func (s *Store) SaveImportedFactKey(ctx context.Context, k *domain.ImportedFactKey) error {
	if k.ImportedAt.IsZero() {
		k.ImportedAt = time.Now()
	}
	_, err := s.db.NewInsert().
		Model(k).
		On("CONFLICT (source_uuid) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("saving imported fact key: %w", err)
	}
	return nil
}

// UpsertImportRun inserts or updates the durable summary for an import run, keyed by
// job_id. started_at is preserved on update.
func (s *Store) UpsertImportRun(ctx context.Context, r *domain.ImportRun) error {
	_, err := s.db.NewInsert().
		Model(r).
		On("CONFLICT (job_id) DO UPDATE").
		Set("status = EXCLUDED.status").
		Set("source_sha = EXCLUDED.source_sha").
		Set("conversations = EXCLUDED.conversations").
		Set("messages_stored = EXCLUDED.messages_stored").
		Set("messages_skipped = EXCLUDED.messages_skipped").
		Set("facts = EXCLUDED.facts").
		Set("skipped_binaries = EXCLUDED.skipped_binaries").
		Set("chunks_embedded = EXCLUDED.chunks_embedded").
		Set("parse_errors = EXCLUDED.parse_errors").
		Set("last_phase = EXCLUDED.last_phase").
		Set("last_done = EXCLUDED.last_done").
		Set("last_total = EXCLUDED.last_total").
		Set("error = EXCLUDED.error").
		Set("finished_at = EXCLUDED.finished_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("upserting import run: %w", err)
	}
	return nil
}

// GetImportRun fetches a user's run summary by job_id. Returns (nil, nil) when the
// run does not exist; genuine DB errors (e.g. SQLITE_BUSY outlasting busy_timeout)
// are propagated so callers do not mistake a transient failure for "no such run".
// Scoped by user_id (IDOR guard) to match ListImportRuns.
func (s *Store) GetImportRun(ctx context.Context, userID, jobID string) (*domain.ImportRun, error) {
	r := new(domain.ImportRun)
	err := s.db.NewSelect().
		Model(r).
		Where("job_id = ?", jobID).
		Where("user_id = ?", userID).
		Limit(1).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting import run %s: %w", jobID, err)
	}
	return r, nil
}

// ListImportRuns lists a user's import runs, newest first.
func (s *Store) ListImportRuns(ctx context.Context, userID string, limit int) ([]domain.ImportRun, error) {
	if limit <= 0 {
		limit = 50
	}
	var runs []domain.ImportRun
	err := s.db.NewSelect().
		Model(&runs).
		Where("user_id = ?", userID).
		Order("id DESC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing import runs: %w", err)
	}
	return runs, nil
}

// TouchTask bumps claimed_at for a claimed/running task, acting as a worker heartbeat
// so CleanupStaleTasks does not reclaim a long-running job out from under its worker.
// Returns stillOwned=false when no claimed/running row matched — i.e. the task was
// already reclaimed, completed, or failed — so the caller can abort rather than keep
// working on a job another worker may now own.
func (s *Store) TouchTask(ctx context.Context, taskID int64) (bool, error) {
	res, err := s.db.NewUpdate().
		Model((*domain.Task)(nil)).
		Set("claimed_at = ?", time.Now()).
		Where("id = ?", taskID).
		Where("status IN (?, ?)", domain.TaskStatusClaimed, domain.TaskStatusRunning).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("touching task %d: %w", taskID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("touch task %d rows affected: %w", taskID, err)
	}
	return rows > 0, nil
}
