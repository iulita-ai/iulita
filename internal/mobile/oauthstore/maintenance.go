package oauthstore

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
)

func syncDir(path string) error {
	f, err := os.Open(path) //nolint:gosec // Path comes from explicit local runtime configuration, not remote input.
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	return f.Sync()
}
func privateWrite(dir, name string, body []byte) error {
	f, err := os.CreateTemp(dir, ".mobile-maintenance-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) //nolint:errcheck // Best-effort cleanup of a private temporary file.
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if checkErr2 := os.Rename(f.Name(), filepath.Join(dir, name)); checkErr2 != nil {
		return checkErr2
	}
	return syncDir(dir)
}
func exists(ctx context.Context, tx *sql.Tx, table string) bool {
	var n int
	return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n) == nil && n == 1
}

// RotateOffline is an explicit stopped-runtime operation. It preserves effect
// receipts, revokes all credentials and cancels restored work (including queued
// turns), then publishes fresh independent keys/epoch. No rotation is automatic.
// A durable marker blocks startup across any cross-file commit gap. After a
// crash, Recover=true is required to repeat invalidation with a newer epoch.
func RotateOffline(ctx context.Context, keyDir, oauthPath, accountPath, issuer, audience string, restore, recoverCredentials bool) error {
	lock, err := LockRealm(keyDir)
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	if _, err = os.Lstat(filepath.Join(keyDir, pendingFile)); err == nil && !recoverCredentials {
		return ErrConflict
	} else if err != nil && !os.IsNotExist(err) {
		return ErrKeys
	}
	old, err := loadRealm(keyDir, issuer, audience, true)
	if err != nil {
		return err
	}
	oauthAbs, err := filepath.Abs(oauthPath)
	if err != nil {
		return err
	}
	accountAbs, err := filepath.Abs(accountPath)
	if err != nil || oauthAbs == accountAbs {
		return ErrInvalid
	}
	account, err := sqlitedb.Open(ctx, accountPath, false)
	if err != nil {
		return err
	}
	defer account.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	db, err := sqlitedb.Open(ctx, oauthPath, false)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	accountTx, err := account.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer accountTx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	var oauthVersion, chatVersion int
	if tx.QueryRowContext(ctx, `SELECT MAX(version) FROM oauth_schema`).Scan(&oauthVersion) != nil || oauthVersion != 3 || accountTx.QueryRowContext(ctx, `SELECT MAX(version) FROM mobile_chat_schema`).Scan(&chatVersion) != nil || chatVersion != 3 {
		return ErrInvalid
	}
	if exists(ctx, tx, "mobile_phone_schema") {
		var phoneVersion int
		if tx.QueryRowContext(ctx, `SELECT MAX(version) FROM mobile_phone_schema`).Scan(&phoneVersion) != nil || phoneVersion != 2 {
			return ErrInvalid
		}
	}
	var id, kid string
	var epoch uint64
	var binding []byte
	if err = tx.QueryRowContext(ctx, `SELECT id,kid,signing_epoch,binding FROM oauth_realm`).Scan(&id, &kid, &epoch, &binding); err != nil || id != old.id {
		return ErrKeys
	}
	if !restore && !recoverCredentials && (kid != old.kid || epoch != old.epoch || !equalHash(binding, old.binding())) {
		return ErrKeys
	}
	if epoch < old.epoch {
		epoch = old.epoch
	}
	if !boundedEpoch(epoch) {
		return ErrInvalid
	}
	epoch++
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return err
	}
	d := diskKeys{Version: 1, RealmID: old.id, Kid: uuid.NewString(), Issuer: issuer, Audience: audience, SigningEpoch: epoch, PrivateDER: x509.MarshalPKCS1PrivateKey(key), HMAC: make([]byte, 32), Hash: make([]byte, 32), Cursor: make([]byte, 32)}
	for _, b := range [][]byte{d.HMAC, d.Hash, d.Cursor} {
		if _, err = rand.Read(b); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if checkErr3 := privateWrite(keyDir, keyFile+".next", encoded); checkErr3 != nil {
		return checkErr3
	}
	marker, _ := json.Marshal(struct { //nolint:errcheck // This internal snapshot contains only JSON-supported values.
		Epoch   uint64
		Restore bool
	}{epoch, restore})
	if checkErr4 := privateWrite(keyDir, pendingFile, marker); checkErr4 != nil {
		return checkErr4
	}
	next := &Realm{issuer: issuer, audience: audience, id: old.id, kid: d.Kid, epoch: epoch, private: key, hmacKey: d.HMAC, hashKey: d.Hash, cursorKey: d.Cursor}
	if exists(ctx, accountTx, "mobile_turns") {
		for _, q := range []string{
			`UPDATE mobile_turns SET cancellation_requested=1,processing_epoch=processing_epoch+1,state=CASE WHEN EXISTS(SELECT 1 FROM mobile_tool_operations o WHERE o.turn_id=mobile_turns.id AND o.owner_id=mobile_turns.owner_id AND (o.state IN ('started','unknown_outcome') OR (o.state='succeeded' AND o.effect<>'read'))) THEN 'unknown_outcome' ELSE 'cancelled' END,outcome_reason='security_maintenance' WHERE state IN ('accepted','queued','running','awaiting_prompt')`,
			`UPDATE mobile_tool_operations SET state='unknown_outcome',outcome='' WHERE state='started'`,
			`UPDATE mobile_prompts SET state='invalidated' WHERE state='open'`,
			`DELETE FROM mobile_snapshots`,
			`UPDATE mobile_runtime SET expires_at=0,epoch=epoch+1`,
		} {
			if _, err = accountTx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
	}
	for _, q := range []string{`UPDATE oauth_owners SET epoch=epoch+1`, `UPDATE oauth_devices SET epoch=epoch+1`, `UPDATE oauth_families SET revoked=1`, `UPDATE oauth_sessions SET active=0`, `UPDATE oauth_nonces SET consumed=1`} {
		if _, err = tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if exists(ctx, tx, "mobile_phone_tasks") {
		for _, q := range []string{
			`UPDATE mobile_device_streams SET expires_at=0,epoch=epoch+1`,
			`UPDATE mobile_native_sessions SET state='revoked'`,
			`UPDATE mobile_phone_tasks SET cancellation_requested=1,state=CASE WHEN EXISTS(SELECT 1 FROM mobile_phone_commands c WHERE c.task_id=mobile_phone_tasks.id AND c.state IN ('dispatched','started','unknown_outcome')) THEN 'unknown_outcome' ELSE 'cancelled' END WHERE state IN ('queued','running','awaiting_confirmation','needs_user_action')`,
			`UPDATE mobile_phone_commands SET state=CASE WHEN state IN ('dispatched','started') THEN 'unknown_outcome' ELSE 'cancelled' END,coordinator_epoch=coordinator_epoch+1 WHERE state IN ('queued','dispatched','started')`,
		} {
			if _, err = tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE oauth_realm SET kid=?,signing_epoch=?,binding=? WHERE id=?`, next.kid, next.epoch, next.binding(), old.id); err != nil {
		return err
	}
	// If either commit fails, the marker deliberately remains and startup fails.
	if checkErr5 := accountTx.Commit(); checkErr5 != nil {
		return checkErr5
	}
	if checkErr6 := tx.Commit(); checkErr6 != nil {
		return checkErr6
	}
	if checkErr7 := os.Rename(filepath.Join(keyDir, keyFile+".next"), filepath.Join(keyDir, keyFile)); checkErr7 != nil {
		return checkErr7
	}
	if checkErr8 := syncDir(keyDir); checkErr8 != nil {
		return checkErr8
	}
	if checkErr9 := os.Remove(filepath.Join(keyDir, pendingFile)); checkErr9 != nil {
		return checkErr9
	}
	return syncDir(keyDir)
}
