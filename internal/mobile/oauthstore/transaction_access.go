package oauthstore

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// ValidateIn shares the OAuth database transaction with command/receipt
// transitions. A revoke/refresh cannot race between authorization and commit.
// The caller must also hold the short account-authority guard.
func (s *Store) ValidateIn(ctx context.Context, tx *sql.Tx, token string) (*Claims, error) {
	c, err := s.realm.VerifySignature(token)
	if err != nil {
		return nil, err
	}
	if c.PrincipalType == "service" {
		return nil, ErrInvalid
	}
	sig := token[strings.LastIndexByte(token, '.')+1:]
	var data []byte
	var generation uint64
	err = tx.QueryRowContext(ctx, `SELECT x.data,f.generation FROM oauth_sessions x JOIN oauth_families f ON f.id=x.family_id JOIN oauth_devices d ON d.id=f.device_id AND d.owner_id=f.owner_id JOIN oauth_owners o ON o.id=f.owner_id JOIN oauth_realm r ON r.id=? WHERE x.kind='access' AND x.key_hash=? AND x.active=1 AND x.expires_at>? AND x.generation=f.generation AND f.revoked=0 AND f.expires_at>? AND (f.deadline=0 OR f.deadline>?) AND d.revoked=0 AND f.owner_epoch=o.epoch AND f.device_epoch=d.epoch AND f.signing_epoch=r.signing_epoch AND r.kid=? AND f.owner_id=? AND f.device_id=? AND f.id=? AND f.client_id=? AND f.stage=? AND f.owner_epoch=? AND f.device_epoch=? AND f.signing_epoch=?`, s.realm.id, s.realm.digest("session.access", sig), time.Now().Unix(), time.Now().Unix(), time.Now().Unix(), s.realm.kid, c.Subject, c.DeviceID, c.FamilyID, c.ClientID, c.Stage, c.OwnerEpoch, c.DeviceEpoch, c.SigningEpoch).Scan(&data, &generation)
	if err != nil {
		return nil, fositeInvalid()
	}
	req, err := decode(data)
	if err != nil {
		return nil, err
	}
	session, validSession := req.GetSession().(*Session)
	if !validSession || session == nil {
		return nil, fositeInvalid()
	}
	b := session.Binding
	if b.OwnerID != c.Subject || b.DeviceID != c.DeviceID || b.FamilyID != c.FamilyID || b.ClientID != c.ClientID || b.Stage != c.Stage || b.OwnerEpoch != c.OwnerEpoch || b.DeviceEpoch != c.DeviceEpoch || b.SigningEpoch != c.SigningEpoch || b.Generation != generation || b.AuthTime != c.AuthTime || strings.Join(req.GetGrantedScopes(), " ") != c.Scope {
		return nil, fositeInvalid()
	}
	return c, nil
}
