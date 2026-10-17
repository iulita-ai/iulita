// Package oauthstore implements persistent mobile OAuth storage, separate from
// dashboard authentication. It registers no endpoints and supplies no login flow.
package oauthstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
)

// Errors distinguish invalid input, stale authority and unavailable resources.
var (
	// ErrInvalid rejects invalid input or authority.
	ErrInvalid     = errors.New("invalid mobile OAuth storage input")
	ErrNotFound    = errors.New("mobile OAuth resource not found")
	ErrConflict    = errors.New("mobile OAuth storage conflict")
	ErrTransaction = errors.New("mobile OAuth request transaction required")
)

// Store owns persistence or bounded content for this mobile subsystem.
type Store struct {
	db    *sql.DB
	realm *Realm
}

const schemaVersion = 4

var schema = []string{
	`CREATE TABLE oauth_schema(version INTEGER PRIMARY KEY CHECK(version=2))`,
	`INSERT INTO oauth_schema VALUES(2)`,
	`CREATE TABLE oauth_realm(id TEXT PRIMARY KEY,kid TEXT NOT NULL,signing_epoch INTEGER NOT NULL CHECK(signing_epoch>0),binding BLOB NOT NULL)`,
	`CREATE TABLE oauth_owners(id TEXT PRIMARY KEY,epoch INTEGER NOT NULL DEFAULT 1 CHECK(epoch>0))`,
	`CREATE TABLE oauth_clients(id TEXT PRIMARY KEY,data BLOB NOT NULL)`,
	`CREATE TABLE oauth_devices(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL REFERENCES oauth_owners(id),thumbprint TEXT NOT NULL,epoch INTEGER NOT NULL DEFAULT 1 CHECK(epoch>0),revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1)),activated INTEGER NOT NULL DEFAULT 0 CHECK(activated IN (0,1)),UNIQUE(owner_id,id))`,
	`CREATE TABLE oauth_families(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,device_id TEXT NOT NULL,client_id TEXT NOT NULL REFERENCES oauth_clients(id),stage TEXT NOT NULL CHECK(stage IN ('pending_activation','provisional_recovery','active')),owner_epoch INTEGER NOT NULL,device_epoch INTEGER NOT NULL,signing_epoch INTEGER NOT NULL,auth_time INTEGER NOT NULL,created_at INTEGER NOT NULL,expires_at INTEGER NOT NULL,deadline INTEGER NOT NULL,generation INTEGER NOT NULL DEFAULT 0 CHECK(generation>=0),revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1)),consent_scopes BLOB NOT NULL,FOREIGN KEY(owner_id,device_id) REFERENCES oauth_devices(owner_id,id),UNIQUE(owner_id,device_id,id))`,
	`CREATE TABLE oauth_sessions(kind TEXT NOT NULL CHECK(kind IN ('code','pkce','access','refresh')),key_hash BLOB NOT NULL CHECK(length(key_hash)=32),family_id TEXT NOT NULL REFERENCES oauth_families(id),stage TEXT NOT NULL CHECK(stage IN ('pending_activation','provisional_recovery','active')),generation INTEGER NOT NULL CHECK(generation>=0),active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),expires_at INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,key_hash))`,
	`CREATE INDEX oauth_sessions_family ON oauth_sessions(family_id)`,
	`CREATE TABLE oauth_nonces(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,device_id TEXT NOT NULL,family_id TEXT NOT NULL,purpose TEXT NOT NULL CHECK(purpose IN ('activate_device','recover_device')),key_thumbprint TEXT NOT NULL,nonce_hash BLOB NOT NULL UNIQUE CHECK(length(nonce_hash)=32),expires_at INTEGER NOT NULL,consumed INTEGER NOT NULL DEFAULT 0 CHECK(consumed IN (0,1)),FOREIGN KEY(owner_id,device_id,family_id) REFERENCES oauth_families(owner_id,device_id,id))`,
}

func (r *Realm) binding() []byte {
	return r.digest("realm-binding.v1", r.kid+"\x00"+base64.RawURLEncoding.EncodeToString(r.hmacKey))
}

// Initialize creates a new empty realm DB explicitly. Existing databases are
// checked, never reset. Open fails if a database or its key file disappeared.
func Initialize(ctx context.Context, path string, realm *Realm) (*Store, error) {
	if realm == nil || privateDir(filepath.Dir(path)) != nil {
		return nil, ErrKeys
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // Path comes from explicit local runtime configuration, not remote input.
	if os.IsExist(err) {
		return Open(ctx, path, realm)
	}
	if err != nil {
		return nil, err
	}
	if checkErr1 := f.Close(); checkErr1 != nil {
		return nil, checkErr1
	}
	db, err := sqlitedb.Open(ctx, path, false)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, realm: realm}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	for _, stmt := range append(append([]string{}, schema...), browserSchema...) {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			tx.Rollback() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			db.Close()    //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return nil, err
		}
	}
	for _, stmt := range lifecycleSchema {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			tx.Rollback() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			db.Close()    //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return nil, err
		}
	}
	for _, stmt := range clientsSchema {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			tx.Rollback() //nolint:errcheck,gosec // Preserve initialization error.
			db.Close()    //nolint:errcheck,gosec // Preserve initialization error.
			return nil, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO oauth_realm VALUES(?,?,?,?)`, realm.id, realm.kid, realm.epoch, realm.binding())
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		tx.Rollback() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		db.Close()    //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, err
	}
	return s, nil
}

// Open opens the store with its required schema and safety settings.
func Open(ctx context.Context, path string, realm *Realm) (*Store, error) {
	if realm == nil || privateDir(filepath.Dir(path)) != nil {
		return nil, ErrKeys
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrKeys
	}
	db, err := sqlitedb.Open(ctx, path, false)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, realm: realm}
	var version int
	var id, kid string
	var epoch uint64
	var binding []byte
	if err = db.QueryRowContext(ctx, `SELECT MAX(version) FROM oauth_schema`).Scan(&version); err == nil {
		err = db.QueryRowContext(ctx, `SELECT id,kid,signing_epoch,binding FROM oauth_realm`).Scan(&id, &kid, &epoch, &binding)
	}
	if err == nil && version == 1 && id == realm.id && kid == realm.kid && epoch == realm.epoch && equalHash(binding, realm.binding()) {
		err = s.migrateBrowser(ctx)
		if err == nil {
			version = 2
		}
	}
	if err == nil && version == 2 && id == realm.id && kid == realm.kid && epoch == realm.epoch && equalHash(binding, realm.binding()) {
		err = s.migrateLifecycle()
		if err == nil {
			version = 3
		}
	}
	if err == nil && version == 3 && id == realm.id && kid == realm.kid && epoch == realm.epoch && equalHash(binding, realm.binding()) {
		err = s.migrateClients(ctx)
		if err == nil {
			version = 4
		}
	}
	if err != nil || version != schemaVersion || id != realm.id || kid != realm.kid || epoch != realm.epoch || !equalHash(binding, realm.binding()) {
		db.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		return nil, ErrKeys
	}
	return s, nil
}

// Close releases the resources owned by this instance.
func (s *Store) Close() error    { return s.db.Close() }
func boundedEpoch(v uint64) bool { return v > 0 && v < 1<<62 }

// EnsureOwner initializes or checks an owner security record.
func (s *Store) EnsureOwner(ctx context.Context, owner string) error {
	if !canonicalID(owner) {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_owners(id) VALUES(?) ON CONFLICT DO NOTHING`, owner)
	return err
}

// Device records an installation and its current security epoch.
type Device struct {
	ID, OwnerID, KeyThumbprint string
	Epoch                      uint64
	Revoked                    bool
	Activated                  bool
	Label                      string
	PendingExpiresAt           int64
}

// RegisterDevice registers an installation under its owner.
func (s *Store) RegisterDevice(ctx context.Context, owner, id, thumbprint string) error {
	b, err := base64.RawURLEncoding.DecodeString(thumbprint)
	if !canonicalID(owner) || !canonicalID(id) || err != nil || len(b) != 32 {
		return ErrInvalid
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_devices(id,owner_id,thumbprint) VALUES(?,?,?)`, id, owner, thumbprint)
	return err
}

// Device returns the owned device security record.
func (s *Store) Device(ctx context.Context, owner, id string) (Device, error) {
	var d Device
	err := s.db.QueryRowContext(ctx, `SELECT id,owner_id,thumbprint,epoch,revoked,activated,label,pending_expires_at FROM oauth_devices WHERE owner_id=? AND id=?`, owner, id).Scan(&d.ID, &d.OwnerID, &d.KeyThumbprint, &d.Epoch, &d.Revoked, &d.Activated, &d.Label, &d.PendingExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return d, err
}

// Family records a rotating credential family and its security epochs.
type Family struct {
	ID, OwnerID, DeviceID, ClientID                   string
	Stage                                             Stage
	OwnerEpoch, DeviceEpoch, SigningEpoch, Generation uint64
	AuthTime, CreatedAt, ExpiresAt, Deadline          time.Time
	ConsentScopes                                     []string
	Revoked                                           bool
}

func scanFamily(row *sql.Row) (Family, error) {
	var f Family
	var auth, created, expires, deadline int64
	var scopes []byte
	err := row.Scan(&f.ID, &f.OwnerID, &f.DeviceID, &f.ClientID, &f.Stage, &f.OwnerEpoch, &f.DeviceEpoch, &f.SigningEpoch, &f.Generation, &auth, &created, &expires, &deadline, &f.Revoked, &scopes)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	if err != nil {
		return f, err
	}
	f.AuthTime = time.Unix(auth, 0).UTC()
	f.CreatedAt = time.Unix(created, 0).UTC()
	f.ExpiresAt = time.Unix(expires, 0).UTC()
	if deadline != 0 {
		f.Deadline = time.Unix(deadline, 0).UTC()
	}
	err = json.Unmarshal(scopes, &f.ConsentScopes)
	return f, err
}

const familyColumns = `id,owner_id,device_id,client_id,stage,owner_epoch,device_epoch,signing_epoch,generation,auth_time,created_at,expires_at,deadline,revoked,consent_scopes`

// Family returns a persistent credential-family record.
func (s *Store) Family(ctx context.Context, owner, id string) (Family, error) {
	return scanFamily(s.db.QueryRowContext(ctx, `SELECT `+familyColumns+` FROM oauth_families WHERE owner_id=? AND id=?`, owner, id))
}

// CreateFamily creates a credential family bound to current account and device state.
func (s *Store) CreateFamily(ctx context.Context, f Family) error {
	now := time.Now().Truncate(time.Second)
	if !canonicalID(f.ID) || !canonicalID(f.OwnerID) || !canonicalID(f.DeviceID) || f.ClientID == "" || !f.Stage.valid() || f.AuthTime.IsZero() || f.AuthTime.After(now.Add(time.Second)) || !f.ExpiresAt.After(now) || f.ExpiresAt.After(now.Add(30*24*time.Hour)) || f.Revoked || f.Generation != 0 {
		return ErrInvalid
	}
	deadline := int64(0)
	if f.Stage != Active {
		if !f.Deadline.After(now) || f.Deadline.After(now.Add(f.Stage.ttl())) || f.Deadline.After(f.ExpiresAt) {
			return ErrInvalid
		}
		deadline = f.Deadline.Unix()
	} else if !f.Deadline.IsZero() {
		return ErrInvalid
	}
	return s.WithRequest(ctx, func(ctx context.Context) error {
		q, err := s.transaction(ctx)
		if err != nil {
			return err
		}
		if ownerErr := checkClientOwnerIn(ctx, q, f.ClientID, f.OwnerID); ownerErr != nil {
			return ownerErr
		}
		var ownerEpoch, deviceEpoch uint64
		var revoked bool
		if err = q.QueryRowContext(ctx, `SELECT o.epoch,d.epoch,d.revoked FROM oauth_owners o JOIN oauth_devices d ON d.owner_id=o.id WHERE o.id=? AND d.id=?`, f.OwnerID, f.DeviceID).Scan(&ownerEpoch, &deviceEpoch, &revoked); err != nil || revoked {
			return ErrNotFound
		}
		client, err := s.GetClient(ctx, f.ClientID)
		if err != nil {
			return err
		}
		if !validScopes(f.ConsentScopes) || len(f.ConsentScopes) == 0 || !subset(f.ConsentScopes, client.GetScopes()) {
			return ErrInvalid
		}
		scopes, err := json.Marshal(f.ConsentScopes)
		if err != nil {
			return ErrInvalid
		}
		_, err = q.ExecContext(ctx, `INSERT INTO oauth_families(id,owner_id,device_id,client_id,stage,owner_epoch,device_epoch,signing_epoch,auth_time,created_at,expires_at,deadline,consent_scopes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, f.ID, f.OwnerID, f.DeviceID, f.ClientID, f.Stage, ownerEpoch, deviceEpoch, s.realm.epoch, f.AuthTime.Unix(), now.Unix(), f.ExpiresAt.Unix(), deadline, scopes)
		return err
	})
}
func (s *Store) familyIn(ctx context.Context, id string) (Family, error) {
	q, err := s.transaction(ctx)
	if err != nil {
		return Family{}, err
	}
	return scanFamily(q.QueryRowContext(ctx, `SELECT `+familyColumns+` FROM oauth_families WHERE id=?`, id))
}
func (s *Store) live(ctx context.Context, f Family) error {
	if f.Revoked || !f.ExpiresAt.After(time.Now()) || (!f.Deadline.IsZero() && !f.Deadline.After(time.Now())) || f.SigningEpoch != s.realm.epoch {
		return fositeInvalid()
	}
	q, err := s.transaction(ctx)
	if err != nil {
		return err
	}
	if ownerErr := checkClientOwnerIn(ctx, q, f.ClientID, f.OwnerID); ownerErr != nil {
		return ownerErr
	}
	var ownerEpoch, deviceEpoch uint64
	var revoked bool
	if err = q.QueryRowContext(ctx, `SELECT o.epoch,d.epoch,d.revoked FROM oauth_owners o JOIN oauth_devices d ON d.owner_id=o.id WHERE o.id=? AND d.id=?`, f.OwnerID, f.DeviceID).Scan(&ownerEpoch, &deviceEpoch, &revoked); err != nil || revoked || ownerEpoch != f.OwnerEpoch || deviceEpoch != f.DeviceEpoch {
		return fositeInvalid()
	}
	return nil
}

// RevokeFamily revokes all authority rooted in the credential family.
func (s *Store) RevokeFamily(ctx context.Context, owner, id string) error {
	return s.WithRequest(ctx, func(ctx context.Context) error {
		f, err := s.familyIn(ctx, id)
		if err != nil || f.OwnerID != owner {
			return ErrNotFound
		}
		return s.revoke(ctx, id)
	})
}

// BumpOwnerEpoch invalidates prior owner authority by advancing its security epoch.
func (s *Store) BumpOwnerEpoch(ctx context.Context, owner string, expected uint64) error {
	if !boundedEpoch(expected) || !canonicalID(owner) {
		return ErrInvalid
	}
	return s.WithRequest(ctx, func(ctx context.Context) error {
		q, operationErr1 := s.transaction(ctx)
		if operationErr1 != nil {
			return operationErr1
		}
		res, err := q.ExecContext(ctx, `UPDATE oauth_owners SET epoch=epoch+1 WHERE id=? AND epoch=?`, owner, expected)
		if err != nil {
			return err
		}
		n, operationErr2 := res.RowsAffected()
		if operationErr2 != nil {
			return operationErr2
		}
		if n != 1 {
			return ErrConflict
		}
		_, err = q.ExecContext(ctx, `UPDATE oauth_families SET revoked=1 WHERE owner_id=?`, owner)
		return err
	})
}

// RevokeDevice revokes the owned device and its credential families.
func (s *Store) RevokeDevice(ctx context.Context, owner, id string, expected uint64) error {
	if !boundedEpoch(expected) {
		return ErrInvalid
	}
	return s.WithRequest(ctx, func(ctx context.Context) error { return s.revokeDeviceIn(ctx, owner, id, expected) })
}
func (s *Store) revokeDeviceIn(ctx context.Context, owner, id string, expected uint64) error {
	q, err := s.transaction(ctx)
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `UPDATE oauth_devices SET epoch=epoch+1,revoked=1 WHERE owner_id=? AND id=? AND epoch=?`, owner, id, expected)
	if err != nil {
		return err
	}
	n, operationErr3 := res.RowsAffected()
	if operationErr3 != nil {
		return operationErr3
	}
	if n != 1 {
		return ErrNotFound
	}
	if _, err = q.ExecContext(ctx, `UPDATE oauth_families SET revoked=1 WHERE owner_id=? AND device_id=?`, owner, id); err != nil {
		return err
	}
	return invalidateDeviceState(ctx, q, owner, id)
}
func (s *Store) revoke(ctx context.Context, id string) error {
	q, err := s.transaction(ctx)
	if err != nil {
		return err
	}
	if _, err = q.ExecContext(ctx, `UPDATE oauth_families SET revoked=1 WHERE id=?`, id); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE oauth_sessions SET active=0 WHERE family_id=?`, id)
	return err
}
