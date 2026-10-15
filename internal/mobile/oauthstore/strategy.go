package oauthstore

import (
	"context"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/handler/oauth2"
)

// Strategy delegates authorization code/refresh cryptography to Fosite while
// issuing the exact mobile RS256 at+jwt profile with fresh iat/jti per issuance.
// Strategy binds Fosite token generation to the mobile realm.
type Strategy struct {
	oauth2.CoreStrategy
	store *Store
}

// Strategy returns the realm-bound Fosite token strategy.
func (s *Store) Strategy() *Strategy {
	return &Strategy{compose.NewOAuth2HMACStrategy(s.FrameworkConfig()), s}
}

// AccessTokenSignature implements the realm-bound Fosite AccessTokenSignature token contract.
func (s *Strategy) AccessTokenSignature(ctx context.Context, token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	return parts[2]
}

// GenerateAccessToken implements the realm-bound Fosite GenerateAccessToken token contract.
func (s *Strategy) GenerateAccessToken(ctx context.Context, r fosite.Requester) (tokenValue, signatureValue string, returnErr error) {
	session, ok := r.GetSession().(*Session)
	if !ok {
		return "", "", ErrInvalid
	}
	b := session.Binding
	f, err := s.store.familyIn(ctx, r.GetID())
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC().Truncate(time.Second)
	expiry := now.Add(b.Stage.ttl())
	if f.ExpiresAt.Before(expiry) {
		expiry = f.ExpiresAt
	}
	if !f.Deadline.IsZero() && f.Deadline.Before(expiry) {
		expiry = f.Deadline
	}
	if !expiry.After(now) {
		return "", "", fosite.ErrInvalidGrant
	}
	session.JWTID = uuid.NewString()
	session.IssuedAt = now
	session.claims = nil
	session.SetExpiresAt(fosite.AccessToken, expiry)
	c := Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: s.store.realm.issuer, Subject: b.OwnerID, ID: session.JWTID, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiry)}, Audience: s.store.realm.audience, ClientID: b.ClientID, DeviceID: b.DeviceID, FamilyID: b.FamilyID, Stage: b.Stage, Scope: strings.Join(r.GetGrantedScopes(), " "), OwnerEpoch: b.OwnerEpoch, DeviceEpoch: b.DeviceEpoch, SigningEpoch: b.SigningEpoch, AuthTime: b.AuthTime}
	token, err := s.store.realm.SignAccess(c)
	if err != nil {
		return "", "", err
	}
	return token, s.AccessTokenSignature(ctx, token), nil
}

// ValidateAccessToken implements the realm-bound Fosite ValidateAccessToken token contract.
func (s *Strategy) ValidateAccessToken(ctx context.Context, r fosite.Requester, token string) error {
	c, err := s.store.realm.VerifySignature(token)
	if err != nil {
		return fosite.ErrRequestUnauthorized
	}
	session, ok := r.GetSession().(*Session)
	if !ok || c.FamilyID != session.Binding.FamilyID || c.Subject != session.Binding.OwnerID {
		return fosite.ErrRequestUnauthorized
	}
	return nil
}

// GenerateRefreshToken implements the realm-bound Fosite GenerateRefreshToken token contract.
func (s *Strategy) GenerateRefreshToken(ctx context.Context, r fosite.Requester) (tokenValue, signatureValue string, returnErr error) {
	f, err := s.store.familyIn(ctx, r.GetID())
	if err != nil {
		return "", "", err
	}
	expiry := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	if f.ExpiresAt.Before(expiry) {
		expiry = f.ExpiresAt
	}
	if !f.Deadline.IsZero() && f.Deadline.Before(expiry) {
		expiry = f.Deadline
	}
	r.GetSession().SetExpiresAt(fosite.RefreshToken, expiry)
	return s.CoreStrategy.GenerateRefreshToken(ctx, r)
}
