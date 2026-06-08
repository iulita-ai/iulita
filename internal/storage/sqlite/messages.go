package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/domain"
)

// sanitizeFTS5Query turns arbitrary user input into a safe FTS5 MATCH expression.
// Each whitespace-separated token becomes a quoted prefix term ("tok"*) joined by
// implicit AND. Quoting neutralizes FTS5 operators/punctuation (so natural-language
// queries can't trigger a "SQL logic error"), and the trailing * gives prefix
// matching so "token" also matches "tokens" (FTS5 has no stemming). Returns ""
// when there are no usable tokens.
func sanitizeFTS5Query(q string) string {
	var terms []string
	for _, tok := range strings.Fields(q) {
		tok = strings.ReplaceAll(tok, `"`, "") // drop embedded quotes
		if tok == "" {
			continue
		}
		terms = append(terms, `"`+tok+`"*`)
	}
	return strings.Join(terms, " ")
}

// SearchMessages returns chat-scoped messages matching the query, newest first.
func (s *Store) SearchMessages(ctx context.Context, chatID, query string, limit int) ([]domain.ChatMessage, error) {
	return s.searchMessages(ctx, "chat_id = ?", chatID, query, limit)
}

// SearchMessagesByUser returns user-scoped messages (across channels) matching
// the query, newest first.
func (s *Store) SearchMessagesByUser(ctx context.Context, userID, query string, limit int) ([]domain.ChatMessage, error) {
	return s.searchMessages(ctx, "user_id = ?", userID, query, limit)
}

func (s *Store) searchMessages(ctx context.Context, scopeClause, scopeVal, query string, limit int) ([]domain.ChatMessage, error) {
	match := sanitizeFTS5Query(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200 // defensive cap for any direct caller
	}
	var msgs []domain.ChatMessage
	err := s.db.NewSelect().
		Model(&msgs).
		Where(scopeClause, scopeVal).
		Where("id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)", match).
		OrderExpr("id DESC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("searching messages: %w", err)
	}
	return msgs, nil
}
