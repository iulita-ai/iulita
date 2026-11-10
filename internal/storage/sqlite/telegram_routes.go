package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/uptrace/bun"
)

// PutTelegramRoute persists immutable bot, owner and native transport coordinates.
func (s *Store) PutTelegramRoute(ctx context.Context, r *domain.TelegramConversationRoute) error {
	if r.ConversationID == "" || r.InstanceID == "" || r.ChatID == 0 || r.ThreadID < 0 || r.OwnerID == "" {
		return errors.New("invalid Telegram route")
	}
	r.UpdatedAt = time.Now()
	// Existing coordinates and owner are immutable. A conflicting instance must
	// mint its own conversation key instead of taking over historical data.
	_, err := s.db.NewInsert().Model(r).On("CONFLICT (conversation_id) DO UPDATE").Set("updated_at = EXCLUDED.updated_at").Where("instance_id = EXCLUDED.instance_id AND chat_id = EXCLUDED.chat_id AND thread_id = EXCLUDED.thread_id AND owner_id = EXCLUDED.owner_id AND bot_id = EXCLUDED.bot_id").Exec(ctx)
	if err != nil {
		return fmt.Errorf("storing Telegram route: %w", err)
	}
	saved, err := s.GetTelegramRoute(ctx, r.ConversationID)
	if err != nil {
		return err
	}
	if saved == nil || saved.InstanceID != r.InstanceID || saved.OwnerID != r.OwnerID || saved.ChatID != r.ChatID || saved.ThreadID != r.ThreadID || saved.BotID != r.BotID {
		return errors.New("telegram route ownership conflict")
	}
	return nil
}

// GetTelegramRoute loads the persisted transport authority for a conversation.
func (s *Store) GetTelegramRoute(ctx context.Context, id string) (*domain.TelegramConversationRoute, error) {
	r := new(domain.TelegramConversationRoute)
	err := s.db.NewSelect().Model(r).Where("conversation_id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// Never infer the owner of legacy routes from a replacement token at startup.
func (s *Store) migrateTelegramBotIdentity(ctx context.Context) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, table := range []string{"telegram_conversation_routes", "telegram_topic_deletions", "telegram_topic_operations"} {
			exists, e := tableExists(ctx, tx, table)
			if e != nil {
				return e
			}
			if !exists {
				continue
			}
			var n int
			if readErr := tx.NewRaw("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'bot_id'", table).Scan(ctx, &n); readErr != nil {
				return readErr
			}
			if n == 0 {
				if _, e = tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN bot_id BIGINT NOT NULL DEFAULT 0"); e != nil {
					return e
				}
			}
		}
		return nil
	})
}
