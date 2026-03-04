package todoist

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestSkillMetadata(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())

	if s.Name() != "todoist" {
		t.Errorf("got name %q, want %q", s.Name(), "todoist")
	}

	if caps := s.RequiredCapabilities(); len(caps) != 1 || caps[0] != "todoist" {
		t.Errorf("unexpected capabilities: %v", caps)
	}

	var schema map[string]any
	if err := json.Unmarshal(s.InputSchema(), &schema); err != nil {
		t.Fatalf("invalid schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}

func TestSkillListTasks(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(paginatedResponse[Task]{
			Results: []Task{
				{ID: "1", Content: "Buy milk", Priority: 4, Due: &Due{String: "today", Date: "2099-01-01"}},
				{ID: "2", Content: "Read book", Priority: 1},
			},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "Buy milk") {
		t.Error("result should contain 'Buy milk'")
	}
	if !strings.Contains(result, "[P1]") {
		t.Error("result should contain priority [P1]")
	}
	if !strings.Contains(result, "Read book") {
		t.Error("result should contain 'Read book'")
	}
}

func TestSkillListWithFilter(t *testing.T) {
	var gotPath string
	var gotQuery string
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		json.NewEncoder(w).Encode(paginatedResponse[Task]{Results: []Task{}})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"list","filter":"overdue"}`))
	if err != nil {
		t.Fatal(err)
	}

	// list with filter should use the dedicated /tasks/filter endpoint (API v1).
	if gotPath != "/tasks/filter" {
		t.Errorf("got path %q, want /tasks/filter", gotPath)
	}
	if gotQuery != "overdue" {
		t.Errorf("got query %q, want %q", gotQuery, "overdue")
	}
	if !strings.Contains(result, "No tasks match filter") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillGetTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Task{
			ID:          "1",
			Content:     "Important task",
			Description: "Do this carefully",
			Priority:    4,
			Labels:      []string{"work", "urgent"},
			Due:         &Due{String: "tomorrow", Date: "2099-01-01"},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"get","task_id":"1"}`))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"Important task", "P1", "Do this carefully", "work", "urgent"} {
		if !strings.Contains(result, want) {
			t.Errorf("result should contain %q", want)
		}
	}
}

func TestSkillGetTaskNoID(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"get"}`))
	if err == nil {
		t.Fatal("expected error for missing task_id")
	}
}

func TestSkillCreateTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		var params CreateTaskParams
		json.NewDecoder(r.Body).Decode(&params)
		json.NewEncoder(w).Encode(Task{
			ID:       "new1",
			Content:  params.Content,
			Priority: params.Priority,
			Due:      &Due{String: "tomorrow"},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{
		"action": "create",
		"content": "New task",
		"priority": "P1",
		"due_string": "tomorrow"
	}`))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "Task created") {
		t.Errorf("unexpected result: %s", result)
	}
	if !strings.Contains(result, "new1") {
		t.Error("result should contain task ID")
	}
}

func TestSkillCreateTaskNoContent(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"create"}`))
	if err == nil {
		t.Fatal("expected error for missing content")
	}
}

func TestSkillCompleteTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks/42/close" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(204)
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"complete","task_id":"42"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "completed") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillReopenTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"reopen","task_id":"42"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "reopened") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillDeleteTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("got method %s, want DELETE", r.Method)
		}
		w.WriteHeader(204)
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"delete","task_id":"42"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "deleted") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillProjects(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(paginatedResponse[Project]{
			Results: []Project{{ID: "1", Name: "Inbox", IsInboxProject: true}},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"projects"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Inbox") {
		t.Errorf("unexpected result: %s", result)
	}
	if !strings.Contains(result, "(Inbox)") {
		t.Error("should mark inbox project")
	}
}

func TestSkillLabels(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(paginatedResponse[Label]{
			Results: []Label{{ID: "l1", Name: "urgent"}},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"labels"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "urgent") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillSections(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(paginatedResponse[Section]{
			Results: []Section{{ID: "s1", Name: "Backlog"}},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"sections","project_id":"123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Backlog") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillSectionsNoProject(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"sections"}`))
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
}

func TestSkillComments(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(paginatedResponse[Comment]{
			Results: []Comment{{ID: "c1", Content: "A note", PostedAt: "2026-03-12T10:00:00Z"}},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"comments","task_id":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "A note") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillAddComment(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Comment{ID: "c2", Content: "Done!"})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"add_comment","task_id":"1","content":"Done!"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Comment added") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillUnknownAction(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"fly"}`))
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSkillInvalidJSON(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{invalid}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestManifest(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("manifest is nil")
	}
	if m.Name != "todoist" {
		t.Errorf("got name %q, want %q", m.Name, "todoist")
	}
	if len(m.Capabilities) == 0 || m.Capabilities[0] != "todoist" {
		t.Errorf("unexpected capabilities: %v", m.Capabilities)
	}
	if m.SystemPrompt == "" {
		t.Error("system prompt should not be empty")
	}
}

func TestSkillUpdateTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("got method %s, want POST", r.Method)
		}
		json.NewEncoder(w).Encode(Task{
			ID:      "42",
			Content: "Updated task",
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"update","task_id":"42","content":"Updated task","priority":"P2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Task updated") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillMoveTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks/42/move" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(Task{ID: "42", Content: "Moved", ProjectID: "proj2"})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"move","task_id":"42","target_project_id":"proj2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Task moved") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillMoveTaskNoID(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"move"}`))
	if err == nil {
		t.Fatal("expected error for missing task_id")
	}
}

func TestSkillMoveTaskNoDestination(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"move","task_id":"42"}`))
	if err == nil {
		t.Fatal("expected error for missing destination")
	}
	if !strings.Contains(err.Error(), "requires one of") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSkillQuickAdd(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks/quick" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(Task{
			ID:      "new1",
			Content: "Buy groceries",
			Due:     &Due{String: "tomorrow"},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"quick_add","quick_add_text":"Buy groceries tomorrow p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "quick add") {
		t.Errorf("unexpected result: %s", result)
	}
	if !strings.Contains(result, "Buy groceries") {
		t.Errorf("result should contain task content: %s", result)
	}
}

func TestSkillQuickAddNoText(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"quick_add"}`))
	if err == nil {
		t.Fatal("expected error for missing quick_add_text")
	}
}

func TestSkillCompletedTasks(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(completedTasksResponse{
			Items: []Task{{ID: "1", Content: "Done task"}},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"completed","since":"2026-03-01"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Completed tasks") {
		t.Errorf("unexpected result: %s", result)
	}
	if !strings.Contains(result, "Done task") {
		t.Errorf("result should contain task content: %s", result)
	}
}

func TestSkillCompletedTasksEmpty(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(completedTasksResponse{Items: []Task{}})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"completed"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No completed tasks") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillFilterTasks(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/tasks/filter") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(paginatedResponse[Task]{
			Results: []Task{{ID: "1", Content: "Filtered task", Priority: 1, Due: &Due{String: "today", Date: "2099-01-01"}}},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"filter","filter":"today | overdue"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Filtered task") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillFilterTasksNoFilter(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"filter"}`))
	if err == nil {
		t.Fatal("expected error for missing filter")
	}
}

func TestSkillCompletedByDueDate(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/tasks/completed/by_due_date") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(completedTasksResponse{
			Items: []Task{{ID: "1", Content: "Done task"}},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"completed_by_due_date","since":"2026-03-01"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Done task") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillCreateProject(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/projects" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(Project{ID: "p1", Name: "Work"})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"create_project","project_name":"Work"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Project created") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillCreateProjectNoName(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"create_project"}`))
	if err == nil {
		t.Fatal("expected error for missing project_name")
	}
}

func TestSkillUpdateProject(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Project{ID: "p1", Name: "Updated"})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"update_project","project_id":"p1","project_name":"Updated"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Project updated") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillDeleteProject(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"delete_project","project_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "deleted") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillDeleteProjectNoID(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"delete_project"}`))
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
}

func TestSkillArchiveProject(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects/p1/archive" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(204)
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"archive_project","project_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "archived") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillArchivedProjects(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(paginatedResponse[Project]{Results: []Project{{ID: "p1", Name: "Old"}}})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"archived_projects"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Old") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillProjectCollaborators(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(paginatedResponse[Collaborator]{Results: []Collaborator{{ID: "u1", Name: "Alice", Email: "a@b.com"}}})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"project_collaborators","project_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Alice") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillCreateSection(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Section{ID: "s1", Name: "Backlog"})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"create_section","section_name":"Backlog","project_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Section created") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillCreateSectionNoName(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"create_section","project_id":"p1"}`))
	if err == nil {
		t.Fatal("expected error for missing section_name")
	}
}

func TestSkillUpdateSection(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Section{ID: "s1", Name: "Renamed"})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"update_section","section_id":"s1","section_name":"Renamed"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Section updated") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillDeleteSection(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"delete_section","section_id":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "deleted") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillCreateLabel(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Label{ID: "l1", Name: "important"})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"create_label","label_name":"important"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Label created") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillCreateLabelNoName(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"create_label"}`))
	if err == nil {
		t.Fatal("expected error for missing label_name")
	}
}

func TestSkillUpdateLabel(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Label{ID: "l1", Name: "critical"})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"update_label","label_id":"l1","label_name":"critical"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Label updated") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillDeleteLabel(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"delete_label","label_id":"l1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "deleted") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillSearchLabels(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(paginatedResponse[Label]{Results: []Label{{ID: "l1", Name: "urgent"}}})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"search_labels","query":"urg"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "urgent") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillSearchLabelsNoQuery(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"search_labels"}`))
	if err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestSkillUpdateComment(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Comment{ID: "c1", Content: "Updated"})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"update_comment","comment_id":"c1","comment_content":"Updated"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Comment updated") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillUpdateCommentNoID(t *testing.T) {
	s := NewSkill(NewClient("", nil, zap.NewNop()), zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"update_comment"}`))
	if err == nil {
		t.Fatal("expected error for missing comment_id")
	}
}

func TestSkillDeleteComment(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"delete_comment","comment_id":"c1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "deleted") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSkillIsFavoriteBoolean(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		var params CreateProjectParams
		json.NewDecoder(r.Body).Decode(&params)
		if !params.IsFavorite {
			t.Error("expected is_favorite=true")
		}
		json.NewEncoder(w).Encode(Project{ID: "p1", Name: "Fav", IsFavorite: true})
	})

	s := NewSkill(c, zap.NewNop())
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"create_project","project_name":"Fav","is_favorite":true}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestOverdueDetection(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(paginatedResponse[Task]{
			Results: []Task{{ID: "1", Content: "Overdue task", Priority: 1, Due: &Due{String: "yesterday", Date: "2020-01-01"}}},
		})
	})

	s := NewSkill(c, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "OVERDUE") {
		t.Error("should detect overdue tasks")
	}
}
