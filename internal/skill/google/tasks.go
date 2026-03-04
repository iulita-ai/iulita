package google

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gtasks "google.golang.org/api/tasks/v1"

	"github.com/iulita-ai/iulita/internal/skill"
)

// TasksSkill manages Google Tasks.
type TasksSkill struct {
	client *Client
}

func NewTasks(client *Client) *TasksSkill {
	return &TasksSkill{client: client}
}

func (s *TasksSkill) Name() string { return "google_tasks" }

func (s *TasksSkill) Description() string {
	return "Manage Google Tasks: list task lists, list tasks, create new tasks with due dates, mark tasks complete, or delete tasks."
}

func (s *TasksSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["lists", "list", "create", "complete", "delete"],
				"description": "Action: lists (list all task lists), list (tasks in a list), create, complete, delete"
			},
			"task_list": {
				"type": "string",
				"description": "Task list ID or title (default: @default)"
			},
			"title": {
				"type": "string",
				"description": "Task title for 'create' action"
			},
			"notes": {
				"type": "string",
				"description": "Task description for 'create' action"
			},
			"due": {
				"type": "string",
				"description": "Due date in YYYY-MM-DD format for 'create' action"
			},
			"task_id": {
				"type": "string",
				"description": "Task ID for 'complete' or 'delete' actions"
			},
			"show_completed": {
				"type": "boolean",
				"description": "Include completed tasks in 'list' (default: false)"
			},
			"account": {
				"type": "string",
				"description": "Google account alias or email"
			}
		},
		"required": ["action"]
	}`)
}

func (s *TasksSkill) RequiredCapabilities() []string { return []string{"google"} }

type tasksInput struct {
	Action        string `json:"action"`
	TaskList      string `json:"task_list"`
	Title         string `json:"title"`
	Notes         string `json:"notes"`
	Due           string `json:"due"`
	TaskID        string `json:"task_id"`
	ShowCompleted bool   `json:"show_completed"`
	Account       string `json:"account"`
}

func (s *TasksSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in tasksInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	userID := skill.UserIDFrom(ctx)
	if userID == "" {
		return "", fmt.Errorf("user not identified")
	}

	if !s.client.HasAccounts(ctx, userID) {
		return "No Google account connected. Please connect one in Settings.", nil
	}

	srv, err := s.client.GetTasksService(ctx, userID, in.Account)
	if err != nil {
		return "", fmt.Errorf("creating Tasks service: %w", err)
	}

	listID := in.TaskList
	if listID == "" {
		listID = "@default"
	}

	switch in.Action {
	case "lists":
		return s.listTaskLists(srv)
	case "list":
		return s.listTasks(srv, listID, in.ShowCompleted)
	case "create":
		if in.Title == "" {
			return "", fmt.Errorf("title is required for create action")
		}
		return s.createTask(srv, listID, in.Title, in.Notes, in.Due)
	case "complete":
		if in.TaskID == "" {
			return "", fmt.Errorf("task_id is required for complete action")
		}
		return s.completeTask(srv, listID, in.TaskID)
	case "delete":
		if in.TaskID == "" {
			return "", fmt.Errorf("task_id is required for delete action")
		}
		return s.deleteTask(srv, listID, in.TaskID)
	default:
		return "", fmt.Errorf("unknown action %q", in.Action)
	}
}

func (s *TasksSkill) listTaskLists(srv *gtasks.Service) (string, error) {
	resp, err := srv.Tasklists.List().MaxResults(100).Do()
	if err != nil {
		return "", fmt.Errorf("listing task lists: %w", err)
	}

	if len(resp.Items) == 0 {
		return "No task lists found.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Task lists (%d):\n\n", len(resp.Items))
	for i, tl := range resp.Items {
		fmt.Fprintf(&b, "%d. %s [id: %s]\n", i+1, tl.Title, tl.Id)
	}
	return b.String(), nil
}

func (s *TasksSkill) listTasks(srv *gtasks.Service, listID string, showCompleted bool) (string, error) {
	call := srv.Tasks.List(listID).MaxResults(100)
	if showCompleted {
		call = call.ShowCompleted(true).ShowHidden(true)
	}

	resp, err := call.Do()
	if err != nil {
		return "", fmt.Errorf("listing tasks: %w", err)
	}

	if len(resp.Items) == 0 {
		return "No tasks found.", nil
	}

	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "Tasks (%d):\n\n", len(resp.Items))

	for i, t := range resp.Items {
		status := "[ ]"
		if t.Status == "completed" {
			status = "[x]"
		}

		overdue := ""
		if t.Due != "" && t.Status != "completed" {
			due, err := time.Parse(time.RFC3339, t.Due)
			if err == nil && due.Before(now) {
				overdue = " **OVERDUE**"
			}
		}

		fmt.Fprintf(&b, "%d. %s %s%s\n", i+1, status, t.Title, overdue)
		if t.Notes != "" {
			notes := t.Notes
			if len(notes) > 200 {
				notes = notes[:200] + "..."
			}
			fmt.Fprintf(&b, "   Notes: %s\n", notes)
		}
		if t.Due != "" {
			due, err := time.Parse(time.RFC3339, t.Due)
			if err == nil {
				fmt.Fprintf(&b, "   Due: %s\n", due.Format("2006-01-02"))
			}
		}
		fmt.Fprintf(&b, "   [id: %s]\n", t.Id)
	}
	return b.String(), nil
}

func (s *TasksSkill) createTask(srv *gtasks.Service, listID, title, notes, due string) (string, error) {
	task := &gtasks.Task{
		Title:  title,
		Notes:  notes,
		Status: "needsAction",
	}

	if due != "" {
		// Parse YYYY-MM-DD and convert to RFC3339.
		d, err := time.Parse("2006-01-02", due)
		if err != nil {
			return "", fmt.Errorf("invalid due date format (use YYYY-MM-DD): %w", err)
		}
		task.Due = d.UTC().Format(time.RFC3339)
	}

	created, err := srv.Tasks.Insert(listID, task).Do()
	if err != nil {
		return "", fmt.Errorf("creating task: %w", err)
	}

	result := fmt.Sprintf("Task created: %s [id: %s]", created.Title, created.Id)
	if due != "" {
		result += fmt.Sprintf(" (due: %s)", due)
	}
	return result, nil
}

func (s *TasksSkill) completeTask(srv *gtasks.Service, listID, taskID string) (string, error) {
	task, err := srv.Tasks.Get(listID, taskID).Do()
	if err != nil {
		return "", fmt.Errorf("getting task: %w", err)
	}

	task.Status = "completed"
	_, err = srv.Tasks.Update(listID, taskID, task).Do()
	if err != nil {
		return "", fmt.Errorf("completing task: %w", err)
	}

	return fmt.Sprintf("Task %q marked as completed.", task.Title), nil
}

func (s *TasksSkill) deleteTask(srv *gtasks.Service, listID, taskID string) (string, error) {
	err := srv.Tasks.Delete(listID, taskID).Do()
	if err != nil {
		return "", fmt.Errorf("deleting task: %w", err)
	}
	return fmt.Sprintf("Task %s deleted.", taskID), nil
}
