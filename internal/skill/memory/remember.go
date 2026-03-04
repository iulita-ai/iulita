package memory

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

// RememberSkill saves facts to long-term memory.
type RememberSkill struct {
	store storage.Repository
}

func NewRemember(store storage.Repository) *RememberSkill {
	return &RememberSkill{store: store}
}

func (s *RememberSkill) Name() string { return "remember" }

func (s *RememberSkill) Description() string {
	return "Save a fact or piece of information to long-term memory for this chat. You MUST call this tool whenever the user asks you to remember, save, note, or not forget something. Also use proactively for user preferences, important details, or anything worth recalling later. Never just acknowledge a memory request in text — always call this tool first."
}

func (s *RememberSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {
				"type": "string",
				"description": "The fact or information to remember"
			}
		},
		"required": ["content"]
	}`)
}

func (s *RememberSkill) RequiredCapabilities() []string {
	return []string{"memory"}
}

type rememberInput struct {
	Content string `json:"content"`
}

func (s *RememberSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in rememberInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	chatID := skill.ChatIDFrom(ctx)
	if chatID == "" {
		return "", fmt.Errorf("chat ID not available in context")
	}
	userID := skill.UserIDFrom(ctx)

	if in.Content == "" {
		return "", fmt.Errorf("content is required")
	}

	// Check for duplicates via FTS search — prefer user-scoped.
	words := strings.Fields(in.Content)
	if len(words) > 3 {
		words = words[:3]
	}
	query := strings.Join(words, " ")
	var existing []domain.Fact
	if userID != "" {
		existing, _ = s.store.SearchFactsByUser(ctx, userID, query, 5)
	}
	if len(existing) == 0 {
		existing, _ = s.store.SearchFacts(ctx, chatID, query, 5)
	}
	for _, f := range existing {
		if strings.EqualFold(strings.TrimSpace(f.Content), strings.TrimSpace(in.Content)) {
			return fmt.Sprintf("Already remembered (fact #%d): %s", f.ID, f.Content), nil
		}
	}

	fact := &domain.Fact{
		ChatID:         chatID,
		UserID:         userID,
		Content:        in.Content,
		SourceType:     "user",
		CreatedAt:      time.Now(),
		LastAccessedAt: time.Now(),
		AccessCount:    0,
	}

	if err := s.store.SaveFact(ctx, fact); err != nil {
		return "", fmt.Errorf("saving fact: %w", err)
	}

	return fmt.Sprintf("Remembered (fact #%d): %s", fact.ID, fact.Content), nil
}
