// Package devices owns durable phone task metadata in the separate OAuth DB.
// Raw command envelopes/observations are references to ephemeral RAM only.
package devices

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/ephemeral"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
)

// ErrUnavailable indicates that a device resource or its authority is unavailable.
var ErrUnavailable = errors.New("mobile device unavailable")

// ErrEpoch rejects a stale control-stream generation.
var ErrEpoch = errors.New("mobile control epoch superseded")

// ErrInvalid rejects invalid input or authority.
var ErrInvalid = errors.New("invalid mobile device transition")

// ErrBusy indicates that current capacity or concurrency limits were reached.
var ErrBusy = errors.New("mobile device busy")

// PolicyRevision identifies the supported native-device policy contract.
const PolicyRevision = "mobile-system-v1"

// Store owns persistence or bounded content for this mobile subsystem.
type Store struct {
	db       *sql.DB
	auth     *oauthstore.Store
	accounts browseroauth.Accounts
	chats    *conversations.Repository
	content  *ephemeral.Store
	history  *ephemeral.Store
	options  Options
}

// Identity binds a device operation to verified mobile credentials.
type Identity struct {
	Token  string
	Claims *oauthstore.Claims
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS mobile_telemetry_records(owner_id TEXT NOT NULL,device_id TEXT NOT NULL,id TEXT NOT NULL,collection_id TEXT NOT NULL,family_id TEXT NOT NULL,category TEXT NOT NULL,digest BLOB NOT NULL,captured_at INTEGER NOT NULL,expires_at INTEGER NOT NULL,received_at INTEGER NOT NULL,bytes INTEGER NOT NULL,PRIMARY KEY(owner_id,device_id,id),FOREIGN KEY(owner_id,device_id) REFERENCES oauth_devices(owner_id,id))`,
	`CREATE INDEX IF NOT EXISTS mobile_telemetry_history ON mobile_telemetry_records(owner_id,device_id,family_id,captured_at,id)`,
	`CREATE INDEX IF NOT EXISTS mobile_telemetry_received ON mobile_telemetry_records(owner_id,device_id,received_at)`,
	`CREATE TABLE IF NOT EXISTS mobile_phone_schema(version INTEGER PRIMARY KEY CHECK(version=3))`,
	`INSERT INTO mobile_phone_schema SELECT 3 WHERE NOT EXISTS(SELECT 1 FROM mobile_phone_schema)`,
	`CREATE TABLE IF NOT EXISTS mobile_device_streams(owner_id TEXT NOT NULL,device_id TEXT NOT NULL,epoch INTEGER NOT NULL,family_id TEXT REFERENCES oauth_families(id),server_seq INTEGER NOT NULL DEFAULT 0,token_expires_at INTEGER NOT NULL,expires_at INTEGER NOT NULL,capabilities BLOB,PRIMARY KEY(owner_id,device_id),FOREIGN KEY(owner_id,device_id) REFERENCES oauth_devices(owner_id,id))`,
	`CREATE TABLE IF NOT EXISTS mobile_native_sessions(owner_id TEXT NOT NULL,device_id TEXT NOT NULL,id TEXT NOT NULL,generation INTEGER NOT NULL,stream_epoch INTEGER NOT NULL,family_id TEXT REFERENCES oauth_families(id),state TEXT NOT NULL,metadata BLOB NOT NULL,expires_at INTEGER NOT NULL,PRIMARY KEY(owner_id,device_id,id),FOREIGN KEY(owner_id,device_id) REFERENCES oauth_devices(owner_id,id))`,
	`CREATE TABLE IF NOT EXISTS mobile_phone_tasks(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,device_id TEXT NOT NULL,conversation_id TEXT NOT NULL,turn_id TEXT NOT NULL,history_generation INTEGER NOT NULL,action INTEGER NOT NULL,request_digest BLOB NOT NULL,caller_family_id TEXT NOT NULL REFERENCES oauth_families(id),viewer_device_id TEXT NOT NULL,state TEXT NOT NULL,cancellation_requested INTEGER NOT NULL DEFAULT 0,reason INTEGER NOT NULL DEFAULT 0,session_id TEXT NOT NULL,session_generation INTEGER NOT NULL,created_at INTEGER NOT NULL,expires_at INTEGER NOT NULL,consent_revision TEXT NOT NULL,destination TEXT NOT NULL,finished_at INTEGER NOT NULL DEFAULT 0,approval_authority BLOB,FOREIGN KEY(owner_id,device_id) REFERENCES oauth_devices(owner_id,id),FOREIGN KEY(owner_id,viewer_device_id) REFERENCES oauth_devices(owner_id,id))`,
	`CREATE TABLE IF NOT EXISTS mobile_phone_commands(id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES mobile_phone_tasks(id),owner_id TEXT NOT NULL,device_id TEXT NOT NULL,server_seq INTEGER NOT NULL,digest BLOB NOT NULL,state TEXT NOT NULL,stream_epoch INTEGER NOT NULL,coordinator_epoch INTEGER NOT NULL DEFAULT 1,expires_at INTEGER NOT NULL,payload_ref TEXT NOT NULL,UNIQUE(owner_id,device_id,server_seq))`,
	`CREATE TABLE IF NOT EXISTS mobile_phone_receipts(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,device_id TEXT NOT NULL,command_id TEXT NOT NULL REFERENCES mobile_phone_commands(id),digest BLOB NOT NULL,created_at INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS mobile_phone_events(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,owner_id TEXT NOT NULL,conversation_id TEXT NOT NULL,turn_id TEXT NOT NULL,history_generation INTEGER NOT NULL,task_id TEXT NOT NULL REFERENCES mobile_phone_tasks(id),state TEXT NOT NULL,created_at INTEGER NOT NULL,published INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS mobile_phone_observations(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,device_id TEXT NOT NULL,task_id TEXT NOT NULL REFERENCES mobile_phone_tasks(id),category INTEGER NOT NULL,digest BLOB NOT NULL,expires_at INTEGER NOT NULL,created_at INTEGER NOT NULL,bytes INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS mobile_sync_operations(owner_id TEXT NOT NULL,client_id TEXT NOT NULL,operation_id TEXT NOT NULL,device_id TEXT NOT NULL,digest BLOB NOT NULL,accepted_ids BLOB NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(owner_id,client_id,operation_id))`,
	`CREATE INDEX IF NOT EXISTS mobile_sync_window ON mobile_sync_operations(owner_id,created_at)`,
	`CREATE TABLE IF NOT EXISTS mobile_sync_task_operations(owner_id TEXT NOT NULL,task_id TEXT NOT NULL REFERENCES mobile_phone_tasks(id),operation_id TEXT NOT NULL,PRIMARY KEY(owner_id,task_id,operation_id))`,
	`CREATE INDEX IF NOT EXISTS mobile_phone_queue ON mobile_phone_commands(owner_id,device_id,stream_epoch,server_seq) WHERE state='queued'`,
	`CREATE INDEX IF NOT EXISTS mobile_phone_active ON mobile_phone_tasks(owner_id,device_id,state,expires_at)`,
	`CREATE INDEX IF NOT EXISTS mobile_phone_results ON mobile_phone_observations(owner_id,device_id,task_id)`,
	`CREATE INDEX IF NOT EXISTS mobile_phone_receipt_command ON mobile_phone_receipts(command_id,owner_id,device_id)`,
	`CREATE INDEX IF NOT EXISTS mobile_phone_event_pending ON mobile_phone_events(seq) WHERE published=0`,
	`CREATE INDEX IF NOT EXISTS mobile_native_active ON mobile_native_sessions(owner_id,device_id,state,stream_epoch,expires_at)`,
}

// Open opens the store with its required schema and safety settings.
func Open(ctx context.Context, path string, auth *oauthstore.Store, accounts browseroauth.Accounts, chats *conversations.Repository, content *ephemeral.Store, options ...Options) (*Store, error) {
	if auth == nil || accounts == nil || chats == nil || content == nil {
		return nil, ErrInvalid
	}
	history, historyErr := ephemeral.NewTelemetry()
	if historyErr != nil {
		return nil, historyErr
	}
	s := &Store{auth: auth, accounts: accounts, chats: chats, content: content, history: history}
	if len(options) > 1 {
		return nil, ErrInvalid
	}
	if len(options) == 1 {
		if err := s.configure(options[0]); err != nil {
			return nil, err
		}
	}
	db, stepErr2 := sqlitedb.Open(ctx, path, false)
	if stepErr2 != nil {
		return nil, stepErr2
	}
	s.db = db
	tx, stepErr2 := db.BeginTx(ctx, nil)
	if stepErr2 != nil {
		db.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, stepErr2
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	var version int
	if stepErr2 = tx.QueryRowContext(ctx, `SELECT MAX(version) FROM oauth_schema`).Scan(&version); stepErr2 != nil || version != 4 {
		db.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, ErrInvalid
	}
	if stepErr2 = s.migratePhone(ctx, tx); stepErr2 != nil {
		tx.Rollback() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		db.Close()    //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, stepErr2
	}
	for _, q := range append(append([]string{}, schema...), extensionSchema...) {
		if _, stepErr2 = tx.ExecContext(ctx, q); stepErr2 != nil {
			tx.Rollback() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			db.Close()    //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return nil, stepErr2
		}
	}
	if tx.QueryRowContext(ctx, `SELECT MAX(version) FROM mobile_phone_schema`).Scan(&version) != nil || version != 3 {
		tx.Rollback() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		db.Close()    //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, ErrInvalid
	}
	if stepErr2 = tx.Commit(); stepErr2 != nil {
		db.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, stepErr2
	}
	return s, nil
}

// Close releases the resources owned by this instance.
func (s *Store) Close() error { return s.db.Close() }
func validID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

// Every transition reserves the account authority FIRST, then shares one OAuth
// DB write transaction for live token/epochs and command state. Lock order is
// fixed; account mutation and parent clear cannot slip through that boundary.
func (s *Store) with(ctx context.Context, identity Identity, fn func(*sql.Tx, *oauthstore.Claims) error) error {
	if identity.Claims == nil {
		return ErrInvalid
	}
	return s.accounts.Guard(ctx, identity.Claims.Subject, func(a browseroauth.Account) error {
		available := a.ID == identity.Claims.Subject && !a.Disabled && !a.MustChangePassword
		if err := s.auth.SyncAccount(ctx, a.ID, a.CredentialVersion, available); err != nil {
			return err
		}
		if !available {
			return ErrUnavailable
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
		claims, err := s.auth.ValidateIn(ctx, tx, identity.Token)
		if err != nil || claims.Stage != oauthstore.Active {
			return ErrUnavailable
		}
		if checkErr1 := fn(tx, claims); checkErr1 != nil {
			return checkErr1
		}
		return tx.Commit()
	})
}
func streamLive(ctx context.Context, tx *sql.Tx, c *oauthstore.Claims, epoch uint64) error {
	var n int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_device_streams WHERE owner_id=? AND device_id=? AND epoch=? AND family_id=? AND expires_at>?`, c.Subject, c.DeviceID, epoch, c.FamilyID, time.Now().UnixMilli()).Scan(&n) != nil || n != 1 {
		return ErrEpoch
	}
	return nil
}
func (s *Store) binding(owner, device, task, destination, consent, category string) ephemeral.Binding {
	return ephemeral.Binding{Owner: owner, Device: device, Task: task, Destination: destination, ConsentRevision: consent, Category: category}
}
func isSystem(kind v1.ActionKind) bool {
	return kind >= v1.ActionKind_ACTION_KIND_READ_STATUS && kind <= v1.ActionKind_ACTION_KIND_OPEN_NAVIGATION
}
