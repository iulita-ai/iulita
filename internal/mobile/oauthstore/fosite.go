package oauthstore

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"
	fstorage "github.com/ory/fosite/storage"
)

var (
	_ fosite.Storage                = (*Store)(nil)
	_ oauth2.CoreStorage            = (*Store)(nil)
	_ oauth2.TokenRevocationStorage = (*Store)(nil)
	_ pkce.PKCERequestStorage       = (*Store)(nil)
	_ fstorage.Transactional        = (*Store)(nil)
)

func equalHash(a, b []byte) bool { return hmac.Equal(a, b) }
func validScopes(v []string) bool {
	seen := map[string]bool{}
	for _, scope := range v {
		if scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \t\r\n") || seen[scope] {
			return false
		}
		seen[scope] = true
	}
	return true
}
func subset(v, allowed []string) bool {
	for _, s := range v {
		found := false
		for _, a := range allowed {
			if s == a {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// RegisterClient registers the permitted public native OAuth client.
func (s *Store) RegisterClient(ctx context.Context, c fosite.DefaultClient) error {
	if c.ID == "" || len(c.ID) > 128 || !c.Public || len(c.Secret) != 0 || len(c.RotatedSecrets) != 0 || (len(c.GrantTypes) != 2 || !fosite.Arguments(c.GrantTypes).Has("authorization_code") || !fosite.Arguments(c.GrantTypes).Has("refresh_token")) || !fosite.Arguments(c.ResponseTypes).ExactOne("code") || len(c.RedirectURIs) == 0 || !validScopes(c.Scopes) {
		return ErrInvalid
	}
	for _, redirect := range c.RedirectURIs {
		u, err := url.Parse(redirect)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Fragment != "" || u.User != nil || u.RawQuery != "" {
			return ErrInvalid
		}
	}
	data, err := json.Marshal(c) //nolint:gosec // The public client is required to have empty secrets before encoding.
	if err != nil {
		return ErrInvalid
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_clients(id,data) VALUES(?,?)`, c.ID, data)
	return err
}

// GetClient implements the Fosite GetClient storage contract inside the request transaction.
func (s *Store) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	var data []byte
	var err error
	if root, e := s.root(ctx); e == nil {
		// Authentication header/body ambiguity must not bypass replay binding.
		if root.verifiedClient != "" && root.verifiedClient != id {
			return nil, fosite.ErrInvalidClient
		}
		err = root.tx.QueryRowContext(ctx, `SELECT data FROM oauth_clients WHERE id=?`, id).Scan(&data)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT data FROM oauth_clients WHERE id=?`, id).Scan(&data)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c := new(fosite.DefaultClient)
	err = json.Unmarshal(data, c)
	return c, err
}

// ClientAssertionJWTValid implements the Fosite ClientAssertionJWTValid storage contract inside the request transaction.
func (s *Store) ClientAssertionJWTValid(context.Context, string) error {
	return fosite.ErrRequestUnauthorized
}

// SetClientAssertionJWT implements the Fosite SetClientAssertionJWT storage contract inside the request transaction.
func (s *Store) SetClientAssertionJWT(context.Context, string, time.Time) error {
	return fosite.ErrRequestUnauthorized
}

// FrameworkConfig uses a copy of the independent mobile HMAC secret. It grants
// no readiness and deliberately does not compose handlers or serve endpoints.
func (s *Store) FrameworkConfig() *fosite.Config {
	return &fosite.Config{GlobalSecret: append([]byte(nil), s.realm.hmacKey...), AccessTokenIssuer: s.realm.issuer, EnforcePKCE: true, EnablePKCEPlainChallengeMethod: false, ScopeStrategy: fosite.ExactScopeStrategy, RefreshTokenScopes: []string{}, AccessTokenLifespan: 15 * time.Minute, RefreshTokenLifespan: 24 * time.Hour, AuthorizeCodeLifespan: 5 * time.Minute}
}

// NewSession creates a realm-bound Fosite session.
func (s *Store) NewSession(ctx context.Context, owner, id string) (*Session, error) {
	var out *Session
	err := s.WithRequest(ctx, func(ctx context.Context) error {
		f, err := s.familyIn(ctx, id)
		if err != nil || f.OwnerID != owner {
			return ErrNotFound
		}
		if checkErr1 := s.live(ctx, f); checkErr1 != nil {
			return checkErr1
		}
		var thumb string
		q, operationErr1 := s.transaction(ctx)
		if operationErr1 != nil {
			return operationErr1
		}
		if checkErr2 := q.QueryRowContext(ctx, `SELECT thumbprint FROM oauth_devices WHERE owner_id=? AND id=?`, owner, f.DeviceID).Scan(&thumb); checkErr2 != nil {
			return checkErr2
		}
		out = &Session{Binding: Binding{OwnerID: f.OwnerID, DeviceID: f.DeviceID, FamilyID: f.ID, ClientID: f.ClientID, Kid: s.realm.kid, KeyThumbprint: thumb, Stage: f.Stage, OwnerEpoch: f.OwnerEpoch, DeviceEpoch: f.DeviceEpoch, SigningEpoch: f.SigningEpoch, Generation: f.Generation, AuthTime: f.AuthTime.Unix()}}
		return nil
	})
	return out, err
}
func (s *Store) create(ctx context.Context, kind, sig string, r fosite.Requester) error {
	q, err := s.transaction(ctx)
	if err != nil {
		return err
	}
	if sig == "" || len(sig) > 4096 {
		return ErrInvalid
	}
	var sessionCount int
	if checkErr3 := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_sessions WHERE family_id=?`, r.GetID()).Scan(&sessionCount); checkErr3 != nil {
		return checkErr3
	}
	if sessionCount >= 8192 {
		return fosite.ErrInvalidGrant
	}
	session, ok := r.GetSession().(*Session)
	if !ok || session == nil {
		return ErrInvalid
	}
	b := session.Binding
	f, err := s.familyIn(ctx, r.GetID())
	if err != nil {
		return err
	}
	if err = s.live(ctx, f); err != nil {
		return fosite.ErrInactiveToken
	}
	if b.FamilyID != f.ID || b.OwnerID != f.OwnerID || b.DeviceID != f.DeviceID || b.ClientID != f.ClientID || b.Stage != f.Stage || b.Kid != s.realm.kid || b.OwnerEpoch != f.OwnerEpoch || b.DeviceEpoch != f.DeviceEpoch || b.SigningEpoch != f.SigningEpoch || b.AuthTime != f.AuthTime.Unix() || r.GetClient().GetID() != f.ClientID {
		return ErrInvalid
	}
	var thumb string
	if err = q.QueryRowContext(ctx, `SELECT thumbprint FROM oauth_devices WHERE owner_id=? AND id=?`, f.OwnerID, f.DeviceID).Scan(&thumb); err != nil || thumb != b.KeyThumbprint {
		return ErrInvalid
	}
	root, operationErr2 := s.root(ctx)
	if operationErr2 != nil {
		return operationErr2
	}
	if b.Generation != f.Generation && (root.rotated[f.ID] != f.Generation || b.Generation+1 != f.Generation) {
		return fosite.ErrSerializationFailure
	}
	client, err := s.GetClient(ctx, f.ClientID)
	if err != nil {
		return err
	}
	if !validScopes(r.GetGrantedScopes()) || !subset(r.GetGrantedScopes(), f.ConsentScopes) || !subset(f.ConsentScopes, client.GetScopes()) || (b.Stage != Active && len(r.GetGrantedScopes()) != 0) {
		return ErrInvalid
	}
	if !subset(r.GetGrantedAudience(), []string{s.realm.audience}) || !subset(r.GetRequestedAudience(), []string{s.realm.audience}) {
		return ErrInvalid
	}
	tokenType := fosite.AccessToken
	switch kind {
	case "refresh":
		tokenType = fosite.RefreshToken
	case "code", "pkce":
		tokenType = fosite.AuthorizeCode
	}
	expires := session.GetExpiresAt(tokenType)
	if expires.IsZero() && kind == "pkce" {
		expires = time.Now().Add(5 * time.Minute)
	}
	limit := f.ExpiresAt
	if !f.Deadline.IsZero() && f.Deadline.Before(limit) {
		limit = f.Deadline
	}
	if (kind == "code" || kind == "pkce") && expires.After(limit) {
		expires = limit
	}
	if !expires.After(time.Now()) || expires.After(limit) {
		return ErrInvalid
	}
	form := map[string][]string{}
	for _, key := range []string{"redirect_uri", "code_challenge", "code_challenge_method"} {
		if v := r.GetRequestForm().Get(key); v != "" {
			form[key] = []string{v}
		}
	}
	copySession := session.cloneSession()
	copySession.Binding.Generation = f.Generation
	copySession.SetExpiresAt(tokenType, expires)
	payload := savedRequest{Version: 1, ID: f.ID, ClientID: f.ClientID, RequestedAt: r.GetRequestedAt(), RequestedScopes: r.GetRequestedScopes(), GrantedScopes: r.GetGrantedScopes(), RequestedAudience: r.GetRequestedAudience(), GrantedAudience: r.GetGrantedAudience(), Form: form, Session: *copySession}
	data, err := json.Marshal(payload)
	if err != nil {
		return ErrInvalid
	}
	_, err = q.ExecContext(ctx, `INSERT INTO oauth_sessions(kind,key_hash,family_id,stage,generation,expires_at,data) VALUES(?,?,?,?,?,?,?)`, kind, s.realm.digest("session."+kind, sig), f.ID, b.Stage, f.Generation, expires.Unix(), data)
	return err
}

// load returns inactive rows too, because verified reuse needs the original
// family binding. Cryptographic/client checks happen in TokenRequest first.
func (s *Store) load(ctx context.Context, kind, sig string) (*fosite.Request, bool, time.Time, error) {
	q, err := s.transaction(ctx)
	if err != nil {
		return nil, false, time.Time{}, err
	}
	var data []byte
	var active bool
	var expires int64
	err = q.QueryRowContext(ctx, `SELECT data,active,expires_at FROM oauth_sessions WHERE kind=? AND key_hash=?`, kind, s.realm.digest("session."+kind, sig)).Scan(&data, &active, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, time.Time{}, fosite.ErrNotFound
	}
	if err != nil {
		return nil, false, time.Time{}, err
	}
	r, err := decode(data)
	if err != nil {
		return nil, false, time.Time{}, err
	}
	client, err := s.GetClient(ctx, r.Client.GetID())
	if err != nil {
		return nil, false, time.Time{}, err
	}
	r.Client = client
	return r, active, time.Unix(expires, 0), nil
}
func (s *Store) get(ctx context.Context, kind, sig string) (fosite.Requester, error) {
	r, active, expires, err := s.load(ctx, kind, sig)
	if err != nil {
		return nil, err
	}
	f, err := s.familyIn(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if !active {
		if kind == "code" {
			return r, fosite.ErrInvalidatedAuthorizeCode
		}
		return r, fosite.ErrInactiveToken
	}
	session, validSession := r.Session.(*Session)
	if !validSession || session == nil {
		return nil, ErrInvalid
	}
	if s.live(ctx, f) != nil || !expires.After(time.Now()) || session.Binding.Generation != f.Generation {
		return r, fosite.ErrInactiveToken
	}
	b := session.Binding
	if b.Stage != f.Stage || b.OwnerID != f.OwnerID || b.DeviceID != f.DeviceID || b.OwnerEpoch != f.OwnerEpoch || b.SigningEpoch != f.SigningEpoch {
		return r, fosite.ErrInactiveToken
	}
	if b.DeviceEpoch != f.DeviceEpoch {
		// Recovery revokes old access immediately. Only its one unused refresh
		// may complete the already-committed proof within the original deadline.
		var completed int
		q, operationErr3 := s.transaction(ctx)
		if operationErr3 != nil {
			return nil, operationErr3
		}
		if kind != "refresh" || b.Stage != Recovery || b.DeviceEpoch+1 != f.DeviceEpoch {
			return r, fosite.ErrInactiveToken
		}
		if q.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_completions WHERE family_id=? AND kind='recover_device'`, f.ID).Scan(&completed) != nil || completed != 1 {
			return r, fosite.ErrInactiveToken
		}
	}
	return r, nil
}

// CreateAuthorizeCodeSession implements the Fosite CreateAuthorizeCodeSession storage contract inside the request transaction.
func (s *Store) CreateAuthorizeCodeSession(c context.Context, k string, r fosite.Requester) error {
	return s.create(c, "code", k, r)
}

// GetAuthorizeCodeSession implements the Fosite GetAuthorizeCodeSession storage contract inside the request transaction.
func (s *Store) GetAuthorizeCodeSession(c context.Context, k string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(c, "code", k)
}

// InvalidateAuthorizeCodeSession implements the Fosite InvalidateAuthorizeCodeSession storage contract inside the request transaction.
func (s *Store) InvalidateAuthorizeCodeSession(c context.Context, k string) error {
	q, err := s.transaction(c)
	if err != nil {
		return err
	}
	res, err := q.ExecContext(c, `UPDATE oauth_sessions SET active=0 WHERE kind='code' AND key_hash=? AND active=1 AND expires_at>?`, s.realm.digest("session.code", k), time.Now().Unix())
	if err != nil {
		return err
	}
	n, operationErr4 := res.RowsAffected()
	if operationErr4 != nil {
		return operationErr4
	}
	if n != 1 {
		return fosite.ErrSerializationFailure
	}
	return nil
}

// CreateAccessTokenSession implements the Fosite CreateAccessTokenSession storage contract inside the request transaction.
func (s *Store) CreateAccessTokenSession(c context.Context, k string, r fosite.Requester) error {
	return s.create(c, "access", k, r)
}

// GetAccessTokenSession implements the Fosite GetAccessTokenSession storage contract inside the request transaction.
func (s *Store) GetAccessTokenSession(c context.Context, k string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(c, "access", k)
}

// DeleteAccessTokenSession implements the Fosite DeleteAccessTokenSession storage contract inside the request transaction.
func (s *Store) DeleteAccessTokenSession(c context.Context, k string) error {
	return s.inactivate(c, "access", k)
}

// CreateRefreshTokenSession implements the Fosite CreateRefreshTokenSession storage contract inside the request transaction.
func (s *Store) CreateRefreshTokenSession(c context.Context, k, _ string, r fosite.Requester) error {
	return s.create(c, "refresh", k, r)
}

// GetRefreshTokenSession implements the Fosite GetRefreshTokenSession storage contract inside the request transaction.
func (s *Store) GetRefreshTokenSession(c context.Context, k string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(c, "refresh", k)
}

// DeleteRefreshTokenSession implements the Fosite DeleteRefreshTokenSession storage contract inside the request transaction.
func (s *Store) DeleteRefreshTokenSession(c context.Context, k string) error {
	return s.inactivate(c, "refresh", k)
}
func (s *Store) inactivate(ctx context.Context, kind, sig string) error {
	q, err := s.transaction(ctx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE oauth_sessions SET active=0 WHERE kind=? AND key_hash=?`, kind, s.realm.digest("session."+kind, sig))
	return err
}

// RotateRefreshToken implements the Fosite RotateRefreshToken storage contract inside the request transaction.
func (s *Store) RotateRefreshToken(ctx context.Context, id, sig string) error {
	q, err := s.transaction(ctx)
	if err != nil {
		return err
	}
	r, err := s.root(ctx)
	if err != nil || r.verifiedFamily != id {
		return ErrTransaction
	}
	f, err := s.familyIn(ctx, id)
	if err != nil {
		return err
	}
	if err = s.live(ctx, f); err != nil {
		return fosite.ErrInactiveToken
	}
	res, err := q.ExecContext(ctx, `UPDATE oauth_sessions SET active=0 WHERE kind='refresh' AND key_hash=? AND family_id=? AND generation=? AND active=1 AND expires_at>?`, s.realm.digest("session.refresh", sig), id, f.Generation, time.Now().Unix())
	if err != nil {
		return err
	}
	n, operationErr5 := res.RowsAffected()
	if operationErr5 != nil {
		return operationErr5
	}
	if n != 1 {
		return fosite.ErrSerializationFailure
	}
	if _, err = q.ExecContext(ctx, `UPDATE oauth_families SET generation=generation+1 WHERE id=? AND generation=? AND revoked=0`, id, f.Generation); err != nil {
		return err
	}
	if _, err = q.ExecContext(ctx, `UPDATE oauth_sessions SET active=0 WHERE kind='access' AND family_id=?`, id); err != nil {
		return err
	}
	r.rotated[id] = f.Generation + 1
	return nil
}
func (s *Store) revokeVerified(ctx context.Context, id string) error {
	r, err := s.root(ctx)
	if err != nil {
		return err
	}
	if r.verifiedFamily != id {
		return ErrTransaction
	}
	r.revocations[id] = true
	return s.revoke(ctx, id)
}

// RevokeAccessToken implements the Fosite RevokeAccessToken storage contract inside the request transaction.
func (s *Store) RevokeAccessToken(ctx context.Context, id string) error {
	return s.revokeVerified(ctx, id)
}

// RevokeRefreshToken implements the Fosite RevokeRefreshToken storage contract inside the request transaction.
func (s *Store) RevokeRefreshToken(ctx context.Context, id string) error {
	return s.revokeVerified(ctx, id)
}

// CreatePKCERequestSession implements the Fosite CreatePKCERequestSession storage contract inside the request transaction.
func (s *Store) CreatePKCERequestSession(c context.Context, k string, r fosite.Requester) error {
	return s.create(c, "pkce", k, r)
}

// GetPKCERequestSession implements the Fosite GetPKCERequestSession storage contract inside the request transaction.
func (s *Store) GetPKCERequestSession(c context.Context, k string, _ fosite.Session) (fosite.Requester, error) {
	return s.get(c, "pkce", k)
}

// DeletePKCERequestSession implements the Fosite DeletePKCERequestSession storage contract inside the request transaction.
func (s *Store) DeletePKCERequestSession(ctx context.Context, k string) error {
	q, err := s.transaction(ctx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `DELETE FROM oauth_sessions WHERE kind='pkce' AND key_hash=?`, s.realm.digest("session.pkce", k))
	return err
}
