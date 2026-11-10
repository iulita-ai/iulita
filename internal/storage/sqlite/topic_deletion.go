package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/uptrace/bun"
)

func deletionScopeMatches(d *domain.TelegramTopicDeletion, s domain.TelegramTopicDeletionScope) bool {
	return d.BotID == s.BotID && d.ConversationID == s.ConversationID && d.InstanceID == s.InstanceID && d.OwnerID == s.OwnerID && d.ChatID == s.ChatID && d.ThreadID == s.ThreadID
}
func tableExists(ctx context.Context, db bun.IDB, name string) (bool, error) {
	var n int
	e := db.NewRaw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(ctx, &n)
	return n > 0, e
}
func conversationAvailable(ctx context.Context, db bun.IDB, key string) error {
	if key == "" {
		return nil
	}
	for _, table := range []string{"telegram_topic_deletions", "telegram_topic_states"} {
		exists, e := tableExists(ctx, db, table)
		if e != nil {
			return e
		}
		if !exists {
			continue
		}
		var n int
		if readErr := db.NewRaw("SELECT COUNT(*) FROM "+table+" WHERE conversation_id = ? AND status IN ('deleting','deleted')", key).Scan(ctx, &n); readErr != nil {
			return readErr
		}
		if n > 0 {
			return domain.ErrConversationDeleted
		}
	}
	return nil
}

// ConversationUnavailable reports terminal lifecycle state without requiring a route for ordinary chats.
func (s *Store) ConversationUnavailable(ctx context.Context, key string) (bool, error) {
	e := conversationAvailable(ctx, s.db, key)
	if errors.Is(e, domain.ErrConversationDeleted) {
		return true, nil
	}
	return false, e
}
func (s *Store) withConversationWrite(ctx context.Context, key string, fn func(bun.IDB) error) error {
	return s.db.RunInTx(ctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		if e := conversationAvailable(ctx, tx, key); e != nil {
			return e
		}
		return fn(tx)
	})
}

// GetTelegramTopicDeletion loads a durable tombstone, if present.
func (s *Store) GetTelegramTopicDeletion(ctx context.Context, key string) (*domain.TelegramTopicDeletion, error) {
	exists, e := tableExists(ctx, s.db, "telegram_topic_deletions")
	if e != nil || !exists {
		return nil, e
	}
	d := new(domain.TelegramTopicDeletion)
	e = s.db.NewSelect().Model(d).Where("conversation_id = ?", key).Scan(ctx)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return d, e
}

// BeginTelegramTopicDeletion atomically fences an exact private route and cleans its bound local data.
func (s *Store) BeginTelegramTopicDeletion(ctx context.Context, scope domain.TelegramTopicDeletionScope, source string) (out *domain.TelegramTopicDeletion, err error) {
	if scope.ConversationID == "" || scope.InstanceID == "" || scope.OwnerID == "" || scope.ChatID <= 0 || scope.ThreadID <= 0 || scope.BotID <= 0 {
		return nil, fmt.Errorf("invalid private topic deletion scope")
	}
	if e := domain.CheckLegacyChatID(scope.ConversationID); e != nil {
		return nil, e
	}
	err = s.db.RunInTx(ctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		route := new(domain.TelegramConversationRoute)
		if e := tx.NewSelect().Model(route).Where("conversation_id = ? AND instance_id = ? AND owner_id = ? AND chat_id = ? AND thread_id = ? AND private = ? AND bot_id = ?", scope.ConversationID, scope.InstanceID, scope.OwnerID, scope.ChatID, scope.ThreadID, true, scope.BotID).Scan(ctx); e != nil {
			return fmt.Errorf("topic deletion ownership mismatch")
		}
		d := new(domain.TelegramTopicDeletion)
		e := tx.NewSelect().Model(d).Where("conversation_id = ?", scope.ConversationID).Scan(ctx)
		if e == nil {
			if !deletionScopeMatches(d, scope) {
				return fmt.Errorf("topic deletion scope mismatch")
			}
			out = d
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if source == "command" {
			if scope.OperationID == "" || scope.Digest == "" {
				return fmt.Errorf("missing approved deletion intent")
			}
			op := new(domain.TelegramTopicOperation)
			if intentErr := tx.NewSelect().Model(op).Where("id = ? AND digest = ? AND owner_id = ? AND instance_id = ? AND chat_id = ? AND thread_id = ? AND action = 'delete' AND status = 'running'", scope.OperationID, scope.Digest, scope.OwnerID, scope.InstanceID, scope.ChatID, scope.ThreadID).Scan(ctx); intentErr != nil {
				return fmt.Errorf("approved deletion intent unavailable")
			}
		}
		now := time.Now()
		d = &domain.TelegramTopicDeletion{BotID: scope.BotID, ConversationID: scope.ConversationID, InstanceID: scope.InstanceID, OwnerID: scope.OwnerID, ChatID: scope.ChatID, ThreadID: scope.ThreadID, Source: source, Status: "deleting", CreatedAt: now, UpdatedAt: now}
		if _, e = tx.NewInsert().Model(d).Exec(ctx); e != nil {
			return e
		}
		state := &domain.TelegramTopicState{ConversationID: scope.ConversationID, Status: "deleting", UpdatedAt: now}
		if _, e = tx.NewInsert().Model(state).On("CONFLICT (conversation_id) DO UPDATE").Set("status = EXCLUDED.status").Set("name = ''").Set("updated_at = EXCLUDED.updated_at").Exec(ctx); e != nil {
			return e
		}
		if _, deleteErr := tx.NewDelete().Model((*domain.TechFact)(nil)).Where("chat_id = ?", scope.ConversationID).Exec(ctx); deleteErr != nil {
			return deleteErr
		}
		if exists, tableErr := tableExists(ctx, tx, "insight_vectors"); tableErr != nil {
			return tableErr
		} else if exists {
			if _, e = tx.ExecContext(ctx, "DELETE FROM insight_vectors WHERE insight_id IN (SELECT id FROM insights WHERE chat_id = ?)", scope.ConversationID); e != nil {
				return e
			}
		}
		if _, deleteErr := tx.NewDelete().Model((*domain.Insight)(nil)).Where("chat_id = ?", scope.ConversationID).Exec(ctx); deleteErr != nil {
			return deleteErr
		}
		res, e := tx.NewDelete().Model((*domain.ChatMessage)(nil)).Where("chat_id = ?", scope.ConversationID).Exec(ctx)
		if e != nil {
			return e
		}
		n, countErr := res.RowsAffected()
		if countErr != nil {
			return countErr
		}
		d.Messages = int(n)
		d.Reminders, e = tx.NewSelect().Model((*domain.Reminder)(nil)).Where("chat_id = ? AND user_id = ? AND status IN ('pending','firing','delivery_unknown','suppressed')", scope.ConversationID, scope.OwnerID).Count(ctx)
		if e != nil {
			return e
		}
		d.Jobs, e = tx.NewSelect().Model((*domain.AgentJob)(nil)).Where("delivery_chat_id = ? AND user_id = ? AND enabled = ?", scope.ConversationID, scope.OwnerID, true).Count(ctx)
		if e != nil {
			return e
		}
		// Resolve binding through persisted parent IDs, never a fuzzy JSON substring.
		bound := `(type = 'reminder.fire' AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.reminder_id') END IN (SELECT id FROM reminders WHERE chat_id = ? AND user_id = ?)) OR (type = 'agent.job' AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.job_id') END IN (SELECT id FROM agent_jobs WHERE delivery_chat_id = ? AND user_id = ?)) OR (type IN ('techfact.analyze','insight.generate','bookmark.refine','skill.review','heartbeat.check') AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.chat_id') END = ? AND COALESCE(CASE WHEN json_valid(payload) THEN json_extract(payload,'$.user_id') END,'') IN ('',?))`
		args := []any{scope.ConversationID, scope.OwnerID, scope.ConversationID, scope.OwnerID, scope.ConversationID, scope.OwnerID}
		d.Tasks, e = tx.NewSelect().Model((*domain.Task)(nil)).Where("("+bound+")", args...).Where("status IN ('pending','claimed','running')").Count(ctx)
		if e != nil {
			return e
		}
		if _, e = tx.NewUpdate().Model((*domain.Task)(nil)).Set("status = CASE WHEN status IN ('pending','claimed','running') THEN 'cancelled' ELSE status END").Set("payload = '{}' ").Set("result = ''").Set("error = ''").Set("finished_at = ?", now).Where("("+bound+")", args...).Exec(ctx); e != nil {
			return e
		}
		if _, e = tx.NewUpdate().Model((*domain.Reminder)(nil)).Set("status = CASE WHEN status IN ('pending','firing','delivery_unknown','suppressed') THEN 'cancelled' ELSE status END").Set("title = ''").Where("chat_id = ? AND user_id = ?", scope.ConversationID, scope.OwnerID).Exec(ctx); e != nil {
			return e
		}
		if _, e = tx.NewUpdate().Model((*domain.AgentJob)(nil)).Set("enabled = FALSE").Set("prompt = ''").Set("wake_gate_prompt = ''").Set("name = ''").Set("next_run = NULL").Where("delivery_chat_id = ? AND user_id = ?", scope.ConversationID, scope.OwnerID).Exec(ctx); e != nil {
			return e
		}
		if exists, tableErr := tableExists(ctx, tx, "cards"); tableErr != nil {
			return tableErr
		} else if exists {
			if _, e = tx.NewUpdate().Model((*dayview.Card)(nil)).Set("items_json = ''").Set("desired_state = ''").Set("expires = ?", now).Where("instance = ? AND owner = ? AND chat = ? AND thread = ?", scope.InstanceID, scope.OwnerID, scope.ChatID, scope.ThreadID).Exec(ctx); e != nil {
				return e
			}
		}
		if exists, tableErr := tableExists(ctx, tx, "operations"); tableErr != nil {
			return tableErr
		} else if exists {
			var ops []dayview.Operation
			if readErr := tx.NewSelect().Model(&ops).Where("owner = ? AND instance = ? AND conversation = ?", scope.OwnerID, scope.InstanceID, scope.ConversationID).Scan(ctx); readErr != nil {
				return readErr
			}
			for index := range ops {
				op := &ops[index]
				var c dayview.Command
				safe := ""
				if json.Unmarshal([]byte(op.CommandJSON), &c) == nil {
					c.Item.Title = ""
					raw, marshalErr := json.Marshal(c)
					if marshalErr != nil {
						return marshalErr
					}
					safe = string(raw)
				}
				if _, e = tx.NewUpdate().Model((*dayview.Operation)(nil)).Set("command_json = ?", safe).Set("payload_compacted = TRUE").Where("id = ?", op.ID).Exec(ctx); e != nil {
					return e
				}
			}
		}
		if exists, tableErr := tableExists(ctx, tx, "telegram_topic_seeds"); tableErr != nil {
			return tableErr
		} else if exists {
			if _, e = tx.NewUpdate().Model((*domain.TelegramTopicSeed)(nil)).Set("status = CASE WHEN status = 'processing' THEN 'discarded' ELSE status END").Where("instance_id = ? AND owner_id = ? AND chat_id = ? AND thread_id = ?", scope.InstanceID, scope.OwnerID, scope.ChatID, scope.ThreadID).Exec(ctx); e != nil {
				return e
			}
		}
		if _, e = tx.NewUpdate().Model(d).WherePK().Exec(ctx); e != nil {
			return e
		}
		out = d
		return nil
	})
	return
}

// FinalizeTelegramTopicDeletion records confirmed native deletion for the same durable scope.
func (s *Store) FinalizeTelegramTopicDeletion(ctx context.Context, scope domain.TelegramTopicDeletionScope) (out *domain.TelegramTopicDeletion, err error) {
	err = s.db.RunInTx(ctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		d := new(domain.TelegramTopicDeletion)
		if e := tx.NewSelect().Model(d).Where("conversation_id = ?", scope.ConversationID).Scan(ctx); e != nil {
			return e
		}
		if !deletionScopeMatches(d, scope) {
			return fmt.Errorf("topic deletion scope mismatch")
		}
		d.Status = "deleted"
		d.UpdatedAt = time.Now()
		if _, e := tx.NewUpdate().Model(d).WherePK().Exec(ctx); e != nil {
			return e
		}
		if _, e := tx.NewUpdate().Model((*domain.TelegramTopicState)(nil)).Set("status = 'deleted'").Set("name = ''").Where("conversation_id = ?", scope.ConversationID).Exec(ctx); e != nil {
			return e
		}
		out = d
		return nil
	})
	return
}

// ClaimTelegramTopicDeletionNotice claims at most one notice attempt for a finalized deletion.
func (s *Store) ClaimTelegramTopicDeletionNotice(ctx context.Context, scope domain.TelegramTopicDeletionScope) (bool, error) {
	res, e := s.db.NewUpdate().Model((*domain.TelegramTopicDeletion)(nil)).Set("notice_claimed = TRUE").Where("conversation_id = ? AND instance_id = ? AND owner_id = ? AND chat_id = ? AND thread_id = ? AND status = 'deleted' AND notice_claimed = FALSE AND bot_id = ?", scope.ConversationID, scope.InstanceID, scope.OwnerID, scope.ChatID, scope.ThreadID, scope.BotID).Exec(ctx)
	if e != nil {
		return false, e
	}
	n, e := res.RowsAffected()
	return n == 1, e
}

// ListTelegramTopicSyncRoutes returns a bounded keyset of proven routes still needing reconciliation.
func (s *Store) ListTelegramTopicSyncRoutes(ctx context.Context, instance, after string, limit int) ([]domain.TelegramConversationRoute, error) {
	if instance == "" || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid topic sync scope")
	}
	var rows []domain.TelegramConversationRoute
	e := s.db.NewSelect().Model(&rows).Where("instance_id = ? AND private = ? AND thread_id > 0 AND bot_id > 0 AND conversation_id > ?", instance, true, after).Where("conversation_id NOT IN (SELECT conversation_id FROM telegram_topic_deletions WHERE status = 'deleted' AND notice_claimed = TRUE)").Order("conversation_id ASC").Limit(limit).Scan(ctx)
	return rows, e
}
