package mobileoauth

// Isolated BE12 experiment. This store is not imported by the application and
// must not be promoted to production without BE13's schema/lifecycle work.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"
	fositestore "github.com/ory/fosite/storage"
	"github.com/uptrace/bun/driver/sqliteshim"
)

type store struct {
	db                *sql.DB
	failRefreshInsert bool
}
type txKey struct{}
type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

var (
	_ fosite.Storage                = (*store)(nil)
	_ oauth2.CoreStorage            = (*store)(nil)
	_ oauth2.TokenRevocationStorage = (*store)(nil)
	_ pkce.PKCERequestStorage       = (*store)(nil)
	_ fositestore.Transactional     = (*store)(nil)
)

func openStore(path string) (*store, error) {
	db, err := sql.Open(sqliteshim.ShimName, "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &store{db: db}
	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS clients(id TEXT PRIMARY KEY, data BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS families(id TEXT PRIMARY KEY, revoked INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS grants(kind TEXT NOT NULL, signature TEXT NOT NULL, family TEXT NOT NULL REFERENCES families(id), active INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY(kind,signature))`,
		`CREATE INDEX IF NOT EXISTS grant_family ON grants(family)`,
	} {
		if _, err = db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}
func (s *store) q(ctx context.Context) queryer {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.db
}
func (s *store) BeginTX(ctx context.Context) (context.Context, error) {
	if ctx.Value(txKey{}) != nil {
		return nil, errors.New("nested transaction")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, txKey{}, tx), nil
}
func (s *store) Commit(ctx context.Context) error { return ctx.Value(txKey{}).(*sql.Tx).Commit() }
func (s *store) Rollback(ctx context.Context) error {
	err := ctx.Value(txKey{}).(*sql.Tx).Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}
func (s *store) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	var b []byte
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT data FROM clients WHERE id=?`, id).Scan(&b); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fosite.ErrNotFound
		}
		return nil, err
	}
	c := new(fosite.DefaultClient)
	err := json.Unmarshal(b, c)
	return c, err
}
func (s *store) putClient(c *fosite.DefaultClient) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO clients(id,data) VALUES(?,?)`, c.ID, b)
	return err
}

// JWT client authentication is disabled: do not silently accept assertions.
func (s *store) ClientAssertionJWTValid(context.Context, string) error {
	return fosite.ErrRequestUnauthorized
}
func (s *store) SetClientAssertionJWT(context.Context, string, time.Time) error {
	return fosite.ErrRequestUnauthorized
}

func (s *store) create(ctx context.Context, kind, sig string, r fosite.Requester) error {
	// Store only signatures and an explicit allowlist of protocol fields.
	// No code/verifier/refresh token, incoming arbitrary extensions or client secret.
	form := url.Values{}
	for _, k := range []string{"redirect_uri", "code_challenge", "code_challenge_method"} {
		if v := r.GetRequestForm().Get(k); v != "" {
			form.Set(k, v)
		}
	}
	saved := &fosite.Request{ID: r.GetID(), RequestedAt: r.GetRequestedAt(), Client: r.GetClient(), RequestedScope: r.GetRequestedScopes(), GrantedScope: r.GetGrantedScopes(), RequestedAudience: r.GetRequestedAudience(), GrantedAudience: r.GetGrantedAudience(), Form: form, Session: r.GetSession()}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if kind == "code" {
		if _, err = s.q(ctx).ExecContext(ctx, `INSERT INTO families(id) VALUES(?) ON CONFLICT DO NOTHING`, r.GetID()); err != nil {
			return err
		}
	}
	var revoked int
	if err = s.q(ctx).QueryRowContext(ctx, `SELECT revoked FROM families WHERE id=?`, r.GetID()).Scan(&revoked); err != nil {
		return err
	}
	if revoked != 0 {
		return fosite.ErrInactiveToken
	}
	if kind == "refresh" && s.failRefreshInsert {
		return errors.New("injected refresh insert failure")
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO grants(kind,signature,family,active,data) VALUES(?,?,?,1,?)`, kind, sig, r.GetID(), data)
	return err
}
func (s *store) get(ctx context.Context, kind, sig string) (fosite.Requester, error) {
	var data []byte
	var active, revoked int
	err := s.q(ctx).QueryRowContext(ctx, `SELECT g.data,g.active,f.revoked FROM grants g JOIN families f ON f.id=g.family WHERE g.kind=? AND g.signature=?`, kind, sig).Scan(&data, &active, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r := &fosite.Request{Client: new(fosite.DefaultClient), Session: new(stageSession)}
	if err = json.Unmarshal(data, r); err != nil {
		return nil, err
	}
	// Return the original requester as required for Fosite's replay revocation.
	if kind == "code" && active == 0 {
		return r, fosite.ErrInvalidatedAuthorizeCode
	}
	if active == 0 || revoked != 0 {
		return r, fosite.ErrInactiveToken
	}
	return r, nil
}
func (s *store) remove(ctx context.Context, kind, sig string) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM grants WHERE kind=? AND signature=?`, kind, sig)
	return err
}
func (s *store) CreateAuthorizeCodeSession(c context.Context, k string, r fosite.Requester) error {
	return s.create(c, "code", k, r)
}
func (s *store) GetAuthorizeCodeSession(c context.Context, k string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(c, "code", k)
}
func (s *store) InvalidateAuthorizeCodeSession(c context.Context, k string) error {
	res, err := s.q(c).ExecContext(c, `UPDATE grants SET active=0 WHERE kind='code' AND signature=? AND active=1`, k)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return fosite.ErrSerializationFailure
	}
	return err
}
func (s *store) CreateAccessTokenSession(c context.Context, k string, r fosite.Requester) error {
	return s.create(c, "access", k, r)
}
func (s *store) GetAccessTokenSession(c context.Context, k string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(c, "access", k)
}
func (s *store) DeleteAccessTokenSession(c context.Context, k string) error {
	return s.remove(c, "access", k)
}
func (s *store) CreateRefreshTokenSession(c context.Context, k, _ string, r fosite.Requester) error {
	return s.create(c, "refresh", k, r)
}
func (s *store) GetRefreshTokenSession(c context.Context, k string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(c, "refresh", k)
}
func (s *store) DeleteRefreshTokenSession(c context.Context, k string) error {
	// Retain the tombstone; deleting a spent token would lose reuse detection.
	_, err := s.q(c).ExecContext(c, `UPDATE grants SET active=0 WHERE kind='refresh' AND signature=?`, k)
	return err
}
func (s *store) RotateRefreshToken(c context.Context, family, sig string) error {
	if c.Value(txKey{}) == nil {
		return errors.New("rotation must run inside a transaction")
	}
	res, err := s.q(c).ExecContext(c, `UPDATE grants SET active=0 WHERE kind='refresh' AND signature=? AND family=? AND active=1 AND EXISTS(SELECT 1 FROM families WHERE id=? AND revoked=0)`, sig, family, family)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fosite.ErrSerializationFailure
	}
	_, err = s.q(c).ExecContext(c, `UPDATE grants SET active=0 WHERE kind='access' AND family=?`, family)
	return err
}
func (s *store) revoke(c context.Context, family, kind string) error {
	if _, err := s.q(c).ExecContext(c, `UPDATE families SET revoked=1 WHERE id=?`, family); err != nil {
		return err
	}
	_, err := s.q(c).ExecContext(c, `UPDATE grants SET active=0 WHERE family=? AND kind=?`, family, kind)
	return err
}
func (s *store) RevokeRefreshToken(c context.Context, f string) error {
	return s.revoke(c, f, "refresh")
}
func (s *store) RevokeAccessToken(c context.Context, f string) error { return s.revoke(c, f, "access") }
func (s *store) CreatePKCERequestSession(c context.Context, k string, r fosite.Requester) error {
	return s.create(c, "pkce", k, r)
}
func (s *store) GetPKCERequestSession(c context.Context, k string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(c, "pkce", k)
}
func (s *store) DeletePKCERequestSession(c context.Context, k string) error {
	return s.remove(c, "pkce", k)
}

// Minimal custom persisted session: demonstrates framework extension points,
// not activation/recovery proof validation or the complete mobile JWT profile.
type challenge struct {
	Nonce     string
	DeviceID  string
	ExpiresAt time.Time
}
type stageSession struct {
	oauth2.JWTSession
	Stage     string
	Challenge *challenge
}

func (s *stageSession) Clone() fosite.Session {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	out := new(stageSession)
	if err = json.Unmarshal(b, out); err != nil {
		panic(fmt.Sprint(err))
	}
	return out
}
