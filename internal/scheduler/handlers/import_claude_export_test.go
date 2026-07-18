package handlers

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/scheduler"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func newImportTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err := store.RunMigrations(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if err := store.CreateVectorTables(ctx); err != nil {
		t.Fatalf("vector tables: %v", err)
	}
	return store
}

// writeExportZip builds a minimal Claude export zip on disk and returns its path.
func writeExportZip(t *testing.T, memories, conversations string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "export.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range map[string]string{
		"memories.json":      memories,
		"conversations.json": conversations,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return path
}

const testMemories = `[{
	"account_uuid":"acc-1",
	"conversations_memory":"**Work context**\n\nLeads teams.\n\n**Personal context**\n\nLoves photography.",
	"project_memories":{"proj-1":"A single project blob."}
}]`

const testConversations = `[
	{"uuid":"c1","name":"Deploy chat","summary":"","account":{"uuid":"acc-1"},
	 "created_at":"2025-01-27T19:23:47.398804Z","updated_at":"2025-01-27T19:30:00.000000Z",
	 "chat_messages":[
		{"uuid":"m1","sender":"human","text":"How do I deploy?","created_at":"2025-01-27T19:23:50.000000Z"},
		{"uuid":"m2","sender":"assistant","text":"Use docker compose.","created_at":"2025-01-27T19:24:00.000000Z","parent_message_uuid":"m1"},
		{"uuid":"m3","sender":"human","text":"   ","created_at":"2025-01-27T19:25:00.000000Z"}
	 ]},
	{"uuid":"c2","name":"","summary":"","account":{"uuid":"acc-1"},
	 "created_at":"2025-02-01T10:00:00.000000Z",
	 "chat_messages":[
		{"uuid":"m4","sender":"human","text":"Second conversation.","created_at":"2025-02-01T10:00:05.000000Z"}
	 ]}
]`

func runImport(t *testing.T, store *sqlite.Store, jobID, zipPath string) {
	t.Helper()
	h := NewImportClaudeExportHandler(store, nil, nil, zap.NewNop())
	payload, _ := json.Marshal(importPayload{
		ZipPath: zipPath, UserID: "admin", JobID: jobID, SourceSHA: "sha-" + jobID, ImportMemories: true,
	})
	// Provide a task ID so the heartbeat path is exercised (no reclaim in a fast test).
	ctx := scheduler.WithTaskID(context.Background(), 1)
	if _, err := h.Handle(ctx, string(payload)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestImportHandlerHappyPath(t *testing.T) {
	store := newImportTestStore(t)
	ctx := context.Background()
	zipPath := writeExportZip(t, testMemories, testConversations)

	runImport(t, store, "job-1", zipPath)

	// Staged zip is removed on completion.
	if _, err := os.Stat(zipPath); !os.IsNotExist(err) {
		t.Errorf("expected staged zip removed, stat err = %v", err)
	}

	// Memories → facts: 2 conv sections + 1 single project blob = 3.
	facts, err := store.GetAllFacts(ctx, "claude-import")
	if err != nil {
		t.Fatalf("GetAllFacts: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("expected 3 imported facts, got %d", len(facts))
	}
	for _, f := range facts {
		if f.SourceType != "claude_import" || f.UserID != "admin" {
			t.Errorf("fact metadata wrong: %+v", f)
		}
	}

	// Conversations → archive: 2 conversations.
	convs, err := store.ListImportedConversations(ctx, "admin", 100, 0)
	if err != nil {
		t.Fatalf("ListImportedConversations: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("expected 2 archived conversations, got %d", len(convs))
	}

	// c1 has 3 messages, one empty → 2 stored; MessageCount reflects stored.
	msgs, err := store.GetImportedConversationMessages(ctx, "admin", "c1")
	if err != nil {
		t.Fatalf("GetImportedConversationMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 stored messages for c1, got %d", len(msgs))
	}
	if msgs[0].SourceUUID != "m1" || msgs[1].SourceUUID != "m2" {
		t.Errorf("message order wrong: %s, %s", msgs[0].SourceUUID, msgs[1].SourceUUID)
	}

	// Durable run summary.
	run, err := store.GetImportRun(ctx, "admin", "job-1")
	if err != nil {
		t.Fatalf("GetImportRun: %v", err)
	}
	if run == nil || run.Status != "done" {
		t.Fatalf("expected done run, got %+v", run)
	}
	if run.Conversations != 2 || run.MessagesStored != 3 || run.Facts != 3 || run.MessagesSkipped != 1 {
		t.Errorf("run counters wrong: %+v", run)
	}
}

func TestImportHandlerIdempotentReimport(t *testing.T) {
	store := newImportTestStore(t)
	ctx := context.Background()

	runImport(t, store, "job-1", writeExportZip(t, testMemories, testConversations))
	// Second import of the same data (fresh zip, new job id) must add zero rows.
	runImport(t, store, "job-2", writeExportZip(t, testMemories, testConversations))

	facts, _ := store.GetAllFacts(ctx, "claude-import")
	if len(facts) != 3 {
		t.Errorf("re-import created duplicate facts: got %d, want 3", len(facts))
	}
	convs, _ := store.ListImportedConversations(ctx, "admin", 100, 0)
	if len(convs) != 2 {
		t.Errorf("re-import created duplicate conversations: got %d, want 2", len(convs))
	}
	msgs, _ := store.GetImportedConversationMessages(ctx, "admin", "c1")
	if len(msgs) != 2 {
		t.Errorf("re-import created duplicate messages: got %d, want 2", len(msgs))
	}

	// The second run's summary reflects zero new facts (all keys already present).
	run, _ := store.GetImportRun(ctx, "admin", "job-2")
	if run == nil || run.Facts != 0 {
		t.Errorf("expected 0 new facts on re-import, got %+v", run)
	}
}

func TestImportHandlerMemoriesOptOut(t *testing.T) {
	store := newImportTestStore(t)
	ctx := context.Background()
	h := NewImportClaudeExportHandler(store, nil, nil, zap.NewNop())
	payload, _ := json.Marshal(importPayload{
		ZipPath: writeExportZip(t, testMemories, testConversations),
		UserID:  "admin", JobID: "job-1", ImportMemories: false,
	})
	if _, err := h.Handle(scheduler.WithTaskID(ctx, 1), string(payload)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	facts, _ := store.GetAllFacts(ctx, "claude-import")
	if len(facts) != 0 {
		t.Errorf("ImportMemories=false must skip facts, got %d", len(facts))
	}
	convs, _ := store.ListImportedConversations(ctx, "admin", 100, 0)
	if len(convs) != 2 {
		t.Errorf("conversations should still import, got %d", len(convs))
	}
}

// TestImportHandlerRealDump is an opt-in end-to-end run against a real Claude export
// zip. It is skipped unless IULITA_CLAUDE_DUMP_ZIP points at one, so no personal data
// lives in the repo. The zip is copied first because the handler deletes its staged
// file on completion. Verifies a full import and a zero-delta re-import.
func TestImportHandlerRealDump(t *testing.T) {
	src := os.Getenv("IULITA_CLAUDE_DUMP_ZIP")
	if src == "" {
		t.Skip("set IULITA_CLAUDE_DUMP_ZIP to run against a real export zip")
	}
	store := newImportTestStore(t)
	ctx := context.Background()

	copyZip := func() string {
		dst := filepath.Join(t.TempDir(), "export.zip")
		in, err := os.Open(src)
		if err != nil {
			t.Fatalf("open source zip: %v", err)
		}
		defer in.Close()
		out, err := os.Create(dst)
		if err != nil {
			t.Fatalf("create copy: %v", err)
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			t.Fatalf("copy zip: %v", err)
		}
		out.Close()
		return dst
	}

	runImport(t, store, "real-1", copyZip())
	run, _ := store.GetImportRun(ctx, "admin", "real-1")
	if run == nil || run.Status != "done" {
		t.Fatalf("expected done run, got %+v", run)
	}
	t.Logf("real import: convs=%d msgs=%d facts=%d skipped=%d binaries=%d parseErrs=%d",
		run.Conversations, run.MessagesStored, run.Facts, run.MessagesSkipped, run.SkippedBinaries, run.ParseErrors)
	if run.Facts != 76 {
		t.Errorf("expected 76 memory facts, got %d", run.Facts)
	}
	if run.Conversations == 0 || run.MessagesStored == 0 {
		t.Errorf("expected non-empty archive, got convs=%d msgs=%d", run.Conversations, run.MessagesStored)
	}

	factsAfterFirst, _ := store.GetAllFacts(ctx, "claude-import")

	// Re-import: zero delta.
	runImport(t, store, "real-2", copyZip())
	run2, _ := store.GetImportRun(ctx, "admin", "real-2")
	if run2.Facts != 0 {
		t.Errorf("re-import added %d facts, want 0", run2.Facts)
	}
	factsAfterSecond, _ := store.GetAllFacts(ctx, "claude-import")
	if len(factsAfterFirst) != len(factsAfterSecond) {
		t.Errorf("re-import changed fact count: %d → %d", len(factsAfterFirst), len(factsAfterSecond))
	}
}

func TestImportHandlerMissingConversations(t *testing.T) {
	store := newImportTestStore(t)
	// Zip with an empty conversations member removed is simulated by an invalid member set.
	path := filepath.Join(t.TempDir(), "bad.zip")
	f, _ := os.Create(path)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("memories.json")
	_, _ = w.Write([]byte(testMemories))
	zw.Close()
	f.Close()

	h := NewImportClaudeExportHandler(store, nil, nil, zap.NewNop())
	payload, _ := json.Marshal(importPayload{ZipPath: path, UserID: "admin", JobID: "job-x", ImportMemories: true})
	if _, err := h.Handle(scheduler.WithTaskID(context.Background(), 1), string(payload)); err == nil {
		t.Fatal("expected error when conversations.json is missing")
	}
	// Failed run is recorded.
	run, _ := store.GetImportRun(context.Background(), "admin", "job-x")
	if run == nil || run.Status != "failed" {
		t.Errorf("expected failed run recorded, got %+v", run)
	}
	// The staged zip must survive a failure so the worker's retry / a re-claim can
	// resume — deleting it on failure would guarantee ENOENT on every retry.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("staged zip must be kept on failure for retry/resume, stat err = %v", err)
	}
}
