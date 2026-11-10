package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"strconv"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/domain"
)

// EnsureDaySchema creates or upgrades the private-card and receipt schema.
func (s *Store) EnsureDaySchema(ctx context.Context) error {
	for _, m := range []any{(*dayview.Card)(nil), (*dayview.Operation)(nil), (*dayReminderCreation)(nil), (*reminderDeliveryClaim)(nil), (*dayview.PublicSnapshot)(nil)} {
		if _, e := s.db.NewCreateTable().Model(m).IfNotExists().Exec(ctx); e != nil {
			return e
		}
	}
	return s.migrateDaySchema(ctx)
}

// SaveDayCard persists a bounded projection behind the conversation lifecycle fence.
func (s *Store) SaveDayCard(ctx context.Context, c *dayview.Card) error {
	if len(c.ItemsJSON) > 512*1024 || len(c.DesiredState) > 1024 {
		return fmt.Errorf("day card payload exceeds budget")
	}
	return s.withTelegramCoordinatesWrite(ctx, c.Instance, c.Owner, c.Chat, c.Thread, func(db bun.IDB) error {
		_, e := db.NewInsert().Model(c).On("CONFLICT (id) DO UPDATE").Set("message = EXCLUDED.message").Set("revision = EXCLUDED.revision").Set("items_json = EXCLUDED.items_json").Set("desired_state = EXCLUDED.desired_state").Exec(ctx)
		return e
	})
}

// GetDayCard loads server-side card authority by its opaque identifier.
func (s *Store) GetDayCard(ctx context.Context, id string) (*dayview.Card, error) {
	c := new(dayview.Card)
	e := s.db.NewSelect().Model(c).Where("id = ?", id).Scan(ctx)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return c, e
}

// ClaimDayOperation claims a new approved intent while excluding unresolved resource operations.
func (s *Store) ClaimDayOperation(ctx context.Context, o *dayview.Operation) (claimed bool, err error) {
	if len(o.CommandJSON) > 8192 {
		return false, fmt.Errorf("operation payload exceeds budget")
	}
	var command dayview.Command
	if o.CommandJSON != "" {
		if json.Unmarshal([]byte(o.CommandJSON), &command) != nil || command.Item.ID == "" || o.Owner == "" || o.Conversation == "" || o.Instance == "" || o.ApprovedAt.IsZero() {
			return false, fmt.Errorf("incomplete approved operation")
		}
		o.ResourceKey = dayResourceKey(o.Owner, command.Item)
	}
	err = s.db.RunInTx(ctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		if e := conversationAvailable(ctx, tx, o.Conversation); e != nil {
			return e
		}
		var existing int
		if e := tx.NewRaw("SELECT COUNT(*) FROM operations WHERE id = ?", o.ID).Scan(ctx, &existing); e != nil {
			return e
		}
		if existing != 0 {
			return nil
		}
		if o.ResourceKey != "" {
			var blocked int
			query := tx.NewSelect().Model((*dayview.Operation)(nil)).Where("owner = ? AND status IN (?, ?, ?, ?, ?)", o.Owner, "running", "unknown_outcome", "inconclusive", "not_observed", "revoked")
			if command.Item.Source == "operations" {
				query = query.Where("resource_key = ?", o.ResourceKey)
			} else {
				query = query.Where("(resource_key = ? OR (resource_key = '' AND conversation = ?))", o.ResourceKey, o.Conversation)
			}
			count, e := query.Count(ctx)
			if e != nil {
				return e
			}
			blocked = count
			if blocked > 0 {
				return fmt.Errorf("resource has an unresolved operation; reconcile it before another change")
			}
		}
		res, e := tx.NewInsert().Model(o).On("CONFLICT (id) DO NOTHING").Exec(ctx)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		claimed = n == 1
		return e
	})
	return
}

// FinishDayOperation records success or uncertainty without scheduling another provider write.
func (s *Store) FinishDayOperation(ctx context.Context, id, status string) error {
	if status != "succeeded" && status != "unknown_outcome" {
		return fmt.Errorf("invalid operation outcome")
	}
	_, e := s.db.NewUpdate().Model((*dayview.Operation)(nil)).Set("status = ?", status).Where("id = ?", id).Exec(ctx)
	return e
}

// ReminderDaySource reads only the current owner's pending reminders in the current conversation.
type ReminderDaySource struct {
	Store *Store
	Chat  string
}

// Name returns the local reminder source name.
func (ReminderDaySource) Name() string { return "reminders" }

// List lists owned reminders and their exact delivery-state capabilities.
func (d ReminderDaySource) List(ctx context.Context, owner string, _ time.Time, _ *time.Location) ([]dayview.Item, error) {
	if owner == "" {
		return nil, dayview.ErrUnavailable
	}
	if e := d.Store.ReconcileReminderClaims(ctx, time.Now()); e != nil {
		return nil, e
	}
	var r []domain.Reminder
	e := d.Store.db.NewSelect().Model(&r).Where("user_id = ? AND chat_id = ? AND status IN (?, ?, ?, ?)", owner, d.Chat, "pending", "firing", "delivery_unknown", "suppressed").Order("due_at ASC").Limit(100).Scan(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]dayview.Item, 0, len(r))
	for index := range r {
		v := &r[index]
		v.Title = reminderDayTitle(v.Status, v.Title)
		out = append(out, dayview.Item{ID: strconv.FormatInt(v.ID, 10), Source: "reminders", Title: v.Title, Due: v.DueAt.Format(time.RFC3339Nano), Revision: dayview.Revision(v.Title, v.DueAt.Format(time.RFC3339Nano)), Reschedule: v.Status == "pending" || v.Status == "suppressed", ResolveDelivery: v.Status == "delivery_unknown"})
	}
	return out, nil
}

// Change conditionally acknowledges or reschedules an owned reminder.
func (d ReminderDaySource) Change(ctx context.Context, owner string, i dayview.Item, action string, due time.Time) error {
	if action == "acknowledge" {
		if owner == "" || !i.ResolveDelivery {
			return dayview.ErrUnavailable
		}
		r := new(domain.Reminder)
		if e := d.Store.db.NewSelect().Model(r).Where("id = ? AND user_id = ? AND chat_id = ? AND status = ?", i.ID, owner, d.Chat, "delivery_unknown").Scan(ctx); e != nil {
			return e
		}
		if dayview.Revision("Delivery unconfirmed: "+r.Title, r.DueAt.Format(time.RFC3339Nano)) != i.Revision {
			return dayview.ErrStale
		}
		if e := d.Store.ResolveReminderDelivery(ctx, owner, d.Chat, r.ID, "delivery_unknown", "acknowledged"); e != nil {
			return e
		}
		var verified string
		if e := d.Store.db.NewSelect().Model((*domain.Reminder)(nil)).Column("status").Where("id = ? AND user_id = ? AND chat_id = ?", r.ID, owner, d.Chat).Scan(ctx, &verified); e != nil {
			return e
		}
		if verified != "acknowledged_by_user" {
			return dayview.ErrStale
		}
		return nil
	}
	if owner == "" || action != "reschedule" || !due.After(time.Now()) {
		return dayview.ErrUnavailable
	}
	return d.Store.withConversationWrite(ctx, d.Chat, func(db bun.IDB) error {
		r := new(domain.Reminder)
		e := db.NewSelect().Model(r).Where("id = ? AND user_id = ? AND chat_id = ? AND status IN (?, ?)", i.ID, owner, d.Chat, "pending", "suppressed").Scan(ctx)
		if e != nil {
			return e
		}
		if dayview.Revision(reminderDayTitle(r.Status, r.Title), r.DueAt.Format(time.RFC3339Nano)) != i.Revision {
			return dayview.ErrStale
		}
		res, e := db.NewUpdate().Model((*domain.Reminder)(nil)).Set("due_at = ?", due.UTC()).Set("status = ?", "pending").Where("id = ? AND user_id = ? AND status = ? AND due_at = ?", r.ID, owner, r.Status, r.DueAt).Exec(ctx)
		if e != nil {
			return e
		}
		n, countErr := res.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if n != 1 {
			return dayview.ErrStale
		}
		return nil
	})
}

// CurrentReminder rejects cancelled/rescheduled stale jobs before delivery.
func (s *Store) CurrentReminder(ctx context.Context, id int64, chat string, due time.Time) (*domain.Reminder, error) {
	r := new(domain.Reminder)
	e := s.db.NewSelect().Model(r).Where("id = ? AND chat_id = ? AND status = ?", id, chat, "pending").Scan(ctx)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if !r.DueAt.Equal(due) || r.DueAt.After(time.Now()) {
		return nil, nil
	}
	return r, nil
}

var _ dayview.Store = (*Store)(nil)

// ClaimReminderDelivery atomically excludes snooze/cancel from an in-flight
// send. A crash after this claim is uncertain, never blindly redelivered.
func (s *Store) ClaimReminderDelivery(ctx context.Context, id int64, chat string, due time.Time) (*domain.Reminder, error) {
	return s.claimReminder(ctx, id, chat, due)
}

// MarkReminderDeliveryUnknown marks an in-flight send uncertain without requeuing delivery.
func (s *Store) MarkReminderDeliveryUnknown(ctx context.Context, id int64) error {
	_, e := s.db.NewUpdate().Model((*domain.Reminder)(nil)).Set("status = ?", "delivery_unknown").Where("id = ? AND status = ?", id, "firing").Exec(ctx)
	return e
}

// Verify observes the owned reminder only, with no requeue/reschedule writes.
func (d ReminderDaySource) Verify(ctx context.Context, owner string, item dayview.Item, action string, due time.Time) (dayview.Verification, error) {
	if owner == "" {
		return dayview.Revoked, nil
	}
	r := new(domain.Reminder)
	e := d.Store.db.NewSelect().Model(r).Where("id = ? AND user_id = ? AND chat_id = ?", item.ID, owner, d.Chat).Scan(ctx)
	if errors.Is(e, sql.ErrNoRows) {
		return dayview.Inconclusive, nil
	}
	if e != nil {
		return dayview.Inconclusive, e
	}
	switch action {
	case "acknowledge":
		if r.Status == "acknowledged_by_user" {
			return dayview.EffectObserved, nil
		}
		return dayview.NotObserved, nil
	case "reschedule":
		if !due.IsZero() && r.DueAt.Equal(due) {
			return dayview.EffectObserved, nil
		}
		return dayview.NotObserved, nil
	}
	return dayview.Inconclusive, nil
}

func reminderDayTitle(status, title string) string {
	switch status {
	case "pending":
		return title
	case "suppressed":
		return "Not delivered (topic paused): " + title
	default:
		return "Delivery unconfirmed: " + title
	}
}
