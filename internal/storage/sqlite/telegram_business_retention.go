package sqlite

import (
	"context"
	"time"

	"github.com/uptrace/bun"
)

// PurgeTelegramBusiness expires bounded batches of private payloads and polling
// artifacts. Non-content operation/tombstone metadata is intentionally retained.
// Active snapshots also require the core's per-source seven-day pruning.
func (r *TelegramBusinessRepository) PurgeTelegramBusiness(ctx context.Context, now time.Time) error {
	return r.store.runWriteTransaction(ctx, func(ctx context.Context, db bun.IDB) error {
		for _, statement := range []struct {
			SQL    string
			Before time.Time
		}{
			{`UPDATE telegram_business_connections SET payload='' WHERE rowid IN (SELECT rowid FROM telegram_business_connections WHERE payload<>'' AND updated_at<? LIMIT 100)`, now.Add(-7 * 24 * time.Hour)},
			{`DELETE FROM telegram_business_poll_entries WHERE rowid IN (SELECT rowid FROM telegram_business_poll_entries WHERE created_at<? LIMIT 100)`, now.Add(-24 * time.Hour)},
			{`DELETE FROM telegram_business_budget_reservations WHERE rowid IN (SELECT rowid FROM telegram_business_budget_reservations WHERE expires_at<? LIMIT 100)`, now.Add(-30 * 24 * time.Hour)},
		} {
			if _, err := db.ExecContext(ctx, statement.SQL, statement.Before); err != nil {
				return err
			}
		}
		before := now.UTC().Add(-30 * 24 * time.Hour).Format("2006-01-02")
		if _, err := db.ExecContext(ctx, `DELETE FROM telegram_business_budget_usage WHERE rowid IN (SELECT rowid FROM telegram_business_budget_usage WHERE day<? LIMIT 100)`, before); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `DELETE FROM telegram_business_source_usage WHERE rowid IN (SELECT rowid FROM telegram_business_source_usage WHERE day<? LIMIT 100)`, before)
		return err
	})
}
