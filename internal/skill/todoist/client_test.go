package todoist

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func testServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient("test-token", srv.Client(), zap.NewNop())
	c.baseURL = srv.URL
	return c, srv
}

func TestAuthorization(t *testing.T) {
	var gotAuth string
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte(`{"results":[]}`))
	})

	c.GetProjects(context.Background())
	if gotAuth != "Bearer test-token" {
		t.Errorf("got auth %q, want %q", gotAuth, "Bearer test-token")
	}
}

func TestGetProjects(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(paginatedResponse[Project]{
			Results: []Project{
				{ID: "1", Name: "Inbox", IsInboxProject: true},
				{ID: "2", Name: "Work"},
			},
		})
	})

	projects, err := c.GetProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	if projects[0].Name != "Inbox" {
		t.Errorf("got name %q, want %q", projects[0].Name, "Inbox")
	}
}

func TestGetTasks(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(paginatedResponse[Task]{
			Results: []Task{{ID: "101", Content: "Buy milk", Priority: 1}},
		})
	})

	tasks, err := c.GetTasks(context.Background(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Content != "Buy milk" {
		t.Errorf("got content %q, want %q", tasks[0].Content, "Buy milk")
	}
}

func TestGetTasksByProject(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if pid := r.URL.Query().Get("project_id"); pid != "123" {
			t.Errorf("got project_id %q, want %q", pid, "123")
		}
		json.NewEncoder(w).Encode(paginatedResponse[Task]{Results: []Task{}})
	})

	_, err := c.GetTasks(context.Background(), "123", "", "")
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("got method %s, want POST", r.Method)
		}
		if r.URL.Path != "/tasks" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("got content-type %q, want %q", ct, "application/json")
		}

		var params CreateTaskParams
		json.NewDecoder(r.Body).Decode(&params)
		if params.Content != "Test task" {
			t.Errorf("got content %q, want %q", params.Content, "Test task")
		}
		if params.Priority != 4 {
			t.Errorf("got priority %d, want 4", params.Priority)
		}

		json.NewEncoder(w).Encode(Task{
			ID:       "201",
			Content:  params.Content,
			Priority: params.Priority,
			Due:      &Due{String: "tomorrow", Date: "2026-03-13"},
		})
	})

	task, err := c.CreateTask(context.Background(), CreateTaskParams{
		Content:   "Test task",
		Priority:  4,
		DueString: "tomorrow",
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "201" {
		t.Errorf("got id %q, want %q", task.ID, "201")
	}
}

func TestCloseTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("got method %s, want POST", r.Method)
		}
		if r.URL.Path != "/tasks/301/close" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(204)
	})

	if err := c.CloseTask(context.Background(), "301"); err != nil {
		t.Fatal(err)
	}
}

func TestReopenTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks/301/reopen" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(204)
	})

	if err := c.ReopenTask(context.Background(), "301"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("got method %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/tasks/301" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(204)
	})

	if err := c.DeleteTask(context.Background(), "301"); err != nil {
		t.Fatal(err)
	}
}

func TestGetComments(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("task_id") != "101" {
			t.Errorf("got task_id %q, want %q", r.URL.Query().Get("task_id"), "101")
		}
		json.NewEncoder(w).Encode(paginatedResponse[Comment]{
			Results: []Comment{{ID: "c1", Content: "Note", PostedAt: "2026-03-12T10:00:00Z"}},
		})
	})

	comments, err := c.GetComments(context.Background(), "101")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("got %d comments, want 1", len(comments))
	}
}

func TestGetSections(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project_id") != "123" {
			t.Errorf("got project_id %q, want %q", r.URL.Query().Get("project_id"), "123")
		}
		json.NewEncoder(w).Encode(paginatedResponse[Section]{
			Results: []Section{{ID: "s1", Name: "Backlog", ProjectID: "123"}},
		})
	})

	sections, err := c.GetSections(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1", len(sections))
	}
}

func TestGetLabels(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(paginatedResponse[Label]{
			Results: []Label{{ID: "l1", Name: "urgent"}},
		})
	})

	labels, err := c.GetLabels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 1 {
		t.Fatalf("got %d labels, want 1", len(labels))
	}
}

func TestUpdateTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("got method %s, want POST", r.Method)
		}
		if r.URL.Path != "/tasks/42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var params UpdateTaskParams
		json.NewDecoder(r.Body).Decode(&params)
		if params.Content != "Updated" {
			t.Errorf("got content %q, want %q", params.Content, "Updated")
		}
		json.NewEncoder(w).Encode(Task{ID: "42", Content: "Updated"})
	})

	task, err := c.UpdateTask(context.Background(), "42", UpdateTaskParams{Content: "Updated"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Content != "Updated" {
		t.Errorf("got content %q, want %q", task.Content, "Updated")
	}
}

func TestMoveTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("got method %s, want POST", r.Method)
		}
		if r.URL.Path != "/tasks/101/move" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var params MoveTaskParams
		json.NewDecoder(r.Body).Decode(&params)
		if params.ProjectID != "proj2" {
			t.Errorf("got project_id %q, want %q", params.ProjectID, "proj2")
		}
		json.NewEncoder(w).Encode(Task{ID: "101", Content: "Moved task", ProjectID: "proj2"})
	})

	task, err := c.MoveTask(context.Background(), "101", MoveTaskParams{ProjectID: "proj2"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ProjectID != "proj2" {
		t.Errorf("got project_id %q, want %q", task.ProjectID, "proj2")
	}
}

func TestQuickAddTask(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("got method %s, want POST", r.Method)
		}
		if r.URL.Path != "/tasks/quick" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["text"] != "Buy groceries tomorrow p1" {
			t.Errorf("got text %q", body["text"])
		}
		json.NewEncoder(w).Encode(Task{
			ID:       "301",
			Content:  "Buy groceries",
			Priority: 4,
			Due:      &Due{String: "tomorrow", Date: "2026-03-13"},
		})
	})

	task, err := c.QuickAddTask(context.Background(), "Buy groceries tomorrow p1")
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "301" {
		t.Errorf("got id %q, want %q", task.ID, "301")
	}
	if task.Priority != 4 {
		t.Errorf("got priority %d, want 4", task.Priority)
	}
}

func TestGetCompletedTasks(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("got method %s, want GET", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/tasks/completed/by_completion_date") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if s := r.URL.Query().Get("since"); s != "2026-03-01" {
			t.Errorf("got since %q, want %q", s, "2026-03-01")
		}
		json.NewEncoder(w).Encode(completedTasksResponse{
			Items: []Task{{ID: "201", Content: "Done task", IsCompleted: true}},
		})
	})

	tasks, err := c.GetCompletedTasks(context.Background(), "2026-03-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Content != "Done task" {
		t.Errorf("got content %q, want %q", tasks[0].Content, "Done task")
	}
}

func TestCreateProject(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/projects" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var params CreateProjectParams
		json.NewDecoder(r.Body).Decode(&params)
		if params.Name != "Work" {
			t.Errorf("got name %q, want %q", params.Name, "Work")
		}
		json.NewEncoder(w).Encode(Project{ID: "p1", Name: "Work"})
	})

	project, err := c.CreateProject(context.Background(), CreateProjectParams{Name: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "Work" {
		t.Errorf("got name %q, want %q", project.Name, "Work")
	}
}

func TestDeleteProject(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/projects/p1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	})

	if err := c.DeleteProject(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveProject(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/projects/p1/archive" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	})

	if err := c.ArchiveProject(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
}

func TestGetArchivedProjects(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects/archived" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(paginatedResponse[Project]{Results: []Project{{ID: "p1", Name: "Old Project"}}})
	})

	projects, err := c.GetArchivedProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "Old Project" {
		t.Errorf("unexpected projects: %v", projects)
	}
}

func TestGetProjectCollaborators(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects/p1/collaborators" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(paginatedResponse[Collaborator]{Results: []Collaborator{{ID: "u1", Name: "Alice", Email: "alice@example.com"}}})
	})

	collabs, err := c.GetProjectCollaborators(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(collabs) != 1 || collabs[0].Name != "Alice" {
		t.Errorf("unexpected collaborators: %v", collabs)
	}
}

func TestGetTasksByFilter(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/tasks/filter") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if q := r.URL.Query().Get("query"); q != "today | overdue" {
			t.Errorf("got query %q", q)
		}
		json.NewEncoder(w).Encode(paginatedResponse[Task]{
			Results: []Task{{ID: "1", Content: "Filtered task"}},
		})
	})

	tasks, err := c.GetTasksByFilter(context.Background(), "today | overdue")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
}

func TestGetCompletedTasksByDueDate(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/tasks/completed/by_due_date") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(completedTasksResponse{
			Items: []Task{{ID: "1", Content: "Done"}},
		})
	})

	tasks, err := c.GetCompletedTasksByDueDate(context.Background(), "2026-03-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
}

func TestCreateSection(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sections" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(Section{ID: "s1", Name: "Backlog", ProjectID: "p1"})
	})

	section, err := c.CreateSection(context.Background(), CreateSectionParams{Name: "Backlog", ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if section.Name != "Backlog" {
		t.Errorf("got name %q", section.Name)
	}
}

func TestDeleteSection(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/sections/s1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	})

	if err := c.DeleteSection(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateLabel(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/labels" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(Label{ID: "l1", Name: "urgent"})
	})

	label, err := c.CreateLabel(context.Background(), CreateLabelParams{Name: "urgent"})
	if err != nil {
		t.Fatal(err)
	}
	if label.Name != "urgent" {
		t.Errorf("got name %q", label.Name)
	}
}

func TestDeleteLabel(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/labels/l1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	})

	if err := c.DeleteLabel(context.Background(), "l1"); err != nil {
		t.Fatal(err)
	}
}

func TestSearchLabels(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/labels/search") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if q := r.URL.Query().Get("query"); q != "urg" {
			t.Errorf("got query %q", q)
		}
		json.NewEncoder(w).Encode(paginatedResponse[Label]{Results: []Label{{ID: "l1", Name: "urgent"}}})
	})

	labels, err := c.SearchLabels(context.Background(), "urg")
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 1 {
		t.Fatalf("got %d labels, want 1", len(labels))
	}
}

func TestUpdateComment(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/comments/c1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(Comment{ID: "c1", Content: "Updated note"})
	})

	comment, err := c.UpdateComment(context.Background(), "c1", "Updated note")
	if err != nil {
		t.Fatal(err)
	}
	if comment.Content != "Updated note" {
		t.Errorf("got content %q", comment.Content)
	}
}

func TestDeleteComment(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/comments/c1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	})

	if err := c.DeleteComment(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
}

func TestErrorStatus(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"Forbidden"}`))
	})

	_, err := c.GetProjects(context.Background())
	if err == nil {
		t.Fatal("expected error for 403 status")
	}
	if got := err.Error(); !strings.Contains(got, "403") {
		t.Errorf("error should contain status code: %s", got)
	}
}

func TestUpdateToken(t *testing.T) {
	c := NewClient("old-token", nil, zap.NewNop())
	c.UpdateToken("new-token")
	if got := c.getToken(); got != "new-token" {
		t.Errorf("got token %q, want %q", got, "new-token")
	}
}

func TestParsePriority(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"P1", 4},
		{"P2", 3},
		{"P3", 2},
		{"P4", 1},
		{"1", 4},
		{"p1", 4},
		{"", 1},
	}
	for _, tt := range tests {
		if got := ParsePriority(tt.input); got != tt.want {
			t.Errorf("ParsePriority(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestPriorityLabel(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{4, "P1"},
		{3, "P2"},
		{2, "P3"},
		{1, "P4"},
	}
	for _, tt := range tests {
		if got := priorityLabel(tt.input); got != tt.want {
			t.Errorf("priorityLabel(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
