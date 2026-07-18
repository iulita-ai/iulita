package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/scheduler"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

// fakeEmbedder returns a fixed-dim vector per input text.
type fakeEmbedder struct{}

func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

func (fakeEmbedder) Dimensions() int { return 3 }

// poisonEmbedder fails on any text containing "POISON" (simulating a recovered
// tokenizer panic surfaced as an error by the ONNX provider), succeeds otherwise.
type poisonEmbedder struct{}

func (poisonEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	for _, t := range texts {
		if strings.Contains(t, "POISON") {
			return nil, errors.New("embedding pipeline panicked: index out of range")
		}
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}
func (poisonEmbedder) Dimensions() int { return 3 }

func TestImportReindexSkipsPoisonMessage(t *testing.T) {
	store := reindexTestStore(t)
	ctx := context.Background()

	// One pathological message plus healthy ones. The pathological one must NOT abort
	// the pass or loop forever — it is skipped with a placeholder so it is not re-fetched.
	msgs := []struct{ uuid, content string }{
		{"good1", "healthy content one"},
		{"bad", "this message triggers a POISON tokenizer crash"},
		{"good2", "healthy content two"},
	}
	for _, m := range msgs {
		if _, err := store.SaveImportedMessage(ctx, &domain.ImportedMessage{
			SourceUUID: m.uuid, ConversationUUID: "c", UserID: "admin", Sender: "human", Content: m.content,
		}); err != nil {
			t.Fatalf("save %s: %v", m.uuid, err)
		}
	}

	h := NewImportReindexHandler(store, poisonEmbedder{}, nil, zap.NewNop())
	payload, _ := json.Marshal(reindexPayload{UserID: "admin", JobID: "reindex:admin"})
	if _, err := h.Handle(scheduler.WithTaskID(ctx, 1), string(payload)); err != nil {
		t.Fatalf("reindex must not fail on a poison message: %v", err)
	}

	// All messages resolved (healthy embedded, poison placeholdered) → none pending.
	pending, _ := store.ImportedMessagesWithoutEmbeddings(ctx, 100)
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending after skip, got %d", len(pending))
	}
	run, _ := store.GetImportRun(ctx, "admin", "reindex:admin")
	if run == nil || run.Status != "done" {
		t.Errorf("expected done run, got %+v", run)
	}
}

func reindexTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err := store.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateVectorTables(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestImportReindexEmbedsPending(t *testing.T) {
	store := reindexTestStore(t)
	ctx := context.Background()

	// Two archived messages with no vectors.
	for _, u := range []string{"m1", "m2"} {
		if _, err := store.SaveImportedMessage(ctx, &domain.ImportedMessage{
			SourceUUID: u, ConversationUUID: "c", UserID: "admin", Sender: "human", Content: "some content " + u,
		}); err != nil {
			t.Fatalf("save %s: %v", u, err)
		}
	}
	pending, _ := store.ImportedMessagesWithoutEmbeddings(ctx, 100)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}

	h := NewImportReindexHandler(store, fakeEmbedder{}, nil, zap.NewNop())
	payload, _ := json.Marshal(reindexPayload{UserID: "admin", JobID: "reindex:admin"})
	if _, err := h.Handle(scheduler.WithTaskID(ctx, 1), string(payload)); err != nil {
		t.Fatalf("reindex Handle: %v", err)
	}

	pending, _ = store.ImportedMessagesWithoutEmbeddings(ctx, 100)
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after reindex, got %d", len(pending))
	}
	run, _ := store.GetImportRun(ctx, "admin", "reindex:admin")
	if run == nil || run.Status != "done" || run.ChunksEmbedded == 0 {
		t.Errorf("expected done reindex run with chunks, got %+v", run)
	}
}

func TestImportReindexRequiresEmbedder(t *testing.T) {
	store := reindexTestStore(t)
	h := NewImportReindexHandler(store, nil, nil, zap.NewNop())
	payload, _ := json.Marshal(reindexPayload{UserID: "admin", JobID: "reindex:admin"})
	if _, err := h.Handle(context.Background(), string(payload)); err == nil {
		t.Fatal("expected error when embeddings are not configured")
	}
}

func TestSweepStagedImports(t *testing.T) {
	store := reindexTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()

	// A zip backed by an active (pending) task must be kept.
	activeZip := filepath.Join(dir, "active.zip")
	os.WriteFile(activeZip, []byte("z"), 0o600)
	pl, _ := json.Marshal(importPayload{ZipPath: activeZip, JobID: "keep"})
	if err := store.CreateTask(ctx, &domain.Task{Type: TaskTypeImportClaudeExport, Payload: string(pl), Status: domain.TaskStatusPending}); err != nil {
		t.Fatal(err)
	}

	// An orphaned zip (no task) and a leftover temp file must be removed.
	orphanZip := filepath.Join(dir, "orphan.zip")
	os.WriteFile(orphanZip, []byte("z"), 0o600)
	tmpFile := filepath.Join(dir, "upload-123.tmp")
	os.WriteFile(tmpFile, []byte("t"), 0o600)

	removed, err := SweepStagedImports(ctx, store, dir, zap.NewNop())
	if err != nil {
		t.Fatalf("SweepStagedImports: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removed (orphan + tmp), got %d", removed)
	}
	if _, err := os.Stat(activeZip); err != nil {
		t.Error("active-task zip must be kept")
	}
	if _, err := os.Stat(orphanZip); !os.IsNotExist(err) {
		t.Error("orphan zip must be removed")
	}
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Error("temp file must be removed")
	}
}
