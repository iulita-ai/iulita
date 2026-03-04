package reminders

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// Skill manages one-time reminders (create, list, delete).
type Skill struct {
	store storage.Repository
}

// New creates a new reminders skill.
func New(store storage.Repository) *Skill {
	return &Skill{store: store}
}

func (s *Skill) Name() string { return "reminders" }

func (s *Skill) Description() string {
	return "Create, list, or delete reminders. Use action: create (with title, due_at in RFC3339, timezone), list, or delete (with id)."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["create", "list", "delete"],
				"description": "Operation to perform"
			},
			"title": {
				"type": "string",
				"description": "Reminder text (for create)"
			},
			"due_at": {
				"type": "string",
				"description": "When to remind, RFC3339 format e.g. 2026-03-05T14:00:00 (for create)"
			},
			"timezone": {
				"type": "string",
				"description": "IANA timezone, e.g. Europe/Helsinki. Defaults to UTC."
			},
			"id": {
				"type": "integer",
				"description": "Reminder ID (for delete)"
			}
		},
		"required": ["action"]
	}`)
}

type input struct {
	Action   string `json:"action"`
	Title    string `json:"title"`
	DueAt    string `json:"due_at"`
	Timezone string `json:"timezone"`
	ID       int64  `json:"id"`
}

func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	chatID := skill.ChatIDFrom(ctx)
	userID := skill.UserIDFrom(ctx)
	if chatID == "" {
		return "", fmt.Errorf("chat ID not available in context")
	}

	switch in.Action {
	case "create":
		return s.create(ctx, in, chatID, userID)
	case "list":
		return s.list(ctx, chatID)
	case "delete":
		return s.delete(ctx, in.ID, chatID)
	default:
		return "", fmt.Errorf("unknown action %q, use create/list/delete", in.Action)
	}
}

func (s *Skill) create(ctx context.Context, in input, chatID, userID string) (string, error) {
	if in.Title == "" {
		return "", fmt.Errorf("title is required for create")
	}
	if in.DueAt == "" {
		return "", fmt.Errorf("due_at is required for create")
	}

	tz := in.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return "", fmt.Errorf("unknown timezone %q: %w", tz, err)
	}

	dueAt, err := time.ParseInLocation(time.RFC3339, in.DueAt, loc)
	if err != nil {
		// Try without timezone offset (common from LLMs).
		dueAt, err = time.ParseInLocation("2006-01-02T15:04:05", in.DueAt, loc)
		if err != nil {
			return "", fmt.Errorf("invalid due_at format, use RFC3339: %w", err)
		}
	}

	r := &domain.Reminder{
		ChatID:    chatID,
		UserID:    userID,
		Title:     in.Title,
		DueAt:     dueAt.UTC(),
		Timezone:  tz,
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	if err := s.store.CreateReminder(ctx, r); err != nil {
		return "", err
	}

	dueLocal := r.DueAt.In(loc)
	return fmt.Sprintf("Reminder #%d created: %q at %s (%s)",
		r.ID, r.Title, dueLocal.Format("15:04 02.01.2006"), tz), nil
}

func (s *Skill) list(ctx context.Context, chatID string) (string, error) {
	reminders, err := s.store.ListReminders(ctx, chatID)
	if err != nil {
		return "", err
	}

	if len(reminders) == 0 {
		return "No pending reminders.", nil
	}

	var b strings.Builder
	b.WriteString("Pending reminders:\n")
	for _, r := range reminders {
		loc, err := time.LoadLocation(r.Timezone)
		if err != nil {
			loc = time.UTC
		}
		dueLocal := r.DueAt.In(loc)
		fmt.Fprintf(&b, "#%d — %s — %s (%s)\n",
			r.ID, r.Title, dueLocal.Format("15:04 02.01.2006"), r.Timezone)
	}
	return b.String(), nil
}

func (s *Skill) delete(ctx context.Context, id int64, chatID string) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("id is required for delete")
	}
	if err := s.store.DeleteReminder(ctx, id, chatID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Reminder #%d cancelled.", id), nil
}
