package directives

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// Skill manages user-specific directives (custom instructions for the assistant).
type Skill struct {
	store storage.Repository
}

func New(store storage.Repository) *Skill {
	return &Skill{store: store}
}

func (s *Skill) Name() string { return "directives" }

func (s *Skill) Description() string {
	return "Set, get, or clear custom instructions (directives) that guide assistant behavior for this chat. Use action: set (with content), get, or clear."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["set", "get", "clear"],
				"description": "Operation to perform"
			},
			"content": {
				"type": "string",
				"description": "Directive content (for set)"
			}
		},
		"required": ["action"]
	}`)
}

type input struct {
	Action  string `json:"action"`
	Content string `json:"content"`
}

func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	chatID := skill.ChatIDFrom(ctx)
	if chatID == "" {
		return "", fmt.Errorf("chat ID not available in context")
	}

	switch in.Action {
	case "set":
		if in.Content == "" {
			return "", fmt.Errorf("content is required for set")
		}
		d := &domain.Directive{
			ChatID:    chatID,
			Content:   in.Content,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := s.store.SaveDirective(ctx, d); err != nil {
			return "", err
		}
		return "Directive saved. It will be applied to all future messages in this chat.", nil

	case "get":
		d, err := s.store.GetDirective(ctx, chatID)
		if err != nil {
			return "", err
		}
		if d == nil {
			return "No directive set for this chat.", nil
		}
		return fmt.Sprintf("Current directive:\n%s", d.Content), nil

	case "clear":
		if err := s.store.DeleteDirective(ctx, chatID); err != nil {
			return "", err
		}
		return "Directive cleared.", nil

	default:
		return "", fmt.Errorf("unknown action %q, use set/get/clear", in.Action)
	}
}
