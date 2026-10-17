package memory

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// ForgetSkill deletes facts from long-term memory.
type ForgetSkill struct {
	store storage.Repository
}

func NewForget(store storage.Repository) *ForgetSkill {
	return &ForgetSkill{store: store}
}

func (s *ForgetSkill) Name() string { return "forget" }

func (s *ForgetSkill) Description() string {
	return "Delete facts from long-term memory by ID or search query."
}

func (s *ForgetSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {
				"type": "integer",
				"description": "Specific fact ID to delete"
			},
			"query": {
				"type": "string",
				"description": "Search query to find and delete matching facts"
			}
		}
	}`)
}

func (s *ForgetSkill) RequiredCapabilities() []string {
	return []string{"memory"}
}

type forgetInput struct {
	ID    int64  `json:"id"`
	Query string `json:"query"`
}

func (s *ForgetSkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in forgetInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	chatID := skill.ChatIDFrom(ctx)
	if chatID == "" {
		return "", fmt.Errorf("chat ID not available in context")
	}
	// Account memory is shared across sources, including manually saved native
	// facts whose source chat is intentionally empty. Never delete by global ID
	// or fall back to another chat after resolving an account owner.
	if owner := skill.UserIDFrom(ctx); owner != "" {
		if accountStore, ok := s.store.(storage.AccountFacts); ok {
			user, lookupErr := s.store.GetUser(ctx, owner)
			if lookupErr != nil {
				return "", lookupErr
			}
			if user != nil {
				if in.ID > 0 {
					deleted, deleteErr := accountStore.DeleteAccountFact(ctx, owner, in.ID)
					if deleteErr != nil {
						return "", deleteErr
					}
					if !deleted {
						return "No matching account fact found.", nil
					}
					return fmt.Sprintf("Account fact #%d deleted.", in.ID), nil
				}
				if in.Query != "" {
					count, deleteErr := accountStore.DeleteAccountFactsByQuery(ctx, owner, in.Query)
					if deleteErr != nil {
						return "", deleteErr
					}
					return fmt.Sprintf("Deleted %d matching account fact(s).", count), nil
				}
				return "", fmt.Errorf("provide either id or query to identify facts to delete")
			}
		}
	}

	if in.ID > 0 {
		if err := s.store.DeleteFact(ctx, in.ID, chatID); err != nil {
			return "", err
		}
		return fmt.Sprintf("Fact #%d deleted.", in.ID), nil
	}

	if in.Query != "" {
		count, err := s.store.DeleteFactsByQuery(ctx, chatID, in.Query)
		if err != nil {
			return "", err
		}
		if count == 0 {
			return "No matching facts found to delete.", nil
		}
		return fmt.Sprintf("Deleted %d matching fact(s).", count), nil
	}

	return "", fmt.Errorf("provide either id or query to identify facts to delete")
}
