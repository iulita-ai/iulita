package dashboard

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func buildImportServer(t *testing.T) (*Server, *sqlite.Store) {
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
	hash, _ := auth.HashPassword("test-pass")
	if err := store.CreateUser(ctx, &domain.User{ID: "admin-1", Username: "admin", Role: "admin", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	authSvc := auth.NewService(store, testJWTSecret, time.Hour, 24*time.Hour)
	srv := New(Config{
		Address:     ":0",
		Store:       store,
		Logger:      zap.NewNop(),
		StaticFS:    fstest.MapFS{"index.html": {Data: []byte("ok")}},
		AuthService: authSvc,
	})
	return srv, store
}

func makeExportZipBytes(t *testing.T, withMemories, withConversations bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if withMemories {
		w, _ := zw.Create("memories.json")
		_, _ = w.Write([]byte(`[{"account_uuid":"a","conversations_memory":"**H**\n\nbody","project_memories":{}}]`))
	}
	if withConversations {
		w, _ := zw.Create("conversations.json")
		_, _ = w.Write([]byte(`[{"uuid":"c1","name":"Chat","account":{"uuid":"a"},"created_at":"2025-01-01T00:00:00Z","chat_messages":[{"uuid":"m1","sender":"human","text":"hi","created_at":"2025-01-01T00:00:01Z"}]}]`))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadExport(t *testing.T, srv *Server, zipBytes []byte, importMemories string) (status int, out map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("export", "export.zip")
	_, _ = fw.Write(zipBytes)
	if importMemories != "" {
		_ = mw.WriteField("import_memories", importMemories)
	}
	mw.Close()

	req := httptest.NewRequest("POST", "/api/import/claude-export", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+testAdminToken(t))
	resp, err := srv.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return resp.StatusCode, result
}

func TestImportUploadQueuesTask(t *testing.T) {
	srv, store := buildImportServer(t)
	ctx := context.Background()

	code, res := uploadExport(t, srv, makeExportZipBytes(t, true, true), "true")
	if code != 202 {
		t.Fatalf("expected 202, got %d (%v)", code, res)
	}
	jobID, _ := res["job_id"].(string)
	if jobID == "" {
		t.Fatal("expected job_id in response")
	}

	// A task was enqueued.
	tasks, err := store.ListTasks(ctx, storage.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	found := false
	for _, tk := range tasks {
		if tk.Type == "import.claude_export" && tk.UniqueKey == "import:"+jobID {
			found = true
			if tk.Capabilities != "import" || tk.MaxAttempts != 1 {
				t.Errorf("task caps/attempts wrong: %+v", tk)
			}
		}
	}
	if !found {
		t.Fatal("import task not enqueued")
	}

	// An ImportRun was recorded.
	run, _ := store.GetImportRun(ctx, "admin-1", jobID)
	if run == nil || run.Status != "running" {
		t.Fatalf("expected running import run, got %+v", run)
	}

	// Re-uploading the same content while queued is deduped.
	code2, res2 := uploadExport(t, srv, makeExportZipBytes(t, true, true), "true")
	if code2 != 200 || res2["status"] != "already_queued" {
		t.Errorf("expected already_queued dedup, got %d (%v)", code2, res2)
	}
}

func TestImportUploadRejectsMissingConversations(t *testing.T) {
	srv, _ := buildImportServer(t)
	code, res := uploadExport(t, srv, makeExportZipBytes(t, true, false), "true")
	if code != 400 {
		t.Fatalf("expected 400 for missing conversations.json, got %d (%v)", code, res)
	}
	if res["expected_files"] == nil {
		t.Error("expected expected_files hint in error response")
	}
}

func TestImportUploadRejectsNonZip(t *testing.T) {
	srv, _ := buildImportServer(t)
	code, _ := uploadExport(t, srv, []byte("this is not a zip"), "true")
	if code != 400 {
		t.Fatalf("expected 400 for non-zip upload, got %d", code)
	}
}

func TestImportUploadRequiresAdmin(t *testing.T) {
	srv, _ := buildImportServer(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("export", "export.zip")
	_, _ = fw.Write(makeExportZipBytes(t, true, true))
	mw.Close()
	// No Authorization header → auth middleware rejects.
	req := httptest.NewRequest("POST", "/api/import/claude-export", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := srv.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

func TestImportStatusAndSearchScoped(t *testing.T) {
	srv, store := buildImportServer(t)
	ctx := context.Background()

	// Seed an archive for the admin and another user.
	_, _ = store.SaveImportedConversation(ctx, &domain.ImportedConversation{SourceUUID: "c1", UserID: "admin-1", Title: "Mine", CreatedAt: time.Now()})
	_, _ = store.SaveImportedMessage(ctx, &domain.ImportedMessage{SourceUUID: "m1", ConversationUUID: "c1", UserID: "admin-1", Sender: "human", Content: "kubernetes secrets rotation"})
	_, _ = store.SaveImportedMessage(ctx, &domain.ImportedMessage{SourceUUID: "m2", ConversationUUID: "c2", UserID: "other", Sender: "human", Content: "kubernetes other user"})
	_ = store.UpsertImportRun(ctx, &domain.ImportRun{JobID: "job-1", UserID: "admin-1", Status: "done"})

	// Status.
	code, body := doImportGet(t, srv, "/api/import/status")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	runs, _ := body.([]any)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run for admin, got %d", len(runs))
	}

	// Search is user-scoped: only the admin's message matches.
	code, body = doImportGet(t, srv, "/api/import/search?q=kubernetes")
	if code != 200 {
		t.Fatalf("search: %d", code)
	}
	obj, _ := body.(map[string]any)
	results, _ := obj["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 scoped search result, got %d", len(results))
	}

	// Conversation messages are scoped: the other user's conversation is invisible.
	code, body = doImportGet(t, srv, "/api/import/conversations/c2")
	if code != 200 {
		t.Fatalf("get messages: %d", code)
	}
	msgs, _ := body.([]any)
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages for another user's conversation, got %d", len(msgs))
	}
}

func TestImportCancelPending(t *testing.T) {
	srv, store := buildImportServer(t)
	ctx := context.Background()

	code, res := uploadExport(t, srv, makeExportZipBytes(t, true, true), "true")
	if code != 202 {
		t.Fatalf("upload: %d", code)
	}
	jobID := res["job_id"].(string)

	// The task is pending (no worker running) → cancelable.
	dc, dres := doImportDelete(t, srv, "/api/import/"+jobID)
	if dc != 200 || dres["status"] != "canceled" {
		t.Fatalf("expected cancel 200, got %d (%v)", dc, dres)
	}
	run, _ := store.GetImportRun(ctx, "admin-1", jobID)
	if run == nil || run.Status != "canceled" {
		t.Errorf("expected canceled run, got %+v", run)
	}
	// Canceling again → 409 (no longer pending).
	dc2, _ := doImportDelete(t, srv, "/api/import/"+jobID)
	if dc2 != 409 {
		t.Errorf("expected 409 on second cancel, got %d", dc2)
	}
}

func TestImportCancelThenReupload(t *testing.T) {
	srv, store := buildImportServer(t)
	ctx := context.Background()
	zipBytes := makeExportZipBytes(t, true, true)

	code, res := uploadExport(t, srv, zipBytes, "true")
	if code != 202 {
		t.Fatalf("first upload: %d", code)
	}
	jobID := res["job_id"].(string)

	if dc, _ := doImportDelete(t, srv, "/api/import/"+jobID); dc != 200 {
		t.Fatalf("cancel: %d", dc)
	}

	// Re-uploading the same export after cancel must enqueue a fresh task (the unique
	// key was freed on cancel), not be silently blocked as "already_queued".
	code2, res2 := uploadExport(t, srv, zipBytes, "true")
	if code2 != 202 || res2["status"] != "queued" {
		t.Fatalf("expected fresh 202 queued after cancel, got %d (%v)", code2, res2)
	}
	tasks, _ := store.ListTasks(ctx, storage.TaskFilter{})
	active := 0
	for _, tk := range tasks {
		if tk.UniqueKey == "import:"+jobID {
			active++
		}
	}
	if active != 1 {
		t.Errorf("expected exactly 1 active task with the key after re-upload, got %d", active)
	}
}

// --- helpers ---

func doImportGet(t *testing.T, srv *Server, path string) (status int, body any) {
	t.Helper()
	req := httptest.NewRequest("GET", path, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+testAdminToken(t))
	resp, err := srv.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func doImportDelete(t *testing.T, srv *Server, path string) (status int, body map[string]any) {
	t.Helper()
	req := httptest.NewRequest("DELETE", path, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+testAdminToken(t))
	resp, err := srv.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}
