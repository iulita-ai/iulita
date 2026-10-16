package devices

import (
	"context"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
)

// Sweep never replays a dispatch. Terminal metadata/receipt tombstones last at
// least seven days; recovery of evidence does not authorize expired execution.
func (s *Store) Sweep(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	now := s.clock().UnixMilli()
	s.history.Sweep()
	if stepErr2 := taskTransitions(ctx, tx, `UPDATE mobile_phone_tasks SET state=CASE WHEN EXISTS(SELECT 1 FROM mobile_phone_commands c WHERE c.task_id=mobile_phone_tasks.id AND c.state IN ('dispatched','started','unknown_outcome')) THEN 'unknown_outcome' ELSE 'expired' END,cancellation_requested=1,reason=? WHERE action>=6 AND id IN (SELECT task_id FROM mobile_ui_tasks WHERE lease_until<=?) AND state IN ('queued','running','awaiting_confirmation','needs_user_action')`, v1.ErrorReason_ERROR_REASON_UNKNOWN_OUTCOME, now); stepErr2 != nil {
		return stepErr2
	}
	if checkErr1 := taskTransitions(ctx, tx, `UPDATE mobile_phone_tasks SET state=CASE WHEN EXISTS(SELECT 1 FROM mobile_phone_commands c WHERE c.task_id=mobile_phone_tasks.id AND c.state IN ('dispatched','started','unknown_outcome')) THEN 'unknown_outcome' ELSE 'expired' END,reason=? WHERE expires_at<=? AND state IN ('queued','running','awaiting_confirmation','needs_user_action')`, v1.ErrorReason_ERROR_REASON_UNKNOWN_OUTCOME, now); checkErr1 != nil {
		return checkErr1
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_phone_commands SET state=CASE WHEN state IN ('dispatched','started') THEN 'unknown_outcome' ELSE 'expired' END WHERE expires_at<=? AND state IN ('queued','dispatched','started')`, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_native_sessions SET state='expired' WHERE expires_at<=? AND state='active'`, now); err != nil {
		return err
	}
	cutoff := s.clock().Add(-7 * 24 * time.Hour).UnixMilli()
	if _, err = tx.ExecContext(ctx, `DELETE FROM mobile_native_sessions WHERE expires_at<? AND NOT EXISTS(SELECT 1 FROM mobile_phone_tasks t WHERE t.owner_id=mobile_native_sessions.owner_id AND t.device_id=mobile_native_sessions.device_id AND t.session_id=mobile_native_sessions.id)`, cutoff); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM mobile_telemetry_records WHERE expires_at<?`,
		`DELETE FROM mobile_artifact_chunks WHERE upload_id IN (SELECT upload_id FROM mobile_artifacts WHERE expires_at<?)`,
		`DELETE FROM mobile_artifacts WHERE expires_at<?`,
		`DELETE FROM mobile_processing WHERE expires_at<?`,
		`DELETE FROM mobile_ui_commands WHERE command_id IN (SELECT c.id FROM mobile_phone_commands c JOIN mobile_phone_tasks t ON t.id=c.task_id WHERE t.created_at<?)`,
		`DELETE FROM mobile_ui_tasks WHERE hard_until<?`,
		`DELETE FROM mobile_ui_snapshots WHERE task_id IN (SELECT id FROM mobile_phone_tasks WHERE created_at<?)`,
		`DELETE FROM mobile_external_delivery WHERE task_id IN (SELECT id FROM mobile_phone_tasks WHERE created_at<?)`,
		`DELETE FROM mobile_external_turns WHERE turn_id NOT IN (SELECT turn_id FROM mobile_phone_tasks) AND binding_id IN (SELECT id FROM mobile_external_bindings WHERE expires_at<?)`,
		`DELETE FROM mobile_external_bindings WHERE expires_at<? AND id NOT IN (SELECT binding_id FROM mobile_external_turns)`,
		`DELETE FROM mobile_phone_events WHERE created_at<?`,
		`DELETE FROM mobile_phone_receipts WHERE created_at<?`,
		`DELETE FROM mobile_phone_observations WHERE created_at<?`,
		`DELETE FROM mobile_sync_task_operations WHERE task_id IN (SELECT id FROM mobile_phone_tasks WHERE created_at<?)`,
		`DELETE FROM mobile_phone_commands WHERE task_id IN (SELECT id FROM mobile_phone_tasks WHERE created_at<?) AND id NOT IN (SELECT command_id FROM mobile_phone_receipts)`,
		`DELETE FROM mobile_phone_tasks WHERE created_at<? AND id NOT IN (SELECT task_id FROM mobile_phone_commands) AND id NOT IN (SELECT task_id FROM mobile_phone_observations) AND id NOT IN (SELECT task_id FROM mobile_phone_events) AND id NOT IN (SELECT task_id FROM mobile_ui_tasks) AND id NOT IN (SELECT task_id FROM mobile_artifacts) AND id NOT IN (SELECT task_id FROM mobile_processing)`,
	} {
		if _, err = tx.ExecContext(ctx, q, cutoff); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Run maintains expiry and cleanup until the context ends.
func (s *Store) Run(ctx context.Context) error {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := s.PublishEvents(ctx); err != nil {
				return err
			}
			if err := s.PublishExternal(ctx); err != nil {
				return err
			}
			if err := s.Sweep(ctx); err != nil {
				return err
			}
		}
	}
}
