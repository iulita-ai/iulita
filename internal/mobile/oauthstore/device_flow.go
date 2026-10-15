package oauthstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ory/fosite"
)

// DeviceAuthorization contains the one-time RFC8628 credentials returned only at creation.
type DeviceAuthorization struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
}

func normalizeUserCode(value string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
}

// CreateDeviceAuthorization starts an explicitly registered CLI flow.
func (s *Store) CreateDeviceAuthorization(ctx context.Context, client, scope, thumb string) (*DeviceAuthorization, error) {
	registration, err := s.ClientPolicy(ctx, client)
	if err != nil || registration.Kind != "cli" || !ValidSecret(thumb) {
		return nil, ErrInvalid
	}
	scopes := strings.Fields(scope)
	if len(scopes) == 0 {
		scopes = registration.Scopes
	}
	if !validScopes(scopes) || !subset(scopes, registration.Scopes) {
		return nil, ErrInvalid
	}
	deviceCode, err := Secret()
	if err != nil {
		return nil, err
	}
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		return nil, err
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // 32 symbols, 40 bits.
	for i := range random {
		random[i] = alphabet[int(random[i])%len(alphabet)]
	}
	userCode := string(random)
	err = s.WithRequest(ctx, func(ctx context.Context) error {
		tx, txErr := s.transaction(ctx)
		if txErr != nil {
			return txErr
		}
		current, _, policyErr := s.clientPolicy(ctx, client, false)
		if policyErr != nil || current.Kind != "cli" {
			return ErrInvalid
		}
		var count int
		if checkErr1 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_device_requests WHERE client_id=? AND expires_at>? AND state IN ('pending','approved')`, client, time.Now().Unix()).Scan(&count); checkErr1 != nil {
			return checkErr1
		}
		if count >= 128 {
			return ErrConflict
		}
		_, txErr = tx.ExecContext(ctx, `INSERT INTO oauth_device_requests(id,client_id,code_hash,user_hash,key_thumbprint,scope,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), client, s.realm.digest("device.code", deviceCode), s.realm.digest("device.user", userCode), thumb, strings.Join(scopes, " "), time.Now().Add(10*time.Minute).Unix(), time.Now().Unix())
		return txErr
	})
	if err != nil {
		return nil, err
	}
	return &DeviceAuthorization{DeviceCode: deviceCode, UserCode: userCode[:4] + "-" + userCode[4:], VerificationURI: s.Issuer() + "/oauth2/device", ExpiresIn: 600, Interval: 5}, nil
}

// DeviceBrowserAuthorization resolves a manually entered user code; it grants no authority.
func (s *Store) DeviceBrowserAuthorization(ctx context.Context, userCode string) (Authorization, error) {
	var a Authorization
	code := normalizeUserCode(userCode)
	if len(code) != 8 {
		return a, ErrInvalid
	}
	err := s.WithRequest(ctx, func(ctx context.Context) error {
		tx, txErr := s.transaction(ctx)
		if txErr != nil {
			return txErr
		}
		txErr = tx.QueryRowContext(ctx, `SELECT id,client_id,scope,key_thumbprint FROM oauth_device_requests WHERE user_hash=? AND state='pending' AND expires_at>?`, s.realm.digest("device.user", code), time.Now().Unix()).Scan(&a.DeviceFlowID, &a.ClientID, &a.Scope, &a.KeyThumbprint)
		if txErr != nil {
			return ErrInvalid
		}
		registration, _, policyErr := s.clientPolicy(ctx, a.ClientID, false)
		if policyErr != nil || registration.Kind != "cli" {
			return ErrInvalid
		}
		return nil
	})
	return a, err
}

// DecideDeviceAuthorization commits consent under the browser-flow/account transaction.
func (s *Store) DecideDeviceAuthorization(ctx context.Context, f BrowserFlow, userCode string, approve bool) error {
	tx, err := s.transaction(ctx)
	if err != nil {
		return err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM oauth_device_requests WHERE id=? AND client_id=? AND user_hash=? AND key_thumbprint=? AND scope=? AND state='pending' AND expires_at>?`, f.Request.DeviceFlowID, f.Request.ClientID, s.realm.digest("device.user", normalizeUserCode(userCode)), f.Request.KeyThumbprint, f.Request.Scope, time.Now().Unix()).Scan(&id)
	if err != nil {
		return ErrInvalid
	}
	registration, _, err := s.clientPolicy(ctx, f.Request.ClientID, false)
	if err != nil || registration.Kind != "cli" {
		return ErrInvalid
	}
	if !approve {
		_, err = tx.ExecContext(ctx, `UPDATE oauth_device_requests SET state='denied' WHERE id=?`, id)
		return err
	}
	session, err := s.PrepareAuthorization(ctx, f)
	if err != nil {
		return err
	}
	var accountStamp []byte
	if err = tx.QueryRowContext(ctx, `SELECT account_stamp FROM oauth_owners WHERE id=?`, f.OwnerID).Scan(&accountStamp); err != nil || len(accountStamp) != 32 {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, `UPDATE oauth_device_requests SET state='approved',owner_id=?,family_id=?,credential_stamp=?,auth_time=? WHERE id=?`, f.OwnerID, session.Binding.FamilyID, accountStamp, f.AuthTime.Unix(), id)
	return err
}

// PollDeviceAuthorization persists RFC8628 throttling even for pending responses.
// An approved owner is returned before acquiring the account guard; consumption rechecks it.
func (s *Store) PollDeviceAuthorization(ctx context.Context, client, code string, key InstallationJWK) (owner, protocolError string, returnErr error) {
	if !ValidSecret(code) {
		return "", "invalid_grant", nil
	}
	returnErr = s.WithRequest(ctx, func(ctx context.Context) error {
		registration, _, err := s.clientPolicy(ctx, client, false)
		if errors.Is(err, ErrNotFound) {
			protocolError = "unauthorized_client"
			return nil
		}
		if err != nil {
			return err
		}
		if registration.Kind != "cli" {
			protocolError = "unauthorized_client"
			return nil
		}
		tx, err := s.transaction(ctx)
		if err != nil {
			return err
		}
		var state, thumb, id string
		var expiry, last, interval int64
		err = tx.QueryRowContext(ctx, `SELECT id,state,key_thumbprint,expires_at,last_poll,interval,owner_id FROM oauth_device_requests WHERE code_hash=? AND client_id=?`, s.realm.digest("device.code", code), client).Scan(&id, &state, &thumb, &expiry, &last, &interval, &owner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) || thumb != key.Thumbprint() {
			owner = ""
			protocolError = "invalid_grant"
			return nil
		}
		now := time.Now().Unix()
		if expiry <= now {
			owner = ""
			protocolError = "expired_token"
			return nil
		}
		if state == "denied" {
			owner = ""
			protocolError = "access_denied"
			return nil
		}
		if state == "consumed" {
			owner = ""
			protocolError = "invalid_grant"
			return nil
		}
		if last != 0 && now-last < interval {
			interval = min(interval+5, 600)
			protocolError = "slow_down"
		} else if state == "pending" {
			protocolError = "authorization_pending"
		}
		_, err = tx.ExecContext(ctx, `UPDATE oauth_device_requests SET last_poll=?,interval=? WHERE id=?`, now, interval, id)
		if protocolError != "" {
			owner = ""
		}
		return err
	})
	return
}

// ConsumeDeviceAuthorization atomically issues a key-bound pending/recovery family once.
// Caller holds the account guard and has synchronized its current epoch.
func (s *Store) ConsumeDeviceAuthorization(ctx context.Context, client, code, owner string, key InstallationJWK) (*TokenReply, error) {
	var reply *TokenReply
	err := s.WithRequest(ctx, func(ctx context.Context) error {
		tx, err := s.transaction(ctx)
		if err != nil {
			return err
		}
		var id, family, thumb string
		var stamp, current []byte
		err = tx.QueryRowContext(ctx, `SELECT id,family_id,key_thumbprint,credential_stamp FROM oauth_device_requests WHERE code_hash=? AND client_id=? AND owner_id=? AND state='approved' AND expires_at>?`, s.realm.digest("device.code", code), client, owner, time.Now().Unix()).Scan(&id, &family, &thumb, &stamp)
		if err != nil || thumb != key.Thumbprint() {
			return ErrInvalid
		}
		if err = tx.QueryRowContext(ctx, `SELECT account_stamp FROM oauth_owners WHERE id=?`, owner).Scan(&current); err != nil || !equalHash(stamp, current) {
			return ErrInvalid
		}
		f, err := s.familyIn(ctx, family)
		if err != nil {
			return err
		}
		if checkErr2 := s.live(ctx, f); checkErr2 != nil {
			return checkErr2
		}
		registered, err := s.GetClient(ctx, client)
		if err != nil {
			return err
		}
		session := &Session{Binding: Binding{OwnerID: owner, DeviceID: f.DeviceID, FamilyID: family, ClientID: client, Kid: s.realm.kid, KeyThumbprint: thumb, Stage: f.Stage, OwnerEpoch: f.OwnerEpoch, DeviceEpoch: f.DeviceEpoch, SigningEpoch: f.SigningEpoch, Generation: f.Generation, AuthTime: f.AuthTime.Unix()}}
		request := fosite.NewAccessRequest(session)
		request.SetID(family)
		request.Client = registered
		request.RequestedScope = f.ConsentScopes
		request.RequestedAudience = []string{s.Audience()}
		request.GrantedAudience = []string{s.Audience()}
		if checkErr3 := s.BindInstallation(ctx, request, key); checkErr3 != nil {
			return checkErr3
		}
		strategy := s.Strategy()
		access, accessSig, err := strategy.GenerateAccessToken(ctx, request)
		if err != nil {
			return err
		}
		refresh, refreshSig, err := strategy.GenerateRefreshToken(ctx, request)
		if err != nil {
			return err
		}
		if checkErr4 := s.CreateAccessTokenSession(ctx, accessSig, request); checkErr4 != nil {
			return checkErr4
		}
		if checkErr5 := s.CreateRefreshTokenSession(ctx, refreshSig, accessSig, request); checkErr5 != nil {
			return checkErr5
		}
		challenge, err := s.Challenge(ctx, request)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE oauth_device_requests SET state='consumed' WHERE id=?`, id); err != nil {
			return err
		}
		reply = &TokenReply{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", ExpiresIn: session.GetExpiresAt(fosite.AccessToken).Unix() - time.Now().Unix(), Stage: f.Stage, Challenge: challenge}
		return nil
	})
	return reply, err
}
