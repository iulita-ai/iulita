package oauthstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/ory/fosite"
)

// DeviceGrant is the RFC8628 token grant identifier.
const DeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

// ErrClientAuthentication rejects an absent, disabled or incorrectly authenticated registration.
var ErrClientAuthentication = errors.New("invalid OAuth client authentication")

// ErrScope rejects requested authority outside the registration ceiling.
var ErrScope = errors.New("invalid OAuth scope")

// ClientRegistration is explicit operator policy, never remote registration.
type ClientRegistration struct {
	ClientID     string   `json:"client_id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Scopes       []string `json:"scopes"`
	RedirectURIs []string `json:"redirect_uris,omitempty"`
	OwnerID      string   `json:"owner_id,omitempty"`
	PrincipalID  string   `json:"principal_id,omitempty"`
	Epoch        uint64   `json:"epoch,omitempty"`
	Disabled     bool     `json:"disabled,omitempty"`
}

var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var nativeCeiling = []string{"assistant:services:read", "assistant:services:write", "chats:read", "chats:write", "devices:read", "devices:manage", "devices:receive", "devices:sync", "phone:data", "phone:ui"}
var cliCeiling = []string{"chats:read", "chats:write", "devices:read", "devices:manage", "phone:data", "phone:ui"}
var serviceCeiling = []string{"chats:read", "chats:write"}

var clientsSchema = []string{
	`CREATE TABLE oauth_client_policy(client_id TEXT PRIMARY KEY REFERENCES oauth_clients(id),name TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('native','cli','service')),owner_id TEXT NOT NULL DEFAULT '',principal_id TEXT NOT NULL DEFAULT '',epoch INTEGER NOT NULL DEFAULT 1,disabled INTEGER NOT NULL DEFAULT 0,secret_hash BLOB)`,
	`CREATE TABLE oauth_device_requests(id TEXT PRIMARY KEY,client_id TEXT NOT NULL REFERENCES oauth_clients(id),code_hash BLOB NOT NULL UNIQUE,user_hash BLOB NOT NULL UNIQUE,key_thumbprint TEXT NOT NULL,scope TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',owner_id TEXT NOT NULL DEFAULT '',family_id TEXT NOT NULL DEFAULT '',credential_stamp BLOB,auth_time INTEGER NOT NULL DEFAULT 0,expires_at INTEGER NOT NULL,interval INTEGER NOT NULL DEFAULT 5,last_poll INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL)`,
	`CREATE INDEX oauth_device_requests_expiry ON oauth_device_requests(expires_at)`,
	`CREATE TABLE oauth_service_access(grant_id TEXT PRIMARY KEY,client_id TEXT NOT NULL REFERENCES oauth_clients(id),principal_id TEXT NOT NULL,owner_id TEXT NOT NULL,client_epoch INTEGER NOT NULL,owner_epoch INTEGER NOT NULL,signing_epoch INTEGER NOT NULL,scope TEXT NOT NULL,expires_at INTEGER NOT NULL,revoked INTEGER NOT NULL DEFAULT 0)`,
	`CREATE INDEX oauth_service_access_expiry ON oauth_service_access(expires_at)`,
	`INSERT INTO oauth_client_policy(client_id,name,kind) SELECT id,id,'native' FROM oauth_clients`,
	`DROP TABLE oauth_schema`,
	`CREATE TABLE oauth_schema(version INTEGER PRIMARY KEY CHECK(version=4))`,
	`INSERT INTO oauth_schema VALUES(4)`,
}

func (s *Store) migrateClients(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Preserve migration outcome.
	var version int
	if checkErr1 := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM oauth_schema`).Scan(&version); checkErr1 != nil {
		return checkErr1
	}
	if version == 4 {
		return tx.Commit()
	}
	if version != 3 {
		return ErrInvalid
	}
	for _, query := range clientsSchema {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func normalizeRegistration(r ClientRegistration) (ClientRegistration, error) {
	if !clientIDPattern.MatchString(r.ClientID) || len(r.Name) < 1 || len(r.Name) > 128 || strings.ContainsAny(r.Name, "\x00\r\n") || r.Epoch != 0 || r.Disabled || r.PrincipalID != "" {
		return r, ErrInvalid
	}
	ceiling := nativeCeiling
	switch r.Kind {
	case "native":
		if (r.OwnerID != "" && !canonicalID(r.OwnerID)) || len(r.RedirectURIs) < 1 || len(r.RedirectURIs) > 8 {
			return r, ErrInvalid
		}
		for _, value := range r.RedirectURIs {
			u, err := url.Parse(value)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
				return r, ErrInvalid
			}
		}
	case "cli":
		ceiling = cliCeiling
		if r.OwnerID != "" || len(r.RedirectURIs) != 0 {
			return r, ErrInvalid
		}
		if len(r.Scopes) == 0 {
			r.Scopes = []string{"chats:read", "chats:write"}
		}
	case "service":
		ceiling = serviceCeiling
		if !canonicalID(r.OwnerID) || len(r.RedirectURIs) != 0 {
			return r, ErrInvalid
		}
		if len(r.Scopes) == 0 {
			r.Scopes = []string{"chats:read"}
		}
	default:
		return r, ErrInvalid
	}
	if len(r.Scopes) == 0 || !validScopes(r.Scopes) || !subset(r.Scopes, ceiling) {
		return r, ErrInvalid
	}
	return r, nil
}

func clientSecretHash(id, secret string) []byte {
	sum := sha256.Sum256([]byte("iulita-service-secret-v1\x00" + id + "\x00" + secret))
	return sum[:]
}

// RegisterOAuthClient creates one immutable, independently scoped registration.
// Service secrets must be generated256bit values; only their hash is persisted.
func (s *Store) RegisterOAuthClient(ctx context.Context, r ClientRegistration, secret string) error {
	r, err := normalizeRegistration(r)
	if err != nil {
		return err
	}
	if r.Kind == "service" && !ValidSecret(secret) || r.Kind != "service" && secret != "" {
		return ErrInvalid
	}
	client := fosite.DefaultClient{ID: r.ClientID, Public: r.Kind != "service", Scopes: r.Scopes, Audience: []string{s.Audience()}}
	switch r.Kind {
	case "native":
		client.RedirectURIs = r.RedirectURIs
		client.GrantTypes = []string{"authorization_code", "refresh_token"}
		client.ResponseTypes = []string{"code"}
	case "cli":
		client.GrantTypes = []string{DeviceGrant, "refresh_token"}
	case "service":
		client.GrantTypes = []string{"client_credentials"}
	}
	data, err := json.Marshal(client) //nolint:gosec // Secret fields of the Fosite registration are always empty; credential hashes live separately.
	if err != nil {
		return err
	}
	return s.WithRequest(ctx, func(ctx context.Context) error {
		tx, err := s.transaction(ctx)
		if err != nil {
			return err
		}
		if r.Kind == "service" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO oauth_owners(id) VALUES(?) ON CONFLICT DO NOTHING`, r.OwnerID); err != nil {
				return err
			}
			r.PrincipalID = uuid.NewString()
		}
		var hash []byte
		if secret != "" {
			hash = clientSecretHash(r.ClientID, secret)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO oauth_clients(id,data) VALUES(?,?)`, r.ClientID, data); err != nil {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO oauth_client_policy(client_id,name,kind,owner_id,principal_id,secret_hash) VALUES(?,?,?,?,?,?)`, r.ClientID, r.Name, r.Kind, r.OwnerID, r.PrincipalID, hash)
		return err
	})
}

func (s *Store) clientPolicy(ctx context.Context, id string, allowDisabled bool) (ClientRegistration, []byte, error) {
	var r ClientRegistration
	var hash, data []byte
	query := `SELECT p.client_id,p.name,p.kind,p.owner_id,p.principal_id,p.epoch,p.disabled,p.secret_hash,c.data FROM oauth_client_policy p JOIN oauth_clients c ON c.id=p.client_id WHERE p.client_id=?`
	var row *sql.Row
	if root, e := s.root(ctx); e == nil {
		row = root.tx.QueryRowContext(ctx, query, id)
	} else {
		row = s.db.QueryRowContext(ctx, query, id)
	}
	err := row.Scan(&r.ClientID, &r.Name, &r.Kind, &r.OwnerID, &r.PrincipalID, &r.Epoch, &r.Disabled, &hash, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil, ErrNotFound
	}
	if err != nil {
		return r, nil, err
	}
	if !allowDisabled && r.Disabled {
		return r, nil, ErrNotFound
	}
	var client fosite.DefaultClient
	if json.Unmarshal(data, &client) != nil {
		return r, nil, ErrInvalid
	}
	r.Scopes = client.Scopes
	r.RedirectURIs = client.RedirectURIs
	return r, hash, nil
}

// ClientPolicy returns public registration metadata without credential hashes.
func (s *Store) ClientPolicy(ctx context.Context, id string) (ClientRegistration, error) {
	r, _, err := s.clientPolicy(ctx, id, false)
	return r, err
}

// ListOAuthClients returns operator metadata; it never returns service secrets.
func (s *Store) ListOAuthClients(ctx context.Context) ([]ClientRegistration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT client_id FROM oauth_client_policy ORDER BY client_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // Explicit close below precedes further reads.
	var ids []string
	for rows.Next() {
		var id string
		if checkErr2 := rows.Scan(&id); checkErr2 != nil {
			return nil, checkErr2
		}
		ids = append(ids, id)
	}
	if checkErr3 := rows.Err(); checkErr3 != nil {
		return nil, checkErr3
	}
	if checkErr4 := rows.Close(); checkErr4 != nil {
		return nil, checkErr4
	}
	out := make([]ClientRegistration, 0, len(ids))
	for _, id := range ids {
		r, _, loadErr := s.clientPolicy(ctx, id, true)
		if loadErr != nil {
			return nil, loadErr
		}
		out = append(out, r)
	}
	return out, nil
}

// ChangeOAuthClient disables a client or rotates its service secret. Both revoke
// current grants atomically; changing an immutable scope ceiling needs a new ID.
func (s *Store) ChangeOAuthClient(ctx context.Context, id, secret string, disable bool) error {
	if !disable && !ValidSecret(secret) {
		return ErrInvalid
	}
	return s.WithRequest(ctx, func(ctx context.Context) error {
		r, _, err := s.clientPolicy(ctx, id, true)
		if err != nil {
			return err
		}
		if !disable && (r.Kind != "service" || r.Disabled) {
			return ErrInvalid
		}
		tx, err := s.transaction(ctx)
		if err != nil {
			return err
		}
		if disable {
			_, err = tx.ExecContext(ctx, `UPDATE oauth_client_policy SET disabled=1,epoch=epoch+1 WHERE client_id=?`, id)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE oauth_client_policy SET secret_hash=?,epoch=epoch+1 WHERE client_id=?`, clientSecretHash(id, secret), id)
		}
		if err != nil {
			return err
		}
		for _, q := range []string{`UPDATE oauth_families SET revoked=1 WHERE client_id=?`, `UPDATE oauth_service_access SET revoked=1 WHERE client_id=?`, `UPDATE oauth_device_requests SET state='denied' WHERE client_id=? AND state IN ('pending','approved')`} {
			if _, err = tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) serviceClient(ctx context.Context, id, secret string) (ClientRegistration, error) {
	r, hash, err := s.clientPolicy(ctx, id, false)
	if errors.Is(err, ErrNotFound) {
		return ClientRegistration{}, ErrClientAuthentication
	}
	if err != nil {
		return ClientRegistration{}, err
	}
	if r.Kind != "service" || !ValidSecret(secret) || !equalHash(hash, clientSecretHash(id, secret)) {
		return ClientRegistration{}, ErrClientAuthentication
	}
	return r, nil
}

// ServiceClientOwner authenticates only enough to acquire the account guard;
// issuance rechecks the registration and secret in its write transaction.
func (s *Store) ServiceClientOwner(ctx context.Context, id, secret string) (string, error) {
	r, err := s.serviceClient(ctx, id, secret)
	return r.OwnerID, err
}
