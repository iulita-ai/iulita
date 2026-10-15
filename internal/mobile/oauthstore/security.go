package oauthstore

import (
	"context"
	"strings"
	"time"
)

// SyncAccount binds the live credential version from the account authority.
// Changes persist an epoch bump and revocation of ALL owner families. It is
// called before credential use, never after caching an authorization decision.
func (s *Store) SyncAccount(ctx context.Context, owner, version string, available bool) error {
	if !canonicalID(owner) || version == "" {
		return ErrInvalid
	}
	return s.WithRequest(ctx, func(ctx context.Context) error {
		q, operationErr1 := s.transaction(ctx)
		if operationErr1 != nil {
			return operationErr1
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO oauth_owners(id) VALUES(?) ON CONFLICT DO NOTHING`, owner); err != nil {
			return err
		}
		var stored []byte
		if err := q.QueryRowContext(ctx, `SELECT account_stamp FROM oauth_owners WHERE id=?`, owner).Scan(&stored); err != nil {
			return err
		}
		availability := "disabled"
		if available {
			availability = "available"
		}
		stamp := s.CredentialStamp("account.v1\x00" + owner + "\x00" + version + "\x00" + availability)
		if equalHash(stored, stamp) {
			return nil
		}
		if _, err := q.ExecContext(ctx, `UPDATE oauth_owners SET epoch=epoch+1,account_stamp=? WHERE id=?`, stamp, owner); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, `UPDATE oauth_families SET revoked=1 WHERE owner_id=?`, owner)
		return err
	})
}

// TokenOwner looks up the owner needed to acquire the account guard.
func (s *Store) TokenOwner(ctx context.Context, grant, raw, client, redirect string) (string, error) {
	var owner string
	err := s.TokenRequest(ctx, grant, raw, client, redirect, s.Strategy(), func(ctx context.Context) error {
		root, operationErr2 := s.root(ctx)
		if operationErr2 != nil {
			return operationErr2
		}
		f, err := s.familyIn(ctx, root.verifiedFamily)
		owner = f.OwnerID
		return err
	})
	return owner, err
}

// RevokeCredential reveals neither existence nor owner. A public client must
// still possess a cryptographically valid, client-bound credential.
func (s *Store) RevokeCredential(ctx context.Context, raw, client, hint string) error {
	if raw == "" || client == "" || len(raw) > 16384 {
		return ErrInvalid
	}
	if hint != "access_token" {
		err := s.TokenRequest(ctx, "refresh_token", raw, client, "", s.Strategy(), func(ctx context.Context) error {
			root, operationErr3 := s.root(ctx)
			if operationErr3 != nil {
				return operationErr3
			}
			return s.revoke(ctx, root.verifiedFamily)
		})
		if err == nil {
			return nil
		}
	}
	claims, err := s.ValidateAccess(ctx, raw)
	if err == nil && claims.ClientID == client {
		return s.RevokeFamily(ctx, claims.Subject, claims.FamilyID)
	}
	return nil
}

// Fresh CredentialBinding checks the live owner/deadline after cryptographic lookup;
// protocol handlers supply their own account guard around this operation.
func (s *Store) Fresh(c *Claims) bool {
	return c != nil && c.AuthTime > 0 && time.Unix(c.AuthTime, 0).After(time.Now().Add(-5*time.Minute)) && !time.Unix(c.AuthTime, 0).After(time.Now().Add(time.Second))
}

// HasScope reports whether the verified claims contain a required scope.
func HasScope(c *Claims, scope string) bool {
	if c == nil {
		return false
	}
	for _, s := range strings.Fields(c.Scope) {
		if s == scope {
			return true
		}
	}
	return false
}

// VerifySignature verifies the strict realm access-token signature and claims.
func (s *Store) VerifySignature(token string) (*Claims, error) { return s.realm.VerifySignature(token) }
