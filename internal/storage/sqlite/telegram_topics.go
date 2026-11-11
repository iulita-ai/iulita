package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/uptrace/bun"
)

// EnsureTelegramTopics creates and upgrades topic state, identity and receipt tables.
func (s *Store) EnsureTelegramTopics(ctx context.Context) error {
	for _, model := range []any{(*domain.TelegramTopicState)(nil), (*domain.TelegramTopicOperation)(nil), (*domain.TelegramTopicSeed)(nil), (*domain.TelegramTopicDeletion)(nil)} {
		if _, e := s.db.NewCreateTable().Model(model).IfNotExists().Exec(ctx); e != nil {
			return e
		}
	}
	if e := s.migrateTelegramBotIdentity(ctx); e != nil {
		return e
	}
	if _, err := s.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS telegram_topic_operations_digest ON telegram_topic_operations(digest, status)"); err != nil {
		return err
	}
	for _, statement := range []string{
		"CREATE INDEX IF NOT EXISTS telegram_topic_seeds_album ON telegram_topic_seeds(instance_id, owner_id, chat_id, album_id, message_id)",
		"CREATE INDEX IF NOT EXISTS telegram_topic_seeds_finish ON telegram_topic_seeds(instance_id, owner_id, chat_id, thread_id, copy_id, status)",
		"DELETE FROM telegram_topic_seeds WHERE id IN (SELECT id FROM telegram_topic_seeds WHERE status IN ('processed','cleared','discarded') AND updated_at < datetime('now', '-30 days') LIMIT 100)",
	} {
		if _, e := s.db.ExecContext(ctx, statement); e != nil {
			return e
		}
	}
	// Preserve uncertain authority indefinitely; completed records no longer
	// need payload after 30 days (well beyond Telegram update retention).
	_, err := s.db.ExecContext(ctx, "DELETE FROM telegram_topic_operations WHERE id IN (SELECT id FROM telegram_topic_operations WHERE status = ? AND updated_at < ? LIMIT 100)", "succeeded", time.Now().Add(-30*24*time.Hour))
	return err
}

// GetTelegramTopicState loads the local conversation lifecycle state.
func (s *Store) GetTelegramTopicState(ctx context.Context, id string) (*domain.TelegramTopicState, error) {
	v := new(domain.TelegramTopicState)
	e := s.db.NewSelect().Model(v).Where("conversation_id = ?", id).Scan(ctx)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return v, e
}

// PutTelegramTopicState updates local metadata without reopening a terminal conversation.
func (s *Store) PutTelegramTopicState(ctx context.Context, v *domain.TelegramTopicState) error {
	v.UpdatedAt = time.Now()
	if v.Status == "deleted" || v.Status == "deleting" {
		_, e := s.db.NewInsert().Model(v).On("CONFLICT (conversation_id) DO UPDATE").Set("name = EXCLUDED.name").Set("status = EXCLUDED.status").Set("updated_at = EXCLUDED.updated_at").Where("status <> 'deleted' OR EXCLUDED.status = 'deleted'").Exec(ctx)
		return e
	}
	return s.withConversationWrite(ctx, v.ConversationID, func(db bun.IDB) error {
		old := new(domain.TelegramTopicState)
		e := db.NewSelect().Model(old).Where("conversation_id = ?", v.ConversationID).Scan(ctx)
		if e == nil && (old.Status == "deleted" || old.Status == "deleting") {
			return domain.ErrConversationDeleted
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		_, e = db.NewInsert().Model(v).On("CONFLICT (conversation_id) DO UPDATE").Set("name = EXCLUDED.name").Set("status = EXCLUDED.status").Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
		return e
	})
}

// ClaimTelegramTopicOperation durably claims an intent without retrying an unresolved native operation.
func (s *Store) ClaimTelegramTopicOperation(ctx context.Context, op *domain.TelegramTopicOperation) (*domain.TelegramTopicOperation, bool, error) {
	// Serialize unresolved logical create commands in SQLite, including after a
	// process crash. No worker retries a Telegram create automatically.
	var result *domain.TelegramTopicOperation
	claimed := false
	err := s.runWriteTransaction(ctx, func(ctx context.Context, tx bun.IDB) error {
		prior := new(domain.TelegramTopicOperation)
		e := tx.NewSelect().Model(prior).Where("id = ? OR (digest = ? AND status IN (?, ?, ?))", op.ID, op.Digest, "running", "unknown_outcome", "created").Order("updated_at DESC").Limit(1).Scan(ctx)
		if e == nil {
			result = prior
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		op.Status = "running"
		op.UpdatedAt = time.Now()
		if _, e = tx.NewInsert().Model(op).Exec(ctx); e != nil {
			return e
		}
		result, claimed = op, true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return result, claimed, nil
}

// SaveTelegramTopicOperation persists the outcome of an already claimed native operation.
func (s *Store) SaveTelegramTopicOperation(ctx context.Context, op *domain.TelegramTopicOperation) error {
	op.UpdatedAt = time.Now()
	_, e := s.db.NewUpdate().Model(op).WherePK().Exec(ctx)
	return e
}

// GetTelegramTopicOperation reads a receipt only for its owning user and bot instance.
func (s *Store) GetTelegramTopicOperation(ctx context.Context, id, owner, instance string) (*domain.TelegramTopicOperation, error) {
	op := new(domain.TelegramTopicOperation)
	e := s.db.NewSelect().Model(op).Where("id = ? AND owner_id = ? AND instance_id = ?", id, owner, instance).Scan(ctx)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return op, e
}

// ApplyLocalTelegramTopicOperation is a local application gate, not a Telegram
// forum close/reopen. State and durable result commit together, without any API call.
func (s *Store) ApplyLocalTelegramTopicOperation(ctx context.Context, op *domain.TelegramTopicOperation, state *domain.TelegramTopicState) error {
	err := s.runWriteTransaction(ctx, func(ctx context.Context, tx bun.IDB) error {
		if op.ThreadID <= 0 {
			return errors.New("local pause requires an actual topic")
		}
		if (op.Action != "pause" || state.Status != "paused") && (op.Action != "resume" || state.Status != "open") {
			return errors.New("invalid local topic transition")
		}
		route := new(domain.TelegramConversationRoute)
		if e := tx.NewSelect().Model(route).Where("conversation_id = ? AND owner_id = ? AND instance_id = ? AND chat_id = ? AND thread_id = ? AND private = ?", state.ConversationID, op.OwnerID, op.InstanceID, op.ChatID, op.ThreadID, true).Scan(ctx); e != nil {
			return errors.New("local topic route ownership mismatch")
		}
		prior := new(domain.TelegramTopicState)
		e := tx.NewSelect().Model(prior).Where("conversation_id = ?", state.ConversationID).Scan(ctx)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e == nil && prior.Status == "deleted" {
			return errors.New("deleted topic cannot be resumed")
		}
		if availabilityErr := conversationAvailable(ctx, tx, state.ConversationID); availabilityErr != nil {
			return availabilityErr
		}
		state.UpdatedAt = time.Now()
		_, e = tx.NewInsert().Model(state).On("CONFLICT (conversation_id) DO UPDATE").Set("name = EXCLUDED.name").Set("status = EXCLUDED.status").Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
		if e != nil {
			return e
		}
		result, e := tx.NewUpdate().Model((*domain.TelegramTopicOperation)(nil)).Set("status = ?", "succeeded").Set("updated_at = ?", time.Now()).Where("id = ? AND owner_id = ? AND instance_id = ? AND chat_id = ? AND thread_id = ? AND action = ? AND digest = ? AND status = ?", op.ID, op.OwnerID, op.InstanceID, op.ChatID, op.ThreadID, op.Action, op.Digest, "running").Exec(ctx)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return errors.New("local topic operation no longer claimable")
		}
		return nil
	})
	if err != nil {
		return err
	}
	op.Status = "succeeded"
	return nil
}
