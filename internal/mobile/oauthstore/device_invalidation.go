package oauthstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
)

// Phone tables are optional, but when present their authority is revoked in the
// same transaction as device recovery/revocation. Effect receipts are retained.
func invalidateDeviceState(ctx context.Context, tx *sql.Tx, owner, device string) error {
	if !exists(ctx, tx, "mobile_device_streams") {
		return nil
	}
	now := time.Now().UnixMilli()
	for _, q := range []string{
		`UPDATE mobile_device_streams SET epoch=epoch+1,expires_at=0,capabilities=NULL WHERE owner_id=? AND device_id=?`,
		`UPDATE mobile_native_sessions SET state='revoked',expires_at=0 WHERE owner_id=? AND device_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, q, owner, device); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `UPDATE mobile_phone_tasks SET cancellation_requested=1,state=CASE WHEN EXISTS(SELECT 1 FROM mobile_phone_commands c WHERE c.task_id=mobile_phone_tasks.id AND c.state IN ('dispatched','started','unknown_outcome')) THEN 'unknown_outcome' ELSE 'cancelled' END,reason=? WHERE owner_id=? AND (device_id=? OR viewer_device_id=?) AND state IN ('queued','running','awaiting_confirmation','needs_user_action') RETURNING id`, v1.ErrorReason_ERROR_REASON_UNKNOWN_OUTCOME, owner, device, device)
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO mobile_phone_events(id,owner_id,conversation_id,turn_id,history_generation,task_id,state,created_at) SELECT ?,owner_id,conversation_id,turn_id,history_generation,id,state,? FROM mobile_phone_tasks WHERE id=?`, uuid.NewString(), now, id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE mobile_phone_commands SET state=CASE WHEN state IN ('dispatched','started') THEN 'unknown_outcome' ELSE 'cancelled' END WHERE task_id IN (SELECT id FROM mobile_phone_tasks WHERE owner_id=? AND (device_id=? OR viewer_device_id=?)) AND state IN ('queued','dispatched','started')`, owner, device, device)
	return err
}
