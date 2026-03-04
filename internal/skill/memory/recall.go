package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// RecallSkill searches long-term memory for relevant facts.
type RecallSkill struct {
	store storage.Repository
}

func NewRecall(store storage.Repository) *RecallSkill {
	return &RecallSkill{store: store}
}

func (s *RecallSkill) Name() string { return "recall" }

func (s *RecallSkill) Description() string {
	return "Search long-term memory for remembered facts. Use a keyword or phrase to find relevant information."
}

func (s *RecallSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Search query to find relevant facts"
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of results (default 10)"
			}
		},
		"required": ["query"]
	}`)
}

func (s *RecallSkill) RequiredCapabilities() []string {
	return []string{"memory"}
}

type recallInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (s *RecallSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in recallInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	chatID := skill.ChatIDFrom(ctx)
	if chatID == "" {
		return "", fmt.Errorf("chat ID not available in context")
	}
	userID := skill.UserIDFrom(ctx)

	if in.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}

	// Prefer user-scoped search when user is resolved, fall back to chat-scoped.
	var facts []domain.Fact
	var err error
	if userID != "" {
		facts, err = s.store.SearchFactsByUser(ctx, userID, in.Query, limit)
	}
	if len(facts) == 0 {
		facts, err = s.store.SearchFacts(ctx, chatID, in.Query, limit)
	}
	if err != nil {
		return "", fmt.Errorf("searching facts: %w", err)
	}

	if len(facts) == 0 {
		return "No matching facts found.", nil
	}

	// Reinforce accessed facts.
	for _, f := range facts {
		_ = s.store.ReinforceFact(ctx, f.ID)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d fact(s):\n", len(facts))
	for _, f := range facts {
		fmt.Fprintf(&b, "#%d — %s (accessed %d times)\n", f.ID, f.Content, f.AccessCount+1)
	}
	return b.String(), nil
}
