package devices

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
)

func TestPhoneV1MigrationInvalidatesUnboundNativeAuthority(t *testing.T) {
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "phone.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{`CREATE TABLE oauth_families(id TEXT PRIMARY KEY)`, `CREATE TABLE oauth_devices(owner_id TEXT NOT NULL,id TEXT NOT NULL,UNIQUE(owner_id,id))`} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range schema {
		if strings.Contains(q, "mobile_sync") {
			continue
		}
		q = strings.ReplaceAll(q, "CHECK(version=3)", "CHECK(version=1)")
		q = strings.ReplaceAll(q, "SELECT 3 WHERE NOT EXISTS", "SELECT 1 WHERE NOT EXISTS")
		q = strings.ReplaceAll(q, ",family_id TEXT REFERENCES oauth_families(id)", "")
		q = strings.ReplaceAll(q, ",approval_authority BLOB", "")
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	owner, device, family, session, task, command := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UnixMilli()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO oauth_families VALUES(?)`, []any{family}},
		{`INSERT INTO oauth_devices VALUES(?,?)`, []any{owner, device}},
		{`INSERT INTO mobile_device_streams(owner_id,device_id,epoch,token_expires_at,expires_at) VALUES(?,?,1,?,?)`, []any{owner, device, now + 60000, now + 60000}},
		{`INSERT INTO mobile_native_sessions VALUES(?,?,?,1,1,'active',X'00',?)`, []any{owner, device, session, now + 60000}},
		{`INSERT INTO mobile_phone_tasks(id,owner_id,device_id,conversation_id,turn_id,history_generation,action,request_digest,caller_family_id,viewer_device_id,state,session_id,session_generation,created_at,expires_at,consent_revision,destination) VALUES(?,?,?,?,?,1,1,X'00',?,?,'running',?,1,?,?,'v1','viewer')`, []any{task, owner, device, uuid.NewString(), uuid.NewString(), family, device, session, now, now + 30000}},
		{`INSERT INTO mobile_phone_commands(id,task_id,owner_id,device_id,server_seq,digest,state,stream_epoch,expires_at,payload_ref) VALUES(?,?,?,?,1,X'00','started',1,?,?)`, []any{command, task, owner, device, now + 30000, command}},
	} {
		if _, err = db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = (&Store{}).migratePhone(ctx, tx); err != nil {
		t.Fatal(err)
	}
	for _, q := range schema {
		if _, err = tx.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version, epoch int
	var revoked, state string
	var expiry int64
	var canceled bool
	if err = db.QueryRow(`SELECT MAX(version) FROM mobile_phone_schema`).Scan(&version); err != nil || version != 3 {
		t.Fatal("schema version", version, err)
	}
	if err = db.QueryRow(`SELECT epoch,expires_at FROM mobile_device_streams`).Scan(&epoch, &expiry); err != nil || epoch != 2 || expiry != 0 {
		t.Fatal("old stream inherited authority", epoch, expiry, err)
	}
	if err = db.QueryRow(`SELECT state FROM mobile_native_sessions`).Scan(&revoked); err != nil || revoked != "revoked" {
		t.Fatal("native grant inferred during migration", err)
	}
	if err = db.QueryRow(`SELECT state,cancellation_requested FROM mobile_phone_tasks`).Scan(&state, &canceled); err != nil || state != "unknown_outcome" || !canceled {
		t.Fatal("started effect replayed or hidden", state, err)
	}
	if err = db.QueryRow(`SELECT state FROM mobile_phone_commands`).Scan(&state); err != nil || state != "unknown_outcome" {
		t.Fatal("old command requeued", err)
	}
}

func TestPhoneV2MigrationAddsExplicitV3Marker(t *testing.T) {
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "v2.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE mobile_phone_schema(version INTEGER PRIMARY KEY CHECK(version=2)); INSERT INTO mobile_phone_schema VALUES(2)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = (&Store{}).migratePhone(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version int
	if err = db.QueryRow(`SELECT MAX(version) FROM mobile_phone_schema`).Scan(&version); err != nil || version != 3 {
		t.Fatal(version, err)
	}
	// A V2 binary's version check rejects this database rather than silently
	// running cleanup that does not know the new foreign-key relationships.
	if _, err = db.Exec(`INSERT INTO mobile_phone_schema VALUES(2)`); err == nil {
		t.Fatal("old schema marker accepted")
	}
}
