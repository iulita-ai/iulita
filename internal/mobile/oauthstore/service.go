package oauthstore

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TokenReply is the custom grant response; raw credentials are never persisted.
type TokenReply struct {
	AccessToken   string     `json:"access_token"`
	TokenType     string     `json:"token_type"`
	ExpiresIn     int64      `json:"expires_in"`
	RefreshToken  string     `json:"refresh_token,omitempty"`
	Scope         string     `json:"scope"`
	Stage         Stage      `json:"iulita_token_stage,omitempty"`
	PrincipalType string     `json:"iulita_principal_type,omitempty"`
	Challenge     *Challenge `json:"iulita_challenge,omitempty"`
}

// IssueServiceToken issues short-lived, persisted client_credentials authority.
// The caller must hold the current account guard and synchronize its epoch.
func (s *Store) IssueServiceToken(ctx context.Context, id, secret, requested string) (*TokenReply, error) {
	var reply *TokenReply
	err := s.WithRequest(ctx, func(ctx context.Context) error {
		r, err := s.serviceClient(ctx, id, secret)
		if err != nil {
			return err
		}
		scopes := strings.Fields(requested)
		if len(scopes) == 0 {
			scopes = []string{"chats:read"}
		}
		if !validScopes(scopes) || !subset(scopes, r.Scopes) || !subset(scopes, serviceCeiling) {
			return ErrScope
		}
		tx, err := s.transaction(ctx)
		if err != nil {
			return err
		}
		var ownerEpoch uint64
		if err = tx.QueryRowContext(ctx, `SELECT epoch FROM oauth_owners WHERE id=? AND account_stamp IS NOT NULL`, r.OwnerID).Scan(&ownerEpoch); err != nil {
			return ErrInvalid
		}
		now := time.Now().UTC().Truncate(time.Second)
		expiry := now.Add(5 * time.Minute)
		var count int
		if checkErr1 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_service_access WHERE client_id=? AND principal_id=? AND owner_id=? AND client_epoch=? AND owner_epoch=? AND signing_epoch=? AND revoked=0 AND expires_at>?`, id, r.PrincipalID, r.OwnerID, r.Epoch, ownerEpoch, s.realm.epoch, now.Unix()).Scan(&count); checkErr1 != nil {
			return checkErr1
		}
		if count >= 128 {
			return ErrConflict
		}
		c := Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: r.OwnerID, ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiry)}, ClientID: id, Stage: Active, Scope: strings.Join(scopes, " "), OwnerEpoch: ownerEpoch, PrincipalType: "service", ServiceID: r.PrincipalID, ClientEpoch: r.Epoch}
		raw, err := s.realm.SignAccess(c)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO oauth_service_access(grant_id,client_id,principal_id,owner_id,client_epoch,owner_epoch,signing_epoch,scope,expires_at) VALUES(?,?,?,?,?,?,?,?,?)`, c.ID, id, r.PrincipalID, r.OwnerID, r.Epoch, ownerEpoch, s.realm.epoch, c.Scope, expiry.Unix()); err != nil {
			return err
		}
		reply = &TokenReply{AccessToken: raw, TokenType: "Bearer", ExpiresIn: 300, Scope: c.Scope, PrincipalType: "service"}
		return nil
	})
	return reply, err
}

func (s *Store) validateServiceIn(ctx context.Context, tx *sql.Tx, c *Claims) error {
	var granted string
	err := tx.QueryRowContext(ctx, `SELECT a.scope FROM oauth_service_access a JOIN oauth_client_policy p ON p.client_id=a.client_id JOIN oauth_owners o ON o.id=a.owner_id JOIN oauth_realm r ON r.id=? WHERE a.grant_id=? AND a.client_id=? AND a.principal_id=? AND a.owner_id=? AND a.client_epoch=? AND a.owner_epoch=? AND a.signing_epoch=? AND a.revoked=0 AND a.expires_at>? AND p.kind='service' AND p.disabled=0 AND p.epoch=a.client_epoch AND p.principal_id=a.principal_id AND p.owner_id=a.owner_id AND o.epoch=a.owner_epoch AND r.signing_epoch=a.signing_epoch`, s.realm.id, c.ID, c.ClientID, c.ServiceID, c.Subject, c.ClientEpoch, c.OwnerEpoch, c.SigningEpoch, time.Now().Unix()).Scan(&granted)
	if err != nil || granted != c.Scope {
		return fositeInvalid()
	}
	r, _, err := s.clientPolicy(ctx, c.ClientID, false)
	if err != nil || !subset(strings.Fields(c.Scope), r.Scopes) || !subset(strings.Fields(c.Scope), serviceCeiling) {
		return fositeInvalid()
	}
	return nil
}

// RevokeServiceAccess revokes one authenticated service token without exposing it.
func (s *Store) RevokeServiceAccess(ctx context.Context, client, secret, token string) error {
	return s.WithRequest(ctx, func(ctx context.Context) error {
		if _, err := s.serviceClient(ctx, client, secret); err != nil {
			return err
		}
		c, err := s.realm.VerifySignature(token)
		if err != nil || c.PrincipalType != "service" || c.ClientID != client {
			return nil //nolint:nilerr // RFC7009 returns success for unknown or invalid tokens.
		}
		tx, err := s.transaction(ctx)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE oauth_service_access SET revoked=1 WHERE grant_id=? AND client_id=?`, c.ID, client)
		return err
	})
}
