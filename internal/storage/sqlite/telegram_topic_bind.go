package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/uptrace/bun"
)

// BindLegacyTelegramTopic is a one-way, explicitly approved provenance repair.
// Observing a new token or receiving a message never authorizes this method.
func (s *Store) BindLegacyTelegramTopic(ctx context.Context, scope domain.TelegramTopicDeletionScope) error {
	if scope.BotID <= 0 || scope.ChatID <= 0 || scope.ThreadID <= 0 || scope.ConversationID == "" || scope.InstanceID == "" || scope.OwnerID == "" || scope.OperationID == "" || scope.Digest == "" {
		return fmt.Errorf("invalid legacy topic binding scope")
	}
	return s.runWriteTransaction(ctx, func(ctx context.Context, tx bun.IDB) error {
		if e := conversationAvailable(ctx, tx, scope.ConversationID); e != nil {
			return e
		}
		route := new(domain.TelegramConversationRoute)
		if e := tx.NewSelect().Model(route).Where("conversation_id = ? AND instance_id = ? AND owner_id = ? AND chat_id = ? AND thread_id = ? AND private = TRUE", scope.ConversationID, scope.InstanceID, scope.OwnerID, scope.ChatID, scope.ThreadID).Scan(ctx); e != nil {
			return fmt.Errorf("legacy topic binding ownership mismatch")
		}
		op := new(domain.TelegramTopicOperation)
		if e := tx.NewSelect().Model(op).Where("id = ? AND digest = ? AND owner_id = ? AND instance_id = ? AND chat_id = ? AND thread_id = ? AND action = 'bind' AND bot_id = ?", scope.OperationID, scope.Digest, scope.OwnerID, scope.InstanceID, scope.ChatID, scope.ThreadID, scope.BotID).Scan(ctx); e != nil {
			return fmt.Errorf("approved legacy binding unavailable")
		}
		if route.BotID == scope.BotID && op.Status == "succeeded" {
			return nil
		}
		if route.BotID != 0 || op.Status != "running" {
			return fmt.Errorf("legacy binding no longer available")
		}
		res, e := tx.NewUpdate().Model((*domain.TelegramConversationRoute)(nil)).Set("bot_id = ?", scope.BotID).Set("updated_at = ?", time.Now()).Where("conversation_id = ? AND bot_id = 0", scope.ConversationID).Exec(ctx)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return fmt.Errorf("legacy identity changed")
		}
		res, e = tx.NewUpdate().Model((*domain.TelegramTopicOperation)(nil)).Set("status = 'succeeded'").Set("updated_at = ?", time.Now()).Where("id = ? AND status = 'running'", scope.OperationID).Exec(ctx)
		if e != nil {
			return e
		}
		n, e = res.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return fmt.Errorf("legacy binding approval changed")
		}
		return nil
	})
}
