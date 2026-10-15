package devices

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"github.com/iulita-ai/iulita/internal/mobile/serverclock"
	"google.golang.org/protobuf/proto"
)

// Options supplies immutable server-owned policies; it never creates native grants.
type Options struct {
	// Now is an optional server clock for deterministic fault simulation.
	Clock      *serverclock.Clock
	Now        func() time.Time
	Models     llm.ProfileInvoker
	Scenarios  []Scenario
	UI, Vision bool
	Origins    OriginResolver
}

func (s *Store) configure(o Options) error {
	if len(o.Scenarios) > 32 {
		return ErrInvalid
	}
	o.Scenarios = append([]Scenario(nil), o.Scenarios...)
	seen := map[string]bool{}
	for i := range o.Scenarios {
		if err := o.Scenarios[i].validate(); err != nil {
			return err
		}
		key := o.Scenarios[i].Binding.ScenarioId
		if seen[key] {
			return ErrInvalid
		}
		seen[key] = true
		o.Scenarios[i] = o.Scenarios[i].clone()
	}
	s.options = o
	return nil
}

var extensionSchema = []string{
	`CREATE TABLE IF NOT EXISTS mobile_ui_snapshots(task_id TEXT NOT NULL REFERENCES mobile_phone_tasks(id),snapshot_id TEXT NOT NULL,digest BLOB NOT NULL,PRIMARY KEY(task_id,snapshot_id))`,
	`CREATE TABLE IF NOT EXISTS mobile_external_delivery(task_id TEXT PRIMARY KEY REFERENCES mobile_phone_tasks(id),last_state TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS mobile_ui_tasks(task_id TEXT PRIMARY KEY REFERENCES mobile_phone_tasks(id),manifest_digest BLOB NOT NULL,envelope_digest BLOB NOT NULL,step INTEGER NOT NULL DEFAULT 0,snapshot_id TEXT NOT NULL DEFAULT '',snapshot_ref TEXT NOT NULL DEFAULT '',lease_until INTEGER NOT NULL,renewed_at INTEGER NOT NULL,hard_until INTEGER NOT NULL,replans INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS mobile_ui_commands(command_id TEXT PRIMARY KEY REFERENCES mobile_phone_commands(id),step INTEGER NOT NULL,snapshot_id TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS mobile_phone_operations(owner_id TEXT NOT NULL,client_id TEXT NOT NULL,operation_id TEXT NOT NULL,kind TEXT NOT NULL,device_id TEXT NOT NULL,digest BLOB NOT NULL,state TEXT NOT NULL,body_ref TEXT NOT NULL,expires_at INTEGER NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(owner_id,client_id,operation_id,kind))`,
	`CREATE TABLE IF NOT EXISTS mobile_processing(offer_id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES mobile_phone_tasks(id),owner_id TEXT NOT NULL,viewer_device_id TEXT NOT NULL,family_id TEXT NOT NULL,digest BLOB NOT NULL,state TEXT NOT NULL,expires_at INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS mobile_artifacts(upload_id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES mobile_phone_tasks(id),owner_id TEXT NOT NULL,device_id TEXT NOT NULL,viewer_device_id TEXT NOT NULL,binding_digest BLOB NOT NULL,binding BLOB NOT NULL,content_digest BLOB NOT NULL,media_type INTEGER NOT NULL,total_bytes INTEGER NOT NULL,received INTEGER NOT NULL DEFAULT 0,complete INTEGER NOT NULL DEFAULT 0,expires_at INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS mobile_artifact_chunks(upload_id TEXT NOT NULL REFERENCES mobile_artifacts(upload_id),offset INTEGER NOT NULL,digest BLOB NOT NULL,size INTEGER NOT NULL,PRIMARY KEY(upload_id,offset))`,
	`CREATE TABLE IF NOT EXISTS mobile_external_bindings(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,device_id TEXT NOT NULL,viewer_device_id TEXT NOT NULL,channel_type TEXT NOT NULL,instance TEXT NOT NULL,chat_id TEXT NOT NULL,conversation_id TEXT NOT NULL,profile_id TEXT NOT NULL,authority BLOB NOT NULL,canonical_revision TEXT NOT NULL,expires_at INTEGER NOT NULL,revoked INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS mobile_external_turns(turn_id TEXT PRIMARY KEY,binding_id TEXT NOT NULL REFERENCES mobile_external_bindings(id))`,
	`CREATE INDEX IF NOT EXISTS mobile_phone_operations_budget ON mobile_phone_operations(owner_id,created_at)`,
	`CREATE INDEX IF NOT EXISTS mobile_processing_task ON mobile_processing(task_id)`,
	`CREATE INDEX IF NOT EXISTS mobile_artifacts_task ON mobile_artifacts(task_id)`,
	`CREATE INDEX IF NOT EXISTS mobile_external_bindings_owner ON mobile_external_bindings(owner_id,expires_at)`,
}

func semantic(message proto.Message) ([]byte, error) {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		return nil, ErrInvalid
	}
	defer clear(data)
	sum := sha256.Sum256(data)
	return sum[:], nil
}
func scope(c *oauthstore.Claims, required string) error {
	if !oauthstore.HasScope(c, required) {
		return ErrUnavailable
	}
	return nil
}
func taskIn(ctx context.Context, tx *sql.Tx, owner, id string) (Task, error) {
	return scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM mobile_phone_tasks WHERE id=? AND owner_id=?`, id, owner))
}
func (s *Store) resultAuthority(ctx context.Context, tx *sql.Tx, c *oauthstore.Claims, id string) (Task, error) {
	t, err := taskIn(ctx, tx, c.Subject, id)
	if err != nil {
		return t, ErrUnavailable
	}
	if t.ViewerDevice != c.DeviceID || t.Cancelled || !s.nativeSessionLive(ctx, tx, t) || !s.uiLeaseLive(ctx, tx, t) || !s.originFamilyLive(ctx, tx, t) || s.chats.OriginAllowed(ctx, t.Owner, t.Conversation, t.Turn, t.Generation) != nil || s.externalLive(ctx, tx, t) != nil {
		return t, ErrUnavailable
	}
	return t, nil
}
func metadata(v any) ([]byte, error) { return json.Marshal(v) }
func expiryBound(t time.Time, others ...time.Time) time.Time {
	for _, other := range others {
		if other.Before(t) {
			t = other
		}
	}
	return t.UTC().Truncate(time.Millisecond)
}

// SupportsUI reports whether server-owned scenario policy is installed and enabled.
func (s *Store) SupportsUI() bool { return s != nil && s.options.UI && len(s.options.Scenarios) > 0 }

// SupportsVision requires the separate capture feature and a UI policy registry.
func (s *Store) SupportsVision() bool { return s.SupportsUI() && s.options.Vision }

func operationBudget(ctx context.Context, tx *sql.Tx, owner string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_phone_operations WHERE owner_id=? AND created_at>?`, owner, time.Now().Add(-7*24*time.Hour).UnixMilli()).Scan(&count); err != nil {
		return err
	}
	if count >= 10000 {
		return ErrBusy
	}
	return nil
}

func (s *Store) clock() time.Time {
	if s.options.Now != nil {
		return s.options.Now()
	}
	return time.Now()
}

func (s *Store) clockID() string {
	if s.options.Clock == nil {
		return ""
	}
	return s.options.Clock.ID()
}
