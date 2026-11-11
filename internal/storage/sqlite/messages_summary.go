package sqlite

import (
	"context"
	"fmt"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/uptrace/bun"
)

// ReplaceMessagesWithSummary commits deletion and insertion together. Reusing
// the last replaced message ID preserves chronological ID ordering and the
// existing FTS triggers. A failed insert never loses the original conversation.
func (s *Store) ReplaceMessagesWithSummary(ctx context.Context, chatID string, lastOldID int64, summary *domain.ChatMessage) error {
	if summary == nil || summary.ChatID != chatID || summary.Content == "" || lastOldID <= 0 {
		return fmt.Errorf("invalid conversation summary")
	}
	summaryCopy := *summary
	summaryCopy.ID = lastOldID
	return s.runWriteTransaction(ctx, func(ctx context.Context, tx bun.IDB) error {
		if e := conversationAvailable(ctx, tx, chatID); e != nil {
			return e
		}
		if _, err := tx.NewDelete().Model((*domain.ChatMessage)(nil)).Where("chat_id = ?", chatID).Where("id <= ?", lastOldID).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewInsert().Model(&summaryCopy).Exec(ctx); err != nil {
			return err
		}
		*summary = summaryCopy
		return nil
	})
}
