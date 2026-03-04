package craft

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TasksSkill manages tasks in Craft.
type TasksSkill struct {
	client *Client
}

func NewTasks(client *Client) *TasksSkill {
	return &TasksSkill{client: client}
}

func (s *TasksSkill) Name() string { return "craft_tasks" }

func (s *TasksSkill) Description() string {
	return "List, create, or complete tasks in Craft. Supports scopes: active, upcoming, inbox, logbook."
}

func (s *TasksSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list", "create", "complete"],
				"description": "Action to perform"
			},
			"scope": {
				"type": "string",
				"enum": ["active", "upcoming", "inbox", "logbook"],
				"description": "Task scope for listing (default: active)"
			},
			"content": {
				"type": "string",
				"description": "Task content (for create action)"
			},
			"schedule_date": {
				"type": "string",
				"description": "Schedule date in YYYY-MM-DD format or 'today'/'tomorrow' (for create action)"
			},
			"task_id": {
				"type": "string",
				"description": "Task ID (for complete action)"
			}
		},
		"required": ["action"]
	}`)
}

func (s *TasksSkill) RequiredCapabilities() []string {
	return []string{"craft"}
}

type tasksInput struct {
	Action       string `json:"action"`
	Scope        string `json:"scope"`
	Content      string `json:"content"`
	ScheduleDate string `json:"schedule_date"`
	TaskID       string `json:"task_id"`
}

func (s *TasksSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in tasksInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	switch in.Action {
	case "list":
		return s.list(ctx, in.Scope)
	case "create":
		return s.create(ctx, in.Content, in.ScheduleDate)
	case "complete":
		return s.complete(ctx, in.TaskID)
	default:
		return "", fmt.Errorf("unknown action %q (use: list, create, complete)", in.Action)
	}
}

func (s *TasksSkill) list(ctx context.Context, scope string) (string, error) {
	if scope == "" {
		scope = "active"
	}

	tasks, err := s.client.GetTasks(ctx, scope)
	if err != nil {
		return "", fmt.Errorf("listing tasks: %w", err)
	}

	if len(tasks) == 0 {
		return fmt.Sprintf("No %s tasks.", scope), nil
	}

	var b strings.Builder
	scopeTitle := strings.ToUpper(scope[:1]) + scope[1:]
	fmt.Fprintf(&b, "%s tasks (%d):\n\n", scopeTitle, len(tasks))
	for i, t := range tasks {
		state := "[ ]"
		if t.TaskInfo.State == "done" {
			state = "[x]"
		} else if t.TaskInfo.State == "canceled" {
			state = "[-]"
		}
		fmt.Fprintf(&b, "%d. %s %s", i+1, state, t.Markdown)
		if t.TaskInfo.ScheduleDate != "" {
			fmt.Fprintf(&b, " (scheduled: %s)", t.TaskInfo.ScheduleDate)
		}
		fmt.Fprintf(&b, " [id: %s]\n", t.ID)
	}
	return b.String(), nil
}

func (s *TasksSkill) create(ctx context.Context, content, scheduleDate string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("content is required for create action")
	}

	taskID, err := s.client.CreateTask(ctx, content, scheduleDate)
	if err != nil {
		return "", fmt.Errorf("creating task: %w", err)
	}
	result := fmt.Sprintf("Task created (id: %s): %s", taskID, content)
	if scheduleDate != "" {
		result += fmt.Sprintf(" (scheduled: %s)", scheduleDate)
	}
	return result, nil
}

func (s *TasksSkill) complete(ctx context.Context, taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("task_id is required for complete action")
	}

	if err := s.client.UpdateTask(ctx, taskID, "done"); err != nil {
		return "", fmt.Errorf("completing task: %w", err)
	}
	return fmt.Sprintf("Task %s marked as done.", taskID), nil
}
