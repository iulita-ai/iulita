package oauthstore

import (
	"context"
	"time"
)

// Sweep keeps replay tombstones for the entire family lifetime plus seven
// days. Expiring an individual refresh never removes its reuse evidence.
func (s *Store) Sweep(ctx context.Context) error {
	return s.WithRequest(ctx, func(ctx context.Context) error {
		tx, operationErr1 := s.transaction(ctx)
		if operationErr1 != nil {
			return operationErr1
		}
		now := time.Now().Unix()
		for _, q := range []string{`DELETE FROM oauth_browser_flows WHERE expires_at<=?`, `DELETE FROM oauth_browser_limits WHERE window_start<?`} {
			cutoff := now
			if q == `DELETE FROM oauth_browser_limits WHERE window_start<?` {
				cutoff = now - 86400
			}
			if _, err := tx.ExecContext(ctx, q, cutoff); err != nil {
				return err
			}
		}
		for _, q := range []string{`DELETE FROM oauth_device_requests WHERE id IN (SELECT id FROM oauth_device_requests WHERE expires_at<? LIMIT 1000)`, `DELETE FROM oauth_service_access WHERE grant_id IN (SELECT grant_id FROM oauth_service_access WHERE expires_at<? LIMIT 1000)`} {
			if _, cleanupErr := tx.ExecContext(ctx, q, now-7*86400); cleanupErr != nil {
				return cleanupErr
			}
		}
		condition := `(expires_at<? OR (stage<>'active' AND deadline<?))`
		if exists(ctx, tx, "mobile_phone_tasks") {
			condition += ` AND id NOT IN (SELECT caller_family_id FROM mobile_phone_tasks)`
		}
		for _, table := range []string{"mobile_device_streams", "mobile_native_sessions"} {
			if exists(ctx, tx, table) {
				// Chat-only runtimes may still have phone v1 tables, which had no
				// family column. Do not make unrelated OAuth maintenance fail.
				var columns int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name='family_id'`, table).Scan(&columns); err != nil {
					return err
				}
				if columns > 0 {
					condition += ` AND id NOT IN (SELECT family_id FROM ` + table + ` WHERE family_id IS NOT NULL)`
				}
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM oauth_families WHERE `+condition+` LIMIT 1000`, now-7*86400, now-7*86400)
		if err != nil {
			return err
		}
		var ids []string
		defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		if err != nil {
			return err
		}
		for _, id := range ids {
			for _, q := range []string{`DELETE FROM oauth_sessions WHERE family_id=?`, `DELETE FROM oauth_nonces WHERE family_id=?`, `DELETE FROM oauth_completions WHERE family_id=?`, `DELETE FROM oauth_families WHERE id=?`} {
				if _, err = tx.ExecContext(ctx, q, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// RunMaintenance performs bounded OAuth expiry and retention maintenance.
func (s *Store) RunMaintenance(ctx context.Context) error {
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := s.Sweep(ctx); err != nil {
				return err
			}
		}
	}
}
