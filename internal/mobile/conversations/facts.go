package conversations

import (
	"context"
	"strings"
	"unicode/utf8"
)

// SaveAnswerFact explicitly promotes ordinary owned assistant text to account
// knowledge. Phone results are never stored in this transcript. Call inside
// Operation so the fact insertion and retry journal commit atomically.
func (r *Repository) SaveAnswerFact(ctx context.Context, owner, conversation string, generation uint64, message, text string) (int64, error) {
	if !validID(message) || !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > 16<<10 {
		return 0, ErrInvalid
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	c, err := get(ctx, tx, owner, conversation)
	if err != nil {
		return 0, err
	}
	if c.Generation != generation {
		return 0, ErrConflict
	}
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM mobile_messages WHERE owner_id=? AND conversation_id=? AND generation=? AND id=?`, owner, conversation, generation, message).Scan(&role); err != nil || role != "assistant" {
		return 0, ErrNotFound
	}
	var count int
	if stepError1 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM facts WHERE user_id=?`, owner).Scan(&count); stepError1 != nil {
		return 0, stepError1
	}
	if count >= 10000 {
		return 0, ErrQueueFull
	}
	// The empty source is intentional account/manual input, not a mobile chat ID.
	// Legacy mobile transcript exclusion stays unchanged. FTS triggers still run.
	var id int64
	if stepError2 := tx.QueryRowContext(ctx, `INSERT INTO facts(chat_id,user_id,content,source_type,created_at,last_accessed_at,access_count) VALUES('',?,?,'user',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,0) RETURNING id`, owner, text).Scan(&id); stepError2 != nil {
		return 0, stepError2
	}
	return id, tx.Commit()
}
