package oauthstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrBrowserFlow rejects an expired or invalid browser authorization flow.
var ErrBrowserFlow = errors.New("invalid or expired OAuth browser flow")
var browserSchema = []string{
	`CREATE TABLE oauth_browser_flows(id TEXT PRIMARY KEY,browser_hash BLOB NOT NULL,csrf_hash BLOB NOT NULL,request BLOB NOT NULL,phase TEXT NOT NULL CHECK(phase IN ('login','consent','consumed')),owner_id TEXT NOT NULL DEFAULT '',credential_stamp BLOB,auth_time INTEGER NOT NULL DEFAULT 0,expires_at INTEGER NOT NULL)`,
	`CREATE INDEX oauth_browser_binding ON oauth_browser_flows(browser_hash,expires_at)`,
	`CREATE TABLE oauth_browser_limits(subject BLOB NOT NULL,window_start INTEGER NOT NULL,count INTEGER NOT NULL,PRIMARY KEY(subject,window_start))`,
}

func (s *Store) migrateBrowser(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	// Another process may have migrated while this opener waited for the lock.
	var version int
	if checkErr4 := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM oauth_schema`).Scan(&version); checkErr4 != nil {
		return checkErr4
	}
	if version == 2 {
		return tx.Commit()
	}
	if version != 1 {
		return ErrInvalid
	}
	// A stored installation hint alone never proves activation or key possession.
	if _, err = tx.ExecContext(ctx, `ALTER TABLE oauth_devices ADD COLUMN activated INTEGER NOT NULL DEFAULT 0 CHECK(activated IN (0,1))`); err != nil {
		return err
	}
	for _, stmt := range browserSchema {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	for _, stmt := range []string{`ALTER TABLE oauth_schema RENAME TO oauth_schema_v1`, `CREATE TABLE oauth_schema(version INTEGER PRIMARY KEY CHECK(version=2))`, `INSERT INTO oauth_schema VALUES(2)`, `DROP TABLE oauth_schema_v1`} {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Secret generates a cryptographically random protocol secret.
func Secret() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidSecret checks the canonical protocol-secret encoding.
func ValidSecret(s string) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}

// Issuer returns the configured realm issuer.
func (s *Store) Issuer() string { return s.realm.issuer }

// Audience returns the configured mobile token audience.
func (s *Store) Audience() string { return s.realm.audience }

// CredentialStamp returns a keyed account-credential change stamp.
func (s *Store) CredentialStamp(version string) []byte {
	return s.realm.digest("browser.credentials", version)
}

// Authorization contains the validated browser authorization request.
type Authorization struct {
	ClientID, RedirectURI, Scope, State, Challenge, KeyThumbprint, DeviceHint string
	FreshAuth                                                                 bool
}

// BrowserFlow records an expiring browser login and consent flow.
type BrowserFlow struct {
	ID                  string
	Request             Authorization
	Phase, OwnerID      string
	CredentialStamp     []byte
	AuthTime, ExpiresAt time.Time
}

func scanFlow(row *sql.Row) (BrowserFlow, error) {
	var f BrowserFlow
	var data []byte
	var auth, expiry int64
	err := row.Scan(&f.ID, &data, &f.Phase, &f.OwnerID, &f.CredentialStamp, &auth, &expiry)
	if err != nil {
		return f, ErrBrowserFlow
	}
	if json.Unmarshal(data, &f.Request) != nil {
		return f, ErrBrowserFlow
	}
	f.AuthTime = time.Unix(auth, 0).UTC()
	f.ExpiresAt = time.Unix(expiry, 0).UTC()
	return f, nil
}
func (s *Store) flow(ctx context.Context, cookie, id, csrf, phase string) (BrowserFlow, error) {
	q, err := s.transaction(ctx)
	if err != nil {
		return BrowserFlow{}, err
	}
	if !ValidSecret(cookie) || !canonicalID(id) || !ValidSecret(csrf) {
		return BrowserFlow{}, ErrBrowserFlow
	}
	f, err := scanFlow(q.QueryRowContext(ctx, `SELECT id,request,phase,owner_id,credential_stamp,auth_time,expires_at FROM oauth_browser_flows WHERE id=? AND browser_hash=? AND expires_at>?`, id, s.realm.digest("browser.cookie", cookie), time.Now().Unix()))
	if err != nil || f.Phase != phase {
		return BrowserFlow{}, ErrBrowserFlow
	}
	if csrf != "" {
		var hash []byte
		if !ValidSecret(csrf) {
			return BrowserFlow{}, ErrBrowserFlow
		}
		if err = q.QueryRowContext(ctx, `SELECT csrf_hash FROM oauth_browser_flows WHERE id=?`, id).Scan(&hash); err != nil || !equalHash(hash, s.realm.digest("browser.csrf", csrf)) {
			return BrowserFlow{}, ErrBrowserFlow
		}
	}
	return f, nil
}

// CreateBrowserFlow persists an expiring browser login and consent flow.
func (s *Store) CreateBrowserFlow(ctx context.Context, cookie string, a Authorization) (BrowserFlow, string, error) {
	if !ValidSecret(cookie) || !ValidSecret(a.Challenge) || !ValidSecret(a.KeyThumbprint) || a.ClientID == "" || a.RedirectURI == "" || a.State == "" {
		return BrowserFlow{}, "", ErrInvalid
	}
	csrf, err := Secret()
	if err != nil {
		return BrowserFlow{}, "", err
	}
	f := BrowserFlow{ID: uuid.NewString(), Request: a, Phase: "login", ExpiresAt: time.Now().Add(10 * time.Minute).UTC()}
	data, err := json.Marshal(a)
	if err != nil {
		return f, "", err
	}
	err = s.WithRequest(ctx, func(ctx context.Context) error {
		q, operationErr1 := s.transaction(ctx)
		if operationErr1 != nil {
			return operationErr1
		}
		if _, stepErr1 := q.ExecContext(ctx, `DELETE FROM oauth_browser_flows WHERE id IN (SELECT id FROM oauth_browser_flows WHERE expires_at<=? LIMIT 100)`, time.Now().Unix()); stepErr1 != nil {
			return stepErr1
		}
		_, stepErr2 := q.ExecContext(ctx, `INSERT INTO oauth_browser_flows(id,browser_hash,csrf_hash,request,phase,expires_at) VALUES(?,?,?,?,'login',?)`, f.ID, s.realm.digest("browser.cookie", cookie), s.realm.digest("browser.csrf", csrf), data, f.ExpiresAt.Unix())
		return stepErr2
	})
	return f, csrf, err
}

// CheckBrowserFlow checks browser-flow binding and lifetime.
func (s *Store) CheckBrowserFlow(ctx context.Context, cookie, id, csrf, phase string) (BrowserFlow, error) {
	var f BrowserFlow
	err := s.WithRequest(ctx, func(ctx context.Context) error {
		var err error
		f, err = s.flow(ctx, cookie, id, csrf, phase)
		return err
	})
	return f, err
}

// LoginBrowserFlow binds authenticated account state to a browser flow.
func (s *Store) LoginBrowserFlow(ctx context.Context, cookie, id, csrf, owner string, stamp []byte, authTime time.Time) (updatedFlow BrowserFlow, browserCookie, csrfToken string, returnErr error) {
	if !canonicalID(owner) || len(stamp) != 32 || authTime.After(time.Now().Add(time.Second)) || authTime.Before(time.Now().Add(-time.Minute)) {
		return BrowserFlow{}, "", "", ErrInvalid
	}
	rotated, err := Secret()
	if err != nil {
		return BrowserFlow{}, "", "", err
	}
	nextCSRF, err := Secret()
	if err != nil {
		return BrowserFlow{}, "", "", err
	}
	var f BrowserFlow
	err = s.WithRequest(ctx, func(ctx context.Context) error {
		var stepErr3 error
		f, stepErr3 = s.flow(ctx, cookie, id, csrf, "login")
		if stepErr3 != nil {
			return stepErr3
		}
		q, operationErr2 := s.transaction(ctx)
		if operationErr2 != nil {
			return operationErr2
		}
		// Rebind concurrent tabs as well: the cookie proves browser binding only,
		// never an authenticated owner. Each flow retains its own login and CSRF.
		if _, stepErr3 = q.ExecContext(ctx, `UPDATE oauth_browser_flows SET browser_hash=? WHERE browser_hash=? AND expires_at>?`, s.realm.digest("browser.cookie", rotated), s.realm.digest("browser.cookie", cookie), time.Now().Unix()); stepErr3 != nil {
			return stepErr3
		}
		_, stepErr3 = q.ExecContext(ctx, `UPDATE oauth_browser_flows SET phase='consent',owner_id=?,credential_stamp=?,auth_time=?,csrf_hash=? WHERE id=? AND phase='login'`, owner, stamp, authTime.Unix(), s.realm.digest("browser.csrf", nextCSRF), id)
		f.Phase = "consent"
		f.OwnerID = owner
		f.CredentialStamp = stamp
		f.AuthTime = authTime.UTC()
		return stepErr3
	})
	return f, rotated, nextCSRF, err
}

// AuthorizeBrowserFlow spans flow CAS, family/device binding, code and PKCE.
// The callback must not write an HTTP response before this function commits.
func (s *Store) AuthorizeBrowserFlow(ctx context.Context, cookie, id, csrf string, stamp []byte, fn func(context.Context, BrowserFlow) error) error {
	return s.WithRequest(ctx, func(ctx context.Context) error {
		f, err := s.flow(ctx, cookie, id, csrf, "consent")
		if err != nil || !equalHash(f.CredentialStamp, stamp) || f.AuthTime.Before(time.Now().Add(-5*time.Minute)) {
			return ErrBrowserFlow
		}
		if checkErr5 := fn(ctx, f); checkErr5 != nil {
			return checkErr5
		}
		q, operationErr3 := s.transaction(ctx)
		if operationErr3 != nil {
			return operationErr3
		}
		res, err := q.ExecContext(ctx, `UPDATE oauth_browser_flows SET phase='consumed' WHERE id=? AND phase='consent'`, id)
		if err != nil {
			return err
		}
		n, operationErr4 := res.RowsAffected()
		if operationErr4 != nil {
			return operationErr4
		}
		if n != 1 {
			return ErrBrowserFlow
		}
		return nil
	})
}

// AllowBrowserAttempt is a persistent fixed-window limiter. Subjects are keyed
// hashes of IP/account selectors; passwords, IPs and usernames are never stored.
func (s *Store) AllowBrowserAttempt(ctx context.Context, category, subject string, limit int, window time.Duration) (bool, error) {
	if limit < 1 || window < time.Second {
		return false, ErrInvalid
	}
	allowed := false
	err := s.WithRequest(ctx, func(ctx context.Context) error {
		q, operationErr5 := s.transaction(ctx)
		if operationErr5 != nil {
			return operationErr5
		}
		now := time.Now().Unix()
		start := now / int64(window/time.Second) * int64(window/time.Second)
		hash := s.realm.digest("browser.rate."+category, subject)
		if _, err := q.ExecContext(ctx, `DELETE FROM oauth_browser_limits WHERE window_start<?`, now-24*3600); err != nil {
			return err
		}
		var count int
		err := q.QueryRowContext(ctx, `INSERT INTO oauth_browser_limits(subject,window_start,count) VALUES(?,?,1) ON CONFLICT(subject,window_start) DO UPDATE SET count=MIN(count+1,?) RETURNING count`, hash, start, limit+1).Scan(&count)
		allowed = count <= limit
		return err
	})
	return allowed, err
}

// PrepareAuthorization is called only after live account validation and consent
// inside AuthorizeBrowserFlow. A device hint is not possession proof and never
// replaces a key. Only previously activated installations get provisional recovery.
func (s *Store) PrepareAuthorization(ctx context.Context, f BrowserFlow) (*Session, error) {
	q, err := s.transaction(ctx)
	if err != nil {
		return nil, err
	}
	a := f.Request
	if f.Phase != "consent" || !canonicalID(f.OwnerID) || !ValidSecret(a.KeyThumbprint) {
		return nil, ErrInvalid
	}
	client, err := s.GetClient(ctx, a.ClientID)
	if err != nil {
		return nil, err
	}
	scopes := splitScopes(a.Scope)
	if len(scopes) == 0 || !validScopes(scopes) || !subset(scopes, client.GetScopes()) {
		return nil, ErrInvalid
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO oauth_owners(id) VALUES(?) ON CONFLICT DO NOTHING`, f.OwnerID); err != nil {
		return nil, err
	}
	var device, thumb string
	var deviceEpoch, ownerEpoch uint64
	var revoked, activated bool
	var pendingExpiry int64
	if a.DeviceHint != "" {
		err = q.QueryRowContext(ctx, `SELECT id,thumbprint,epoch,revoked,activated,pending_expires_at FROM oauth_devices WHERE owner_id=? AND id=?`, f.OwnerID, a.DeviceHint).Scan(&device, &thumb, &deviceEpoch, &revoked, &activated, &pendingExpiry)
	} else {
		err = q.QueryRowContext(ctx, `SELECT id,thumbprint,epoch,revoked,activated,pending_expires_at FROM oauth_devices WHERE owner_id=? AND thumbprint=? AND revoked=0 AND (activated=1 OR pending_expires_at>?) ORDER BY id LIMIT 1`, f.OwnerID, a.KeyThumbprint, time.Now().Unix()).Scan(&device, &thumb, &deviceEpoch, &revoked, &activated, &pendingExpiry)
	}
	stage := Recovery
	if errors.Is(err, sql.ErrNoRows) && a.DeviceHint == "" {
		var count int
		if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_devices WHERE owner_id=? AND revoked=0 AND (activated=1 OR pending_expires_at>?)`, f.OwnerID, time.Now().Unix()).Scan(&count); err != nil || count >= 20 {
			return nil, ErrBrowserFlow
		}
		device = uuid.NewString()
		thumb = a.KeyThumbprint
		deviceEpoch = 1
		stage = Pending
		pendingExpiry = time.Now().Add(10 * time.Minute).Unix()
		_, err = q.ExecContext(ctx, `INSERT INTO oauth_devices(id,owner_id,thumbprint,pending_expires_at) VALUES(?,?,?,?)`, device, f.OwnerID, thumb, pendingExpiry)
	}
	if err != nil || revoked || thumb != a.KeyThumbprint {
		return nil, ErrBrowserFlow
	}
	if !activated {
		if pendingExpiry <= time.Now().Unix() {
			return nil, ErrBrowserFlow
		}
		stage = Pending
	}
	if checkErr6 := q.QueryRowContext(ctx, `SELECT epoch FROM oauth_owners WHERE id=?`, f.OwnerID).Scan(&ownerEpoch); checkErr6 != nil {
		return nil, checkErr6
	}
	var families int
	if checkErr7 := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_families WHERE owner_id=? AND device_id=? AND revoked=0 AND expires_at>? AND (stage='active' OR deadline>?)`, f.OwnerID, device, time.Now().Unix(), time.Now().Unix()).Scan(&families); checkErr7 != nil {
		return nil, checkErr7
	}
	if families >= 32 {
		return nil, ErrBrowserFlow
	}
	family := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	deadline := now.Add(stage.ttl())
	if stage == Pending && time.Unix(pendingExpiry, 0).Before(deadline) {
		deadline = time.Unix(pendingExpiry, 0)
	}
	if stage == Recovery && f.AuthTime.Add(5*time.Minute).Before(deadline) {
		deadline = f.AuthTime.Add(5 * time.Minute).Truncate(time.Second)
	}
	scopeJSON, _ := json.Marshal(scopes) //nolint:errcheck // A slice of strings always JSON-encodes.
	_, err = q.ExecContext(ctx, `INSERT INTO oauth_families(id,owner_id,device_id,client_id,stage,owner_epoch,device_epoch,signing_epoch,auth_time,created_at,expires_at,deadline,consent_scopes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, family, f.OwnerID, device, a.ClientID, stage, ownerEpoch, deviceEpoch, s.realm.epoch, f.AuthTime.Unix(), now.Unix(), now.Add(30*24*time.Hour).Unix(), deadline.Unix(), scopeJSON)
	if err != nil {
		return nil, err
	}
	return &Session{Binding: Binding{OwnerID: f.OwnerID, DeviceID: device, FamilyID: family, ClientID: a.ClientID, Kid: s.realm.kid, KeyThumbprint: thumb, Stage: stage, OwnerEpoch: ownerEpoch, DeviceEpoch: deviceEpoch, SigningEpoch: s.realm.epoch, AuthTime: f.AuthTime.Unix()}}, nil
}

func splitScopes(v string) []string { return strings.Fields(v) }
