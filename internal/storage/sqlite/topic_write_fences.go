package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/uptrace/bun"
)

var errScheduledCommandChanged = errors.New("scheduled command changed")

func taskConversation(ctx context.Context, db bun.IDB, t *domain.Task) (string, error) {
	if !conversationTaskType(t.Type) {
		return "", nil
	}
	// Legacy callers may create a payload-free execution receipt as metadata.
	// Admission below is deliberately stricter before any handler dispatch.
	if strings.TrimSpace(t.Payload) == "" {
		return "", nil
	}
	var p struct {
		ChatID         string `json:"chat_id"`
		DeliveryChatID string `json:"delivery_chat_id"`
		UserID         string `json:"user_id"`
		Prompt         string `json:"prompt"`
		JobID          int64  `json:"job_id"`
		ReminderID     int64  `json:"reminder_id"`
	}
	if decodeErr := json.Unmarshal([]byte(t.Payload), &p); decodeErr != nil {
		return "", fmt.Errorf("invalid scheduled task payload: %w", decodeErr)
	}
	if t.Type == "agent.job" && p.JobID > 0 {
		j := new(domain.AgentJob)
		if e := db.NewSelect().Model(j).Where("id = ?", p.JobID).Scan(ctx); e != nil {
			return "", e
		}
		if !j.Enabled || j.UserID != p.UserID || j.DeliveryChatID != p.DeliveryChatID || j.Prompt != p.Prompt {
			return "", errScheduledCommandChanged
		}
		return j.DeliveryChatID, nil
	}
	if t.Type == "reminder.fire" && p.ReminderID > 0 {
		r := new(domain.Reminder)
		if e := db.NewSelect().Model(r).Where("id = ?", p.ReminderID).Scan(ctx); e != nil {
			return "", e
		}
		if r.ChatID != p.ChatID || (r.Status != "pending" && r.Status != "firing") {
			return "", errScheduledCommandChanged
		}
		return r.ChatID, nil
	}

	if p.ChatID != "" {
		return p.ChatID, nil
	}
	return p.DeliveryChatID, nil
}

// TaskStillRunnable is used before dispatch and by cancellation monitoring.
func (s *Store) TaskStillRunnable(ctx context.Context, id int64) (bool, error) {
	t := new(domain.Task)
	if e := s.db.NewSelect().Model(t).Where("id = ?", id).Scan(ctx); e != nil {
		return false, e
	}
	if t.Status != domain.TaskStatusRunning && t.Status != domain.TaskStatusClaimed {
		return false, nil
	}
	// The terminal reminder status may already have changed during a normal send.
	// Cancellation authority is the durable task status plus the conversation tombstone.
	var payload struct {
		ChatID         string `json:"chat_id"`
		DeliveryChatID string `json:"delivery_chat_id"`
	}
	if !conversationTaskType(t.Type) {
		return true, nil
	}
	// Bun may materialize an absent payload as the column's default {}.
	// Neither an absent payload nor an empty object grants execution authority.
	var authority map[string]json.RawMessage
	if decodeErr := json.Unmarshal([]byte(t.Payload), &authority); decodeErr != nil || len(authority) == 0 {
		return false, s.cancelUnavailableTask(ctx, id)
	}
	if decodeErr := json.Unmarshal([]byte(t.Payload), &payload); decodeErr != nil {
		return false, s.cancelUnavailableTask(ctx, id)
	}
	key := payload.ChatID
	if key == "" {
		key = payload.DeliveryChatID
	}
	var e error
	if t.Type == "agent.job" {
		key, e = taskConversation(ctx, s.db, t)
	}
	if e != nil {
		if errors.Is(e, errScheduledCommandChanged) || errors.Is(e, sql.ErrNoRows) {
			return false, s.cancelUnavailableTask(ctx, id)
		}
		return false, e
	}
	if e = conversationAvailable(ctx, s.db, key); e != nil {
		if errors.Is(e, domain.ErrConversationDeleted) {
			return false, s.cancelUnavailableTask(ctx, id)
		}
		return false, e
	}
	return true, nil
}

func (s *Store) insertTaskFenced(ctx context.Context, t *domain.Task) error {
	return s.runWriteTransaction(ctx, func(ctx context.Context, tx bun.IDB) error {
		key, e := taskConversation(ctx, tx, t)
		if e != nil {
			return e
		}
		if availabilityErr := conversationAvailable(ctx, tx, key); availabilityErr != nil {
			return availabilityErr
		}
		_, e = tx.NewInsert().Model(t).Exec(ctx)
		return e
	})
}

func (s *Store) withTelegramCoordinatesWrite(ctx context.Context, instance, owner string, chat int64, thread int, fn func(bun.IDB) error) error {
	return s.runWriteTransaction(ctx, func(ctx context.Context, tx bun.IDB) error {
		exists, e := tableExists(ctx, tx, "telegram_conversation_routes")
		if e != nil {
			return e
		}
		if exists {
			r := new(domain.TelegramConversationRoute)
			e = tx.NewSelect().Model(r).Where("instance_id = ? AND owner_id = ? AND chat_id = ? AND thread_id = ?", instance, owner, chat, thread).Scan(ctx)
			if e == nil {
				if availabilityErr := conversationAvailable(ctx, tx, r.ConversationID); availabilityErr != nil {
					return availabilityErr
				}
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		return fn(tx)
	})
}

// ValidateAgentJob checks current job authority and conversation availability before execution.
func (s *Store) ValidateAgentJob(ctx context.Context, id int64, owner, prompt, delivery string) (bool, error) {
	t := &domain.Task{Type: "agent.job"}
	raw, marshalErr := json.Marshal(map[string]any{"job_id": id, "user_id": owner, "prompt": prompt, "delivery_chat_id": delivery})
	if marshalErr != nil {
		return false, marshalErr
	}
	t.Payload = string(raw)
	key, e := taskConversation(ctx, s.db, t)
	if e != nil {
		return false, e
	}
	if availabilityErr := conversationAvailable(ctx, s.db, key); availabilityErr != nil {
		return false, availabilityErr
	}
	return true, nil
}

// Terminal cancellation is distinct from retryable authority read failures.
func (s *Store) cancelUnavailableTask(ctx context.Context, id int64) error {
	_, e := s.db.NewUpdate().Model((*domain.Task)(nil)).Set("status = 'cancelled'").Set("payload = '{}'").Set("result = ''").Set("error = ''").Set("finished_at = ?", time.Now()).Where("id = ? AND status IN ('claimed','running')", id).Exec(ctx)
	return e
}

func conversationTaskType(kind string) bool {
	switch kind {
	case "agent.job", "reminder.fire", "techfact.analyze", "insight.generate", "bookmark.refine", "skill.review", "heartbeat.check":
		return true
	}
	return false
}
