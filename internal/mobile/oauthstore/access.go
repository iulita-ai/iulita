package oauthstore

import (
	"context"
	"strings"
)

// ValidateAccess combines strict mobile JWT signature/profile checking with
// persisted session, family, owner/device/signing epochs and expiry. It supplies
// no RPC scope/freshness/feature policy (BE19) and never accepts dashboard JWTs.
func (s *Store) ValidateAccess(ctx context.Context, token string) (*Claims, error) {
	c, err := s.realm.VerifySignature(token)
	if err != nil {
		return nil, err
	}
	sig := token[strings.LastIndexByte(token, '.')+1:]
	err = s.WithRequest(ctx, func(ctx context.Context) error {
		r, stepErr1 := s.get(ctx, "access", sig)
		if stepErr1 != nil {
			return fositeInvalid()
		}
		session, validSession := r.GetSession().(*Session)
		if !validSession || session == nil {
			return fositeInvalid()
		}
		b := session.Binding
		if b.OwnerID != c.Subject || b.DeviceID != c.DeviceID || b.FamilyID != c.FamilyID || b.ClientID != c.ClientID || b.Stage != c.Stage || b.OwnerEpoch != c.OwnerEpoch || b.DeviceEpoch != c.DeviceEpoch || b.SigningEpoch != c.SigningEpoch || b.AuthTime != c.AuthTime || strings.Join(r.GetGrantedScopes(), " ") != c.Scope {
			return fositeInvalid()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}
