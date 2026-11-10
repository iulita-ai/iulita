package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/uptrace/bun"
)

type daySchemaVersion struct {
	ID        int `bun:",pk"`
	AppliedAt time.Time
}

// Versioned additive upgrade: old tables are deliberately exercised by tests.
func (s *Store) migrateDaySchema(ctx context.Context) error {
	if _, e := s.db.NewCreateTable().Model((*daySchemaVersion)(nil)).IfNotExists().Exec(ctx); e != nil {
		return e
	}
	return s.db.RunInTx(ctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		var done int
		if e := tx.NewRaw("SELECT COUNT(*) FROM day_schema_versions WHERE id = 6").Scan(ctx, &done); e != nil {
			return e
		}
		if done != 0 {
			return nil
		}
		for _, c := range []struct{ table, name, kind string }{{"operations", "conversation", "TEXT NOT NULL DEFAULT ''"}, {"operations", "instance", "TEXT NOT NULL DEFAULT ''"}, {"operations", "command_json", "TEXT NOT NULL DEFAULT ''"}, {"operations", "approved_at", "TIMESTAMP"}, {"operations", "last_checked_at", "TIMESTAMP"}, {"operations", "payload_compacted", "BOOLEAN NOT NULL DEFAULT FALSE"}, {"operations", "resource_key", "TEXT NOT NULL DEFAULT ''"}, {"cards", "desired_state", "TEXT NOT NULL DEFAULT ''"}, {"cards", "delivered_revision", "BIGINT NOT NULL DEFAULT 0"}, {"day_reminder_creations", "command_digest", "TEXT NOT NULL DEFAULT ''"}} {
			var count int
			if e := tx.NewRaw("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", c.table, c.name).Scan(ctx, &count); e != nil {
				return e
			}
			if count == 0 {
				if _, e := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.name, c.kind)); e != nil {
					return e
				}
			}
		}
		for _, query := range []string{"CREATE INDEX IF NOT EXISTS day_operations_scope ON operations(owner, conversation, status)", "CREATE INDEX IF NOT EXISTS day_operations_resource ON operations(resource_key, status)", "CREATE INDEX IF NOT EXISTS day_cards_expiry ON cards(expires)", "CREATE INDEX IF NOT EXISTS day_public_expiry ON public_snapshots(expires_at)"} {
			if _, e := tx.ExecContext(ctx, query); e != nil {
				return e
			}
		}
		if _, e := tx.ExecContext(ctx, `UPDATE operations SET conversation=COALESCE((SELECT json_extract(c.items_json,'$.Conversation') FROM cards c WHERE c.id=operations.card_id AND c.owner=operations.owner AND json_valid(c.items_json)),''), instance=COALESCE((SELECT c.instance FROM cards c WHERE c.id=operations.card_id AND c.owner=operations.owner),'') WHERE conversation=''`); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE operations SET status='inconclusive' WHERE command_json='' AND status IN ('running','unknown_outcome')`); e != nil {
			return e
		}
		var legacy []dayview.Operation
		if e := tx.NewSelect().Model(&legacy).Where("resource_key = '' AND command_json <> ''").Scan(ctx, &legacy); e != nil {
			return e
		}
		for index := range legacy {
			op := &legacy[index]
			var cmd dayview.Command
			if json.Unmarshal([]byte(op.CommandJSON), &cmd) != nil {
				continue
			}
			key := dayResourceKey(op.Owner, cmd.Item)
			if _, e := tx.NewUpdate().Model((*dayview.Operation)(nil)).Set("resource_key = ?", key).Where("id = ?", op.ID).Exec(ctx); e != nil {
				return e
			}
		}
		var remindersExist int
		if e := tx.NewRaw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'reminders'").Scan(ctx, &remindersExist); e != nil {
			return e
		}
		if remindersExist > 0 {
			if _, e := tx.ExecContext(ctx, "INSERT INTO reminder_delivery_claims (reminder_id, claimed_at, due_at) SELECT id, ?, due_at FROM reminders WHERE status = ? ON CONFLICT (reminder_id) DO NOTHING", time.Now(), "firing"); e != nil {
				return e
			}
		}
		_, e := tx.NewInsert().Model(&daySchemaVersion{ID: 6, AppliedAt: time.Now()}).Exec(ctx)
		return e
	})
}

// ListDayOperations lists bounded durable receipts for an exact owner and conversation.
func (s *Store) ListDayOperations(ctx context.Context, owner, conversation string) ([]dayview.Operation, error) {
	if owner == "" || conversation == "" {
		return nil, fmt.Errorf("operation scope required")
	}
	var out []dayview.Operation
	e := s.db.NewSelect().Model(&out).Where("owner = ? AND conversation = ?", owner, conversation).Where("status IN (?, ?, ?, ?, ?, ?)", "running", "unknown_outcome", "effect_observed", "not_observed", "inconclusive", "revoked").Order("created_at DESC").Limit(20).Scan(ctx)
	return out, e
}

// ApplyDayVerification records a read-only outcome if the receipt still has the expected status.
func (s *Store) ApplyDayVerification(ctx context.Context, owner, id, expected string, v dayview.Verification) error {
	if owner == "" || id == "" {
		return fmt.Errorf("operation scope required")
	}
	if v != dayview.EffectObserved && v != dayview.NotObserved && v != dayview.Inconclusive && v != dayview.Revoked {
		return fmt.Errorf("invalid verification")
	}
	if expected != "running" && expected != "unknown_outcome" && expected != "inconclusive" && expected != "not_observed" && expected != "revoked" {
		return fmt.Errorf("invalid operation transition")
	}
	res, e := s.db.NewUpdate().Model((*dayview.Operation)(nil)).Set("status = ?", string(v)).Set("last_checked_at = ?", time.Now()).Where("id = ? AND owner = ? AND status = ?", id, owner, expected).Exec(ctx)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return dayview.ErrStale
	}
	return nil
}

// CleanupDayState removes private projections but preserves compact operation
// tombstones. Cleanup never removes active reminder routes or retries writes.
func (s *Store) CleanupDayState(ctx context.Context, now time.Time, batch int) error {
	if e := s.ReconcileReminderClaims(ctx, now); e != nil {
		return e
	}
	if batch < 1 || batch > 100 {
		batch = 100
	}
	var cards []dayview.Card
	if e := s.db.NewSelect().Model(&cards).Where("expires <= ? AND (items_json <> '' OR desired_state <> '')", now).Order("expires ASC").Limit(batch).Scan(ctx); e != nil {
		return e
	}
	for index := range cards {
		c := &cards[index]
		if _, e := s.db.NewUpdate().Model((*dayview.Card)(nil)).Set("items_json = ''").Set("desired_state = ''").Where("id = ? AND expires <= ?", c.ID, now).Exec(ctx); e != nil {
			return e
		}
	}
	var snapshots []dayview.PublicSnapshot
	if e := s.db.NewSelect().Model(&snapshots).Where("expires_at <= ? AND text <> ''", now).Order("expires_at ASC").Limit(batch).Scan(ctx); e != nil {
		return e
	}
	for index := range snapshots {
		snapshot := &snapshots[index]
		if _, e := s.db.NewUpdate().Model((*dayview.PublicSnapshot)(nil)).Set("text = ''").Set("withdrawn = TRUE").Where("id = ? AND expires_at <= ?", snapshot.ID, now).Exec(ctx); e != nil {
			return e
		}
	}
	var operations []dayview.Operation
	if e := s.db.NewSelect().Model(&operations).Where("created_at < ? AND command_json <> '' AND payload_compacted = FALSE", now.Add(-30*24*time.Hour)).Order("created_at ASC").Limit(batch).Scan(ctx); e != nil {
		return e
	}
	for index := range operations {
		op := &operations[index]
		payload := ""
		if op.Status != "succeeded" && op.Status != "effect_observed" && op.Status != "acknowledged_by_user" {
			var cmd dayview.Command
			if json.Unmarshal([]byte(op.CommandJSON), &cmd) == nil {
				cmd.Item.Title = ""
				raw, marshalErr := json.Marshal(cmd)
				if marshalErr != nil {
					return marshalErr
				}
				payload = string(raw)
			}
		}

		if _, e := s.db.NewUpdate().Model((*dayview.Operation)(nil)).Set("command_json = ?", payload).Set("payload_compacted = TRUE").Where("id = ? AND command_json = ?", op.ID, op.CommandJSON).Exec(ctx); e != nil {
			return e
		}
	}
	return nil
}

// ResolveReminderDelivery records a user's observation; it does not manufacture
// technical delivery proof or requeue an uncertain Telegram send.
func (s *Store) ResolveReminderDelivery(ctx context.Context, owner, conversation string, id int64, expected, decision string) error {
	if owner == "" || conversation == "" || decision != "acknowledged" || expected != "delivery_unknown" {
		return fmt.Errorf("invalid reminder resolution")
	}
	return s.acknowledgeReminder(ctx, owner, conversation, id, expected)
}
