package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestImportedConversationIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	conv := &domain.ImportedConversation{
		SourceUUID:   "conv-1",
		UserID:       "admin",
		Title:        "First chat",
		MessageCount: 2,
		CreatedAt:    time.Now(),
	}
	inserted, err := store.SaveImportedConversation(ctx, conv)
	if err != nil {
		t.Fatalf("SaveImportedConversation: %v", err)
	}
	if !inserted {
		t.Fatal("expected first insert to report inserted=true")
	}

	// Re-importing the same source_uuid does not create a new row, and reports
	// created=false, but DOES refresh the mutable header from the newer snapshot
	// (delta re-import: the export snapshot is the source of truth).
	dup := &domain.ImportedConversation{SourceUUID: "conv-1", UserID: "admin", Title: "Renamed chat", MessageCount: 7}
	created, err := store.SaveImportedConversation(ctx, dup)
	if err != nil {
		t.Fatalf("SaveImportedConversation dup: %v", err)
	}
	if created {
		t.Fatal("expected re-import to report created=false")
	}

	convs, err := store.ListImportedConversations(ctx, "admin", 10, 0)
	if err != nil {
		t.Fatalf("ListImportedConversations: %v", err)
	}
	if len(convs) != 1 {
		t.Fatalf("expected 1 conversation after re-import, got %d", len(convs))
	}
	if convs[0].Title != "Renamed chat" || convs[0].MessageCount != 7 {
		t.Errorf("expected header refreshed to snapshot, got title=%q count=%d", convs[0].Title, convs[0].MessageCount)
	}
}

func TestImportedMessageIdempotentAndFTS(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	msg := &domain.ImportedMessage{
		SourceUUID:       "msg-1",
		ConversationUUID: "conv-1",
		UserID:           "admin",
		Sender:           "human",
		Seq:              0,
		Content:          "the quick brown fox jumps",
		CreatedAt:        time.Now(),
	}
	inserted, err := store.SaveImportedMessage(ctx, msg)
	if err != nil {
		t.Fatalf("SaveImportedMessage: %v", err)
	}
	if !inserted || msg.ID == 0 {
		t.Fatalf("expected inserted=true with non-zero ID, got inserted=%v id=%d", inserted, msg.ID)
	}

	// FTS trigger should have populated the mirror.
	results, err := store.SearchImportedMessagesHybrid(ctx, "admin", "brown", nil, 10, 0)
	if err != nil {
		t.Fatalf("SearchImportedMessagesHybrid: %v", err)
	}
	if len(results) != 1 || results[0].SourceUUID != "msg-1" {
		t.Fatalf("expected FTS hit for 'brown', got %d results", len(results))
	}

	// Duplicate source_uuid is ignored.
	dup := &domain.ImportedMessage{SourceUUID: "msg-1", ConversationUUID: "conv-1", UserID: "admin", Sender: "human", Content: "different text"}
	inserted, err = store.SaveImportedMessage(ctx, dup)
	if err != nil {
		t.Fatalf("SaveImportedMessage dup: %v", err)
	}
	if inserted {
		t.Fatal("expected duplicate message insert to report inserted=false")
	}

	// A direct UPDATE of content must not raise "SQL logic error (1)" — there is
	// deliberately no AFTER UPDATE trigger, but the archive should still tolerate it.
	if _, err = store.db.ExecContext(ctx, `UPDATE imported_messages SET content = ? WHERE source_uuid = ?`, "updated content", "msg-1"); err != nil {
		t.Fatalf("direct UPDATE on imported_messages failed: %v", err)
	}

	// Delete removes the FTS entry.
	if _, err = store.db.ExecContext(ctx, `DELETE FROM imported_messages WHERE source_uuid = ?`, "msg-1"); err != nil {
		t.Fatalf("delete imported_messages: %v", err)
	}
	results, err = store.SearchImportedMessagesHybrid(ctx, "admin", "brown", nil, 10, 0)
	if err != nil {
		t.Fatalf("SearchImportedMessagesHybrid after delete: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 FTS hits after delete, got %d", len(results))
	}
}

func TestImportedSearchUserScoped(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	mustSave := func(uuid, user, content string) {
		_, err := store.SaveImportedMessage(ctx, &domain.ImportedMessage{
			SourceUUID: uuid, ConversationUUID: "c", UserID: user, Sender: "human", Content: content, CreatedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("SaveImportedMessage(%s): %v", uuid, err)
		}
	}
	mustSave("a", "admin", "kubernetes deployment notes")
	mustSave("b", "other", "kubernetes secrets")

	results, err := store.SearchImportedMessagesHybrid(ctx, "admin", "kubernetes", nil, 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].SourceUUID != "a" {
		t.Fatalf("expected only admin's message, got %d results", len(results))
	}
}

func TestImportedMessagesWithoutEmbeddings(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	m1 := &domain.ImportedMessage{SourceUUID: "m1", ConversationUUID: "c", UserID: "admin", Sender: "human", Content: "one"}
	m2 := &domain.ImportedMessage{SourceUUID: "m2", ConversationUUID: "c", UserID: "admin", Sender: "human", Content: "two"}
	for _, m := range []*domain.ImportedMessage{m1, m2} {
		if _, err := store.SaveImportedMessage(ctx, m); err != nil {
			t.Fatalf("save %s: %v", m.SourceUUID, err)
		}
	}

	// Embed only m1 (two chunks).
	if err := store.SaveImportedMessageVector(ctx, m1.ID, 0, []float32{0.1, 0.2, 0.3}); err != nil {
		t.Fatalf("save vector chunk 0: %v", err)
	}
	if err := store.SaveImportedMessageVector(ctx, m1.ID, 1, []float32{0.4, 0.5, 0.6}); err != nil {
		t.Fatalf("save vector chunk 1: %v", err)
	}

	pending, err := store.ImportedMessagesWithoutEmbeddings(ctx, 100)
	if err != nil {
		t.Fatalf("ImportedMessagesWithoutEmbeddings: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != m2.ID {
		t.Fatalf("expected only m2 pending, got %d", len(pending))
	}

	// Re-embedding the same chunk is idempotent (INSERT OR REPLACE on unique index).
	if err := store.SaveImportedMessageVector(ctx, m1.ID, 0, []float32{0.9, 0.9, 0.9}); err != nil {
		t.Fatalf("re-embed chunk 0: %v", err)
	}
}

func TestImportedHybridSearchVectorScored(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Two messages that both match FTS; vectors decide ranking.
	near := &domain.ImportedMessage{SourceUUID: "near", ConversationUUID: "c", UserID: "admin", Sender: "human", Content: "vector alpha topic"}
	far := &domain.ImportedMessage{SourceUUID: "far", ConversationUUID: "c", UserID: "admin", Sender: "human", Content: "vector beta topic"}
	for _, m := range []*domain.ImportedMessage{near, far} {
		if _, err := store.SaveImportedMessage(ctx, m); err != nil {
			t.Fatalf("save %s: %v", m.SourceUUID, err)
		}
	}
	query := []float32{1, 0, 0}
	if err := store.SaveImportedMessageVector(ctx, near.ID, 0, []float32{1, 0, 0}); err != nil {
		t.Fatalf("vec near: %v", err)
	}
	if err := store.SaveImportedMessageVector(ctx, far.ID, 0, []float32{0, 1, 0}); err != nil {
		t.Fatalf("vec far: %v", err)
	}

	results, err := store.SearchImportedMessagesHybrid(ctx, "admin", "vector", query, 10, 1.0)
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(results) < 1 || results[0].SourceUUID != "near" {
		t.Fatalf("expected 'near' ranked first by vector score, got %+v", results)
	}
}

// TestImportedHybridMaxPoolAcrossChunks ensures a message's vector score is the MAX
// over its chunks: a message with one far chunk and one near chunk must outrank a
// message whose single chunk is moderately far. A min/last/first pooling regression
// would fail this.
func TestImportedHybridMaxPoolAcrossChunks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	multi := &domain.ImportedMessage{SourceUUID: "multi", ConversationUUID: "c", UserID: "admin", Sender: "human", Content: "vector chunked topic"}
	single := &domain.ImportedMessage{SourceUUID: "single", ConversationUUID: "c", UserID: "admin", Sender: "human", Content: "vector single topic"}
	for _, m := range []*domain.ImportedMessage{multi, single} {
		if _, err := store.SaveImportedMessage(ctx, m); err != nil {
			t.Fatalf("save %s: %v", m.SourceUUID, err)
		}
	}
	query := []float32{1, 0, 0}
	// multi: chunk 0 is orthogonal (score 0), chunk 1 is an exact match (score 1) → max-pool = 1.
	if err := store.SaveImportedMessageVector(ctx, multi.ID, 0, []float32{0, 1, 0}); err != nil {
		t.Fatalf("vec multi chunk0: %v", err)
	}
	if err := store.SaveImportedMessageVector(ctx, multi.ID, 1, []float32{1, 0, 0}); err != nil {
		t.Fatalf("vec multi chunk1: %v", err)
	}
	// single: one moderately-similar chunk (score ~0.71) → must rank below multi's max=1.
	if err := store.SaveImportedMessageVector(ctx, single.ID, 0, []float32{1, 1, 0}); err != nil {
		t.Fatalf("vec single: %v", err)
	}

	results, err := store.SearchImportedMessagesHybrid(ctx, "admin", "vector", query, 10, 1.0)
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(results) < 1 || results[0].SourceUUID != "multi" {
		t.Fatalf("expected 'multi' ranked first via max-pool over chunks, got %+v", results)
	}
}

// TestImportedVectorOnlySearch exercises the semantic-only path: an empty/whitespace
// FTS query (sanitizes to no match) with a non-nil query vector must still return
// vector-ranked results.
func TestImportedVectorOnlySearch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	a := &domain.ImportedMessage{SourceUUID: "a", ConversationUUID: "c", UserID: "admin", Sender: "human", Content: "completely unrelated words"}
	b := &domain.ImportedMessage{SourceUUID: "b", ConversationUUID: "c", UserID: "admin", Sender: "human", Content: "other unrelated words"}
	for _, m := range []*domain.ImportedMessage{a, b} {
		if _, err := store.SaveImportedMessage(ctx, m); err != nil {
			t.Fatalf("save %s: %v", m.SourceUUID, err)
		}
	}
	query := []float32{1, 0, 0}
	if err := store.SaveImportedMessageVector(ctx, a.ID, 0, []float32{1, 0, 0}); err != nil {
		t.Fatalf("vec a: %v", err)
	}
	if err := store.SaveImportedMessageVector(ctx, b.ID, 0, []float32{0, 1, 0}); err != nil {
		t.Fatalf("vec b: %v", err)
	}

	// Whitespace query → sanitizeFTS5Query yields "", so there are NO FTS candidates;
	// results must come purely from vector similarity.
	results, err := store.SearchImportedMessagesHybrid(ctx, "admin", "   ", query, 10, 1.0)
	if err != nil {
		t.Fatalf("vector-only search: %v", err)
	}
	if len(results) < 1 || results[0].SourceUUID != "a" {
		t.Fatalf("expected vector-only search to return 'a' first, got %+v", results)
	}
}

func TestImportedFactKey(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	exists, err := store.ImportedFactKeyExists(ctx, "key-1")
	if err != nil {
		t.Fatalf("ImportedFactKeyExists: %v", err)
	}
	if exists {
		t.Fatal("expected key to not exist initially")
	}

	if err = store.SaveImportedFactKey(ctx, &domain.ImportedFactKey{SourceUUID: "key-1", FactID: 42}); err != nil {
		t.Fatalf("SaveImportedFactKey: %v", err)
	}
	exists, err = store.ImportedFactKeyExists(ctx, "key-1")
	if err != nil {
		t.Fatalf("ImportedFactKeyExists after save: %v", err)
	}
	if !exists {
		t.Fatal("expected key to exist after save")
	}

	// Saving a duplicate key must not error (ON CONFLICT DO NOTHING).
	if err := store.SaveImportedFactKey(ctx, &domain.ImportedFactKey{SourceUUID: "key-1", FactID: 99}); err != nil {
		t.Fatalf("SaveImportedFactKey duplicate: %v", err)
	}
}

func TestImportRunUpsert(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	run := &domain.ImportRun{
		JobID:     "job-1",
		UserID:    "admin",
		Status:    "running",
		SourceSHA: "abc",
		StartedAt: time.Now(),
	}
	if err := store.UpsertImportRun(ctx, run); err != nil {
		t.Fatalf("UpsertImportRun insert: %v", err)
	}

	// Update the same job_id.
	run.Status = "done"
	run.Conversations = 5
	run.MessagesStored = 100
	run.Facts = 33
	run.FinishedAt = time.Now()
	if err := store.UpsertImportRun(ctx, run); err != nil {
		t.Fatalf("UpsertImportRun update: %v", err)
	}

	got, err := store.GetImportRun(ctx, "admin", "job-1")
	if err != nil {
		t.Fatalf("GetImportRun: %v", err)
	}
	if got == nil {
		t.Fatal("expected run, got nil")
	}
	if got.Status != "done" || got.Conversations != 5 || got.MessagesStored != 100 || got.Facts != 33 {
		t.Fatalf("upsert did not update fields: %+v", got)
	}

	// Non-existent job returns nil without error.
	missing, err := store.GetImportRun(ctx, "admin", "nope")
	if err != nil {
		t.Fatalf("GetImportRun missing: %v", err)
	}
	if missing != nil {
		t.Fatal("expected nil for missing run")
	}

	// A run belonging to another user is not visible (IDOR guard).
	other, err := store.GetImportRun(ctx, "intruder", "job-1")
	if err != nil {
		t.Fatalf("GetImportRun cross-user: %v", err)
	}
	if other != nil {
		t.Fatal("expected nil when job_id belongs to a different user")
	}

	runs, err := store.ListImportRuns(ctx, "admin", 10)
	if err != nil {
		t.Fatalf("ListImportRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
}

func TestTouchTask(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	task := &domain.Task{Type: "import.claude_export", Payload: "{}", Status: domain.TaskStatusPending}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	claimed, err := store.ClaimTask(ctx, "worker-1", nil)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected to claim the task")
	}

	// Backdate claimed_at so we can observe TouchTask bumping it forward.
	old := time.Now().Add(-10 * time.Minute)
	if _, err = store.db.ExecContext(ctx, `UPDATE tasks SET claimed_at = ? WHERE id = ?`, old, claimed.ID); err != nil {
		t.Fatalf("backdate claimed_at: %v", err)
	}

	owned, err := store.TouchTask(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("TouchTask: %v", err)
	}
	if !owned {
		t.Fatal("expected stillOwned=true for a claimed task")
	}

	var bumped time.Time
	if err = store.db.QueryRowContext(ctx, `SELECT claimed_at FROM tasks WHERE id = ?`, claimed.ID).Scan(&bumped); err != nil {
		t.Fatalf("read claimed_at: %v", err)
	}
	if !bumped.After(old) {
		t.Fatalf("expected claimed_at bumped forward, old=%v new=%v", old, bumped)
	}

	// Once the task leaves claimed/running (here: completed) it is no longer owned —
	// a heartbeat must report false so the worker can abort instead of racing a
	// reclaiming worker.
	if _, err = store.db.ExecContext(ctx, `UPDATE tasks SET status = 'done' WHERE id = ?`, claimed.ID); err != nil {
		t.Fatalf("mark task done: %v", err)
	}
	owned, err = store.TouchTask(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("TouchTask after complete: %v", err)
	}
	if owned {
		t.Fatal("expected stillOwned=false after task completed")
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	// Running migrations + vector table creation again must be a no-op, not an error.
	if err := store.RunMigrations(ctx); err != nil {
		t.Fatalf("second RunMigrations: %v", err)
	}
	if err := store.CreateVectorTables(ctx); err != nil {
		t.Fatalf("second CreateVectorTables: %v", err)
	}
}
