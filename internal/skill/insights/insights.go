package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// SynthesisRouteHint implements skill.SynthesisModelDeclarer (ListInsightsSkill only).
func (s *ListInsightsSkill) SynthesisRouteHint() string { return llm.RouteHintCheap }

// ListInsightsSkill returns recent insights for the current chat.
type ListInsightsSkill struct {
	store storage.Repository
}

func NewList(store storage.Repository) *ListInsightsSkill {
	return &ListInsightsSkill{store: store}
}

func (s *ListInsightsSkill) Name() string { return "list_insights" }
func (s *ListInsightsSkill) Description() string {
	return "List recent synthesized insights for this chat."
}
func (s *ListInsightsSkill) RequiredCapabilities() []string { return []string{"memory"} }

func (s *ListInsightsSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"limit": {
				"type": "integer",
				"description": "Maximum number of insights to return (default 10)"
			}
		}
	}`)
}

type listInput struct {
	Limit int `json:"limit"`
}

func (s *ListInsightsSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in listInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	chatID := skill.ChatIDFrom(ctx)
	if chatID == "" {
		return "", fmt.Errorf("chat ID not available in context")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}

	insights, err := s.store.GetRecentInsights(ctx, chatID, limit)
	if err != nil {
		return "", fmt.Errorf("listing insights: %w", err)
	}

	if len(insights) == 0 {
		return "No insights available yet.", nil
	}

	var b strings.Builder
	for _, ins := range insights {
		fmt.Fprintf(&b, "#%d [quality:%d, views:%d]: %s\n", ins.ID, ins.Quality, ins.AccessCount, ins.Content)
	}
	return b.String(), nil
}

// DismissInsightSkill deletes an insight by ID.
type DismissInsightSkill struct {
	store storage.Repository
}

func NewDismiss(store storage.Repository) *DismissInsightSkill {
	return &DismissInsightSkill{store: store}
}

func (s *DismissInsightSkill) Name() string                   { return "dismiss_insight" }
func (s *DismissInsightSkill) Description() string            { return "Dismiss (delete) an insight by its ID." }
func (s *DismissInsightSkill) RequiredCapabilities() []string { return []string{"memory"} }

func (s *DismissInsightSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {
				"type": "integer",
				"description": "The insight ID to dismiss"
			}
		},
		"required": ["id"]
	}`)
}

type dismissInput struct {
	ID int64 `json:"id"`
}

func (s *DismissInsightSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in dismissInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	chatID := skill.ChatIDFrom(ctx)
	if chatID == "" {
		return "", fmt.Errorf("chat ID not available in context")
	}

	if err := s.store.DeleteInsight(ctx, in.ID, chatID); err != nil {
		return "", fmt.Errorf("dismissing insight: %w", err)
	}

	return fmt.Sprintf("Insight #%d dismissed.", in.ID), nil
}

// PromoteInsightSkill reinforces an insight (bumps access count).
type PromoteInsightSkill struct {
	store storage.Repository
}

func NewPromote(store storage.Repository) *PromoteInsightSkill {
	return &PromoteInsightSkill{store: store}
}

func (s *PromoteInsightSkill) Name() string { return "promote_insight" }
func (s *PromoteInsightSkill) Description() string {
	return "Promote an insight to increase its relevance and longevity."
}
func (s *PromoteInsightSkill) RequiredCapabilities() []string { return []string{"memory"} }

func (s *PromoteInsightSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {
				"type": "integer",
				"description": "The insight ID to promote"
			}
		},
		"required": ["id"]
	}`)
}

type promoteInput struct {
	ID int64 `json:"id"`
}

func (s *PromoteInsightSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in promoteInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if err := s.store.ReinforceInsight(ctx, in.ID); err != nil {
		return "", fmt.Errorf("promoting insight: %w", err)
	}

	return fmt.Sprintf("Insight #%d promoted.", in.ID), nil
}
