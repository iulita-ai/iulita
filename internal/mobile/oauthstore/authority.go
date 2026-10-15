package oauthstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Authority is a server-derived business snapshot, never a bearer credential.
// Refresh may change access generation without changing family authority;
// revocation still fences every new phone task and dispatch.
// Authority captures verified caller authority without retaining bearer credentials.
type Authority struct {
	Issuer, OwnerID, DeviceID, FamilyID, ClientID string
	OwnerEpoch, DeviceEpoch, SigningEpoch         uint64
	AuthTime                                      int64
	Scopes                                        []string
}

// Authority returns the current verified caller authority snapshot.
func (s *Store) Authority(c *Claims) Authority {
	if c == nil {
		return Authority{}
	}
	return Authority{Issuer: s.Issuer(), OwnerID: c.Subject, DeviceID: c.DeviceID, FamilyID: c.FamilyID, ClientID: c.ClientID, OwnerEpoch: c.OwnerEpoch, DeviceEpoch: c.DeviceEpoch, SigningEpoch: c.SigningEpoch, AuthTime: c.AuthTime, Scopes: strings.Fields(c.Scope)}
}

// ValidateAuthorityIn validates authority inside the caller-owned OAuth transaction.
func (s *Store) ValidateAuthorityIn(ctx context.Context, tx *sql.Tx, a Authority, owner string) error {
	if a.Issuer != s.Issuer() || a.OwnerID != owner || !canonicalID(a.FamilyID) || !canonicalID(a.DeviceID) {
		return ErrInvalid
	}
	var consent []byte
	var authTime int64
	err := tx.QueryRowContext(ctx, `SELECT f.consent_scopes,f.auth_time FROM oauth_families f JOIN oauth_owners o ON o.id=f.owner_id JOIN oauth_devices d ON d.id=f.device_id AND d.owner_id=f.owner_id JOIN oauth_realm r ON r.id=? WHERE f.id=? AND f.owner_id=? AND f.device_id=? AND f.client_id=? AND f.stage='active' AND f.revoked=0 AND f.expires_at>? AND f.owner_epoch=o.epoch AND f.device_epoch=d.epoch AND d.revoked=0 AND d.activated=1 AND f.signing_epoch=r.signing_epoch AND f.owner_epoch=? AND f.device_epoch=? AND f.signing_epoch=?`, s.realm.id, a.FamilyID, owner, a.DeviceID, a.ClientID, time.Now().Unix(), a.OwnerEpoch, a.DeviceEpoch, a.SigningEpoch).Scan(&consent, &authTime)
	if err != nil {
		return err
	}
	var scopes []string
	if json.Unmarshal(consent, &scopes) != nil || authTime != a.AuthTime {
		return ErrInvalid
	}
	for _, wanted := range a.Scopes {
		found := false
		for _, allowed := range scopes {
			if wanted == allowed {
				found = true
				break
			}
		}
		if !found {
			return ErrInvalid
		}
	}
	return nil
}

// ValidateAuthority validates authority against current persistent security state.
func (s *Store) ValidateAuthority(ctx context.Context, a Authority) error {
	return s.WithRequest(ctx, func(ctx context.Context) error {
		root, operationErr1 := s.root(ctx)
		if operationErr1 != nil {
			return operationErr1
		}
		return s.ValidateAuthorityIn(ctx, root.tx, a, a.OwnerID)
	})
}
