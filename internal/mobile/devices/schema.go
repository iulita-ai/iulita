package devices

import (
	"context"
	"database/sql"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
)

// Old mirrors did not preserve the source credential family. Invalidate them;
// never guess a new family or resurrect prior native authority during migration.
func (s *Store) migratePhone(ctx context.Context, tx *sql.Tx) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='mobile_phone_schema'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM mobile_phone_schema`).Scan(&version); err != nil {
		return err
	}
	if version == 3 {
		return nil
	}
	if version == 2 {
		// V3 adds isolated UI/processing/artifact metadata. Existing native
		// permissions remain unchanged; no UI permission is inferred.
		return upgradePhoneMarker(ctx, tx)
	}
	if version != 1 {
		return ErrInvalid
	}
	for _, q := range []string{
		`ALTER TABLE mobile_device_streams ADD COLUMN family_id TEXT REFERENCES oauth_families(id)`,
		`ALTER TABLE mobile_native_sessions ADD COLUMN family_id TEXT REFERENCES oauth_families(id)`,
		`ALTER TABLE mobile_phone_tasks ADD COLUMN approval_authority BLOB`,
		`UPDATE mobile_device_streams SET epoch=epoch+1,expires_at=0,capabilities=NULL`,
		`UPDATE mobile_native_sessions SET state='revoked',expires_at=0`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if err := taskTransitions(ctx, tx, `UPDATE mobile_phone_tasks SET cancellation_requested=1,state=CASE WHEN EXISTS(SELECT 1 FROM mobile_phone_commands c WHERE c.task_id=mobile_phone_tasks.id AND c.state IN ('dispatched','started','unknown_outcome')) THEN 'unknown_outcome' ELSE 'cancelled' END,reason=? WHERE state IN ('queued','running','awaiting_confirmation','needs_user_action')`, v1.ErrorReason_ERROR_REASON_REAUTHENTICATION_REQUIRED); err != nil {
		return err
	}
	for _, q := range []string{
		`UPDATE mobile_phone_commands SET state=CASE WHEN state IN ('dispatched','started') THEN 'unknown_outcome' ELSE 'cancelled' END WHERE state IN ('queued','dispatched','started')`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return upgradePhoneMarker(ctx, tx)
}

func upgradePhoneMarker(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`DROP TABLE mobile_phone_schema`,
		`CREATE TABLE mobile_phone_schema(version INTEGER PRIMARY KEY CHECK(version=3))`,
		`INSERT INTO mobile_phone_schema VALUES(3)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
