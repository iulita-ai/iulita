// Package sessionsearch provides a skill for full-text search over the user's
// past chat messages across sessions (backed by the messages_fts table).
package sessionsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// snippetMax bounds how much of each matched message is shown.
const snippetMax = 240

// Skill searches the user's chat history full-text.
type Skill struct {
	store storage.Repository
}

// New constructs the session_search skill.
func New(store storage.Repository) *Skill { return &Skill{store: store} }

// SynthesisRouteHint runs the post-search synthesis on the cheap model.
func (s *Skill) SynthesisRouteHint() string { return llm.RouteHintCheap }

// Name returns the tool name.
func (s *Skill) Name() string { return "session_search" }

// Description returns the tool description shown to the LLM.
func (s *Skill) Description() string {
	return "Search the user's past conversation history (across all sessions) for earlier messages. " +
		"Use when the user refers to something discussed before."
}

// RequiredCapabilities gates the skill behind the memory capability.
func (s *Skill) RequiredCapabilities() []string { return []string{"memory"} }

// InputSchema returns the JSON Schema for the tool input.
func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Keywords or phrase to find in past messages"
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of matches (default 10)"
			}
		},
		"required": ["query"]
	}`)
}

type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// Execute searches the user's (or chat's) past messages for the query.
func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in searchInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return "", fmt.Errorf("query is required")
	}

	chatID := skill.ChatIDFrom(ctx)
	userID := skill.UserIDFrom(ctx)
	if userID == "" && chatID == "" {
		return "", fmt.Errorf("no user or chat context available")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}

	// Prefer cross-session (user-scoped) search; fall back to the current chat
	// when no user is resolved (single-user installs, older data).
	var msgs []domain.ChatMessage
	var err error
	if userID != "" {
		msgs, err = s.store.SearchMessagesByUser(ctx, userID, in.Query, limit)
	}
	if len(msgs) == 0 && chatID != "" {
		msgs, err = s.store.SearchMessages(ctx, chatID, in.Query, limit)
	}
	if err != nil {
		return "", fmt.Errorf("searching messages: %w", err)
	}
	if len(msgs) == 0 {
		return "No matching messages found in past conversations.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d past message(s):\n", len(msgs))
	for _, m := range msgs {
		fmt.Fprintf(&b, "[%s · %s] %s\n",
			string(m.Role), m.CreatedAt.Format("2006-01-02 15:04"), snippet(m.Content))
	}
	return b.String(), nil
}

// snippet collapses whitespace and truncates a message for display.
func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > snippetMax {
		return s[:snippetMax] + "…"
	}
	return s
}
