package craft

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient creates a Client pointing at the given test server.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{
		apiBaseURL: srv.URL,
		apiKey:     "test-key",
		httpClient: srv.Client(),
	}
}

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient("https://example.com/api/v1", "my-key", nil)
	if c.apiBaseURL != "https://example.com/api/v1" {
		t.Errorf("apiBaseURL = %q", c.apiBaseURL)
	}
	if c.apiKey != "my-key" {
		t.Errorf("apiKey = %q", c.apiKey)
	}
	if c.httpClient != http.DefaultClient {
		t.Error("expected default http client")
	}
}

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	c := NewClient("https://example.com/api/v1/", "key", nil)
	if c.apiBaseURL != "https://example.com/api/v1" {
		t.Errorf("apiBaseURL = %q, trailing slash not trimmed", c.apiBaseURL)
	}
}

func TestClient_apiURL(t *testing.T) {
	c := NewClient("https://connect.craft.do/links/abc123/api/v1", "key", nil)
	base, _ := c.getCredentials()
	got := base + "/documents"
	want := "https://connect.craft.do/links/abc123/api/v1/documents"
	if got != want {
		t.Errorf("apiURL() = %q, want %q", got, want)
	}
}

func TestClient_AuthorizationHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-api-key" {
			t.Errorf("Authorization = %q, want %q", auth, "Bearer test-api-key")
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer srv.Close()

	c := &Client{apiBaseURL: srv.URL, apiKey: "test-api-key", httpClient: srv.Client()}
	_, _ = c.SearchDocuments(context.Background(), "test")
}

func TestClient_SearchDocuments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/documents/search") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("include") != "meeting notes" {
			t.Errorf("query = %q, want %q", r.URL.Query().Get("include"), "meeting notes")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]string{
				{"documentId": "doc-1", "markdown": "Weekly **Meeting** notes"},
				{"documentId": "doc-2", "markdown": "Project meeting summary"},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	results, err := c.SearchDocuments(context.Background(), "meeting notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Markdown != "Weekly **Meeting** notes" {
		t.Errorf("markdown = %q", results[0].Markdown)
	}
	if results[0].DocumentID != "doc-1" {
		t.Errorf("id = %q, want %q", results[0].DocumentID, "doc-1")
	}
}

func TestClient_SearchDocuments_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	results, err := c.SearchDocuments(context.Background(), "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestClient_ListDocuments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "d1", "title": "Doc One"},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	docs, err := c.ListDocuments(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Title != "Doc One" {
		t.Errorf("unexpected docs: %+v", docs)
	}
}

func TestClient_ReadDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/markdown" {
			t.Errorf("Accept = %q, want text/markdown", r.Header.Get("Accept"))
		}
		w.Write([]byte("# Hello\n\nWorld"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	content, err := c.ReadDocument(context.Background(), "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Hello") {
		t.Errorf("content = %q, expected to contain 'Hello'", content)
	}
}

func TestClient_CreateDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		docs, ok := body["documents"].([]any)
		if !ok || len(docs) == 0 {
			t.Error("expected documents array")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]string{{"id": "new-doc-1"}},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	id, err := c.CreateDocument(context.Background(), "Test", "# New Doc", "")
	if err != nil {
		t.Fatal(err)
	}
	if id != "new-doc-1" {
		t.Errorf("id = %q, want %q", id, "new-doc-1")
	}
}

func TestClient_AppendToDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["markdown"] != "appended text" {
			t.Errorf("markdown = %q", body["markdown"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.AppendToDocument(context.Background(), "doc-1", "appended text")
	if err != nil {
		t.Fatal(err)
	}
}

func TestClient_GetTasks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope := r.URL.Query().Get("scope")
		if scope != "active" {
			t.Errorf("scope = %q, want active", scope)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "t1", "markdown": "- [ ] Buy milk", "taskInfo": map[string]string{"state": "todo"}},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	tasks, err := c.GetTasks(context.Background(), "active")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Markdown != "- [ ] Buy milk" {
		t.Errorf("unexpected tasks: %+v", tasks)
	}
}

func TestClient_CreateTask(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]string{{"id": "task-new"}},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	id, err := c.CreateTask(context.Background(), "New task", "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	if id != "task-new" {
		t.Errorf("id = %q, want %q", id, "task-new")
	}
}

func TestClient_UpdateTask(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		tasks, ok := body["tasksToUpdate"].([]any)
		if !ok || len(tasks) == 0 {
			t.Error("expected tasksToUpdate array")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	err := c.UpdateTask(context.Background(), "t1", "done")
	if err != nil {
		t.Fatal(err)
	}
}

func TestClient_ListFolders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "f1", "name": "Projects", "documentCount": 5, "folders": []any{}},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	folders, err := c.ListFolders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Name != "Projects" {
		t.Errorf("unexpected folders: %+v", folders)
	}
}

func TestClient_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("access denied"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.SearchDocuments(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for 403 status")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, expected to contain 403", err.Error())
	}
}
