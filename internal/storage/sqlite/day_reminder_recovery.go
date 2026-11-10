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

type reminderDeliveryClaim struct {
	ReminderID int64 `bun:",pk"`
	ClaimedAt  time.Time
	DueAt      time.Time
}

func (s *Store) claimReminder(ctx context.Context, id int64, chat string, due time.Time) (out *domain.Reminder, err error) {
	err = s.db.RunInTx(ctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		if e := conversationAvailable(ctx, tx, chat); e != nil {
			return e
		}
		r := new(domain.Reminder)
		e := tx.NewSelect().Model(r).Where("id = ? AND chat_id = ? AND status = ?", id, chat, "pending").Scan(ctx)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if !r.DueAt.Equal(due) || r.DueAt.After(time.Now()) {
			return nil
		}
		res, e := tx.NewUpdate().Model((*domain.Reminder)(nil)).Set("status = ?", "firing").Where("id = ? AND status = ? AND due_at = ?", id, "pending", r.DueAt).Exec(ctx)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return nil
		}
		claim := &reminderDeliveryClaim{ReminderID: id, ClaimedAt: time.Now(), DueAt: r.DueAt}
		if _, e = tx.NewInsert().Model(claim).On("CONFLICT (reminder_id) DO UPDATE").Set("claimed_at = EXCLUDED.claimed_at").Set("due_at = EXCLUDED.due_at").Exec(ctx); e != nil {
			return e
		}
		r.Status = "firing"
		out = r
		return nil
	})
	return
}

// ReconcileReminderClaims marks abandoned sends uncertain, never requeues them.
// Fifteen minutes exceeds the bounded sender call and avoids treating an active
// request as evidence of a crash. Legacy rows receive a fresh lease at upgrade.
func (s *Store) ReconcileReminderClaims(ctx context.Context, now time.Time) error {
	_, e := s.db.NewUpdate().Model((*domain.Reminder)(nil)).Set("status = ?", "delivery_unknown").Where("status = ? AND id IN (SELECT reminder_id FROM reminder_delivery_claims WHERE claimed_at < ?)", "firing", now.Add(-15*time.Minute)).Exec(ctx)
	return e
}
func (s *Store) acknowledgeReminder(ctx context.Context, owner, conversation string, id int64, expected string) error {
	if e := s.ReconcileReminderClaims(ctx, time.Now()); e != nil {
		return e
	}
	res, e := s.db.NewUpdate().Model((*domain.Reminder)(nil)).Set("status = ?", "acknowledged_by_user").Where("id = ? AND user_id = ? AND chat_id = ? AND status = ?", id, owner, conversation, expected).Exec(ctx)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("reminder changed or unavailable")
	}
	return nil
}

// MarkReminderDeliverySuppressed records a definitive no-send decision. It is
// terminal for the queued job and becomes pending only via explicit reschedule.
func (s *Store) MarkReminderDeliverySuppressed(ctx context.Context, id int64) error {
	res, e := s.db.NewUpdate().Model((*domain.Reminder)(nil)).Set("status = ?", "suppressed").Where("id = ? AND status = ?", id, "firing").Exec(ctx)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("reminder delivery state changed")
	}
	return nil
}
