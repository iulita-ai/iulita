package craft

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func craftTestServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{
		apiBaseURL: srv.URL,
		apiKey:     "test-key",
		httpClient: srv.Client(),
	}
}

// --- SearchSkill tests ---

func TestSearchSkill_Name(t *testing.T) {
	s := NewSearch(nil)
	if s.Name() != "craft_search" {
		t.Errorf("Name() = %q", s.Name())
	}
}

func TestSearchSkill_RequiredCapabilities(t *testing.T) {
	s := NewSearch(nil)
	caps := s.RequiredCapabilities()
	if len(caps) != 1 || caps[0] != "craft" {
		t.Errorf("caps = %v", caps)
	}
}

func TestSearchSkill_Execute(t *testing.T) {
	c := craftTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]string{
				{"documentId": "d1", "markdown": "important **notes**"},
			},
		})
	})

	s := NewSearch(c)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"query": "notes"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "notes") {
		t.Errorf("result = %q, expected to contain 'notes'", result)
	}
	if !strings.Contains(result, "d1") {
		t.Errorf("result = %q, expected to contain doc ID", result)
	}
}

func TestSearchSkill_EmptyQuery(t *testing.T) {
	s := NewSearch(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{"query": ""}`))
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestSearchSkill_InvalidJSON(t *testing.T) {
	s := NewSearch(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// --- ReadSkill tests ---

func TestReadSkill_Name(t *testing.T) {
	s := NewRead(nil)
	if s.Name() != "craft_read" {
		t.Errorf("Name() = %q", s.Name())
	}
}

func TestReadSkill_ReadDocument(t *testing.T) {
	c := craftTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("# Document Title\n\nContent here"))
	})

	s := NewRead(c)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"document_id": "doc-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "<<<CRAFT_DOCUMENT>>>") {
		t.Error("expected CRAFT_DOCUMENT boundary markers")
	}
	if !strings.Contains(result, "Document Title") {
		t.Errorf("result = %q", result)
	}
}

func TestReadSkill_ListFolders(t *testing.T) {
	c := craftTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "f1", "name": "Work", "documentCount": 3, "folders": []any{}},
			},
		})
	})

	s := NewRead(c)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"list_folders": true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Work") {
		t.Errorf("result = %q", result)
	}
}

func TestReadSkill_ListDocuments(t *testing.T) {
	c := craftTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "d1", "title": "My Doc"},
			},
		})
	})

	s := NewRead(c)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"list_documents": "folder-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "My Doc") {
		t.Errorf("result = %q", result)
	}
}

// --- WriteSkill tests ---

func TestWriteSkill_Name(t *testing.T) {
	s := NewWrite(nil)
	if s.Name() != "craft_write" {
		t.Errorf("Name() = %q", s.Name())
	}
}

func TestWriteSkill_CreateDocument(t *testing.T) {
	c := craftTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]string{{"id": "new-1"}},
		})
	})

	s := NewWrite(c)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"content": "# Hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "new-1") {
		t.Errorf("result = %q, expected doc ID", result)
	}
	if !strings.Contains(result, "created") {
		t.Errorf("result = %q, expected 'created'", result)
	}
}

func TestWriteSkill_AppendDocument(t *testing.T) {
	c := craftTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	s := NewWrite(c)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"content": "More text", "document_id": "doc-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "appended") {
		t.Errorf("result = %q, expected 'appended'", result)
	}
}

func TestWriteSkill_EmptyContent(t *testing.T) {
	s := NewWrite(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{"content": ""}`))
	if err == nil {
		t.Error("expected error for empty content")
	}
}

// --- TasksSkill tests ---

func TestTasksSkill_Name(t *testing.T) {
	s := NewTasks(nil)
	if s.Name() != "craft_tasks" {
		t.Errorf("Name() = %q", s.Name())
	}
}

func TestTasksSkill_List(t *testing.T) {
	c := craftTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "t1", "markdown": "- [ ] Buy groceries", "taskInfo": map[string]string{"state": "todo"}},
				{"id": "t2", "markdown": "- [x] Call dentist", "taskInfo": map[string]string{"state": "done"}},
			},
		})
	})

	s := NewTasks(c)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action": "list"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Buy groceries") {
		t.Errorf("result missing task content: %q", result)
	}
	if !strings.Contains(result, "[ ]") {
		t.Errorf("result missing todo marker: %q", result)
	}
	if !strings.Contains(result, "[x]") {
		t.Errorf("result missing done marker: %q", result)
	}
}

func TestTasksSkill_Create(t *testing.T) {
	c := craftTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]string{{"id": "new-task"}},
		})
	})

	s := NewTasks(c)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action": "create", "content": "Do laundry", "schedule_date": "2026-03-10"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "new-task") {
		t.Errorf("result = %q, expected task ID", result)
	}
	if !strings.Contains(result, "scheduled") {
		t.Errorf("result = %q, expected 'scheduled'", result)
	}
}

func TestTasksSkill_Complete(t *testing.T) {
	c := craftTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	s := NewTasks(c)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action": "complete", "task_id": "t1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "done") {
		t.Errorf("result = %q, expected 'done'", result)
	}
}

func TestTasksSkill_UnknownAction(t *testing.T) {
	s := NewTasks(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action": "delete"}`))
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestTasksSkill_CreateEmptyContent(t *testing.T) {
	s := NewTasks(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action": "create", "content": ""}`))
	if err == nil {
		t.Error("expected error for empty content")
	}
}

func TestTasksSkill_CompleteNoID(t *testing.T) {
	s := NewTasks(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action": "complete"}`))
	if err == nil {
		t.Error("expected error for missing task_id")
	}
}

// --- Manifest test ---

func TestLoadManifest(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("manifest is nil")
	}
	if m.Name != "craft" {
		t.Errorf("Name = %q, want craft", m.Name)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0] != "craft" {
		t.Errorf("Capabilities = %v", m.Capabilities)
	}
	if m.SystemPrompt == "" {
		t.Error("SystemPrompt is empty")
	}
}

// --- InputSchema validation ---

func TestSkills_InputSchema_ValidJSON(t *testing.T) {
	skills := []struct {
		name   string
		schema json.RawMessage
	}{
		{"search", NewSearch(nil).InputSchema()},
		{"read", NewRead(nil).InputSchema()},
		{"write", NewWrite(nil).InputSchema()},
		{"tasks", NewTasks(nil).InputSchema()},
	}

	for _, s := range skills {
		t.Run(s.name, func(t *testing.T) {
			var v map[string]any
			if err := json.Unmarshal(s.schema, &v); err != nil {
				t.Errorf("invalid JSON schema: %v", err)
			}
			if v["type"] != "object" {
				t.Errorf("schema type = %v, want object", v["type"])
			}
		})
	}
}
