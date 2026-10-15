package devices

import (
	"context"
	"database/sql"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

// ReconcileParents invalidates pending work before every control poll. The
// returned native-session revocations are notifications, never remote-wipe or
// proof that an already dispatched effect was rolled back.
func (s *Store) ReconcileParents(ctx context.Context, identity Identity, epoch uint64) ([]*v1.SessionRevocation, error) {
	var revoked []*v1.SessionRevocation
	err := s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if err := streamLive(ctx, tx, c, epoch); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM mobile_phone_tasks WHERE owner_id=? AND device_id=? AND cancellation_requested=0 AND state IN ('queued','running','awaiting_confirmation','needs_user_action') LIMIT 8`, c.Subject, c.DeviceID)
		if err != nil {
			return err
		}
		var tasks []Task
		defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
		for rows.Next() {
			t, e := scanTask(rows)
			if e != nil {
				rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
				return e
			}
			tasks = append(tasks, t)
		}
		err = rows.Err()
		rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		if err != nil {
			return err
		}
		for i := range tasks {
			t := &tasks[i]
			t.Owner = c.Subject
			t.Device = c.DeviceID
			if nativeSessionLive(ctx, tx, *t) && s.originFamilyLive(ctx, tx, *t) && s.chats.OriginAllowed(ctx, c.Subject, t.Conversation, t.Turn, t.Generation) == nil {
				continue
			}
			var uncertain int
			if checkErr1 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_phone_commands WHERE task_id=? AND state IN ('dispatched','started','unknown_outcome')`, t.ID).Scan(&uncertain); checkErr1 != nil {
				return checkErr1
			}
			state := "cancelled"
			reason := v1.ErrorReason_ERROR_REASON_STALE_GENERATION
			if uncertain > 0 {
				state = "unknown_outcome"
				reason = v1.ErrorReason_ERROR_REASON_UNKNOWN_OUTCOME
			}
			if checkErr2 := taskTransitions(ctx, tx, `UPDATE mobile_phone_tasks SET cancellation_requested=1,state=?,reason=? WHERE id=? AND owner_id=? AND device_id=?`, state, reason, t.ID, c.Subject, c.DeviceID); checkErr2 != nil {
				return checkErr2
			}
			if _, err = tx.ExecContext(ctx, `UPDATE mobile_phone_commands SET state=CASE WHEN state IN ('dispatched','started') THEN 'unknown_outcome' ELSE 'cancelled' END WHERE task_id=? AND state IN ('queued','dispatched','started')`, t.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE mobile_native_sessions SET state='revoked' WHERE owner_id=? AND device_id=? AND id=? AND generation=?`, c.Subject, c.DeviceID, t.Session, t.SessionGeneration); err != nil {
				return err
			}
			s.content.DeleteTask(c.Subject, t.ID)
			revoked = append(revoked, &v1.SessionRevocation{SessionId: t.Session, SessionGeneration: t.SessionGeneration, Reason: reason})
		}
		return nil
	})
	return revoked, err
}
