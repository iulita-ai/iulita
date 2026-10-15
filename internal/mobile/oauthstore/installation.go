package oauthstore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ory/fosite"
)

// InstallationJWK encodes a P-256 installation public key.
type InstallationJWK struct {
	Crv string `json:"crv"`
	Kty string `json:"kty"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// ParseInstallation validates a strictly shaped P-256 public installation key.
func ParseInstallation(data []byte) (InstallationJWK, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 4 {
		return InstallationJWK{}, ErrInvalid
	}
	for _, k := range []string{"crv", "kty", "x", "y"} {
		if fields[k] == nil {
			return InstallationJWK{}, ErrInvalid
		}
	}
	var j InstallationJWK
	if json.Unmarshal(data, &j) != nil {
		return j, ErrInvalid
	}
	_, err := j.PublicKey()
	return j, err
}

// PublicKey decodes and validates the installation public key.
func (j InstallationJWK) PublicKey() (*ecdsa.PublicKey, error) {
	if j.Kty != "EC" || j.Crv != "P-256" || !ValidSecret(j.X) || !ValidSecret(j.Y) {
		return nil, ErrInvalid
	}
	x, operationErr1 := base64.RawURLEncoding.DecodeString(j.X)
	if operationErr1 != nil {
		return nil, operationErr1
	}
	y, operationErr2 := base64.RawURLEncoding.DecodeString(j.Y)
	if operationErr2 != nil {
		return nil, operationErr2
	}
	encoded := make([]byte, 1, 65)
	encoded[0] = 4 // SEC 1 uncompressed point encoding.
	encoded = append(encoded, x...)
	encoded = append(encoded, y...)
	p, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), encoded)
	if err != nil {
		return nil, ErrInvalid
	}
	return p, nil
}

// Canonical encodes the public key in the fixed thumbprint member order.
func (j InstallationJWK) Canonical() string { b, _ := json.Marshal(j); return string(b) } //nolint:errcheck // This internal snapshot contains only JSON-supported values.

// Thumbprint returns the canonical installation-key thumbprint.
func (j InstallationJWK) Thumbprint() string {
	sum := sha256.Sum256([]byte(j.Canonical()))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Challenge binds installation proof to a purpose, device, family and deadline.
type Challenge struct {
	Issuer        string `json:"issuer"`
	OwnerID       string `json:"owner_id"`
	ClientID      string `json:"client_id"`
	DeviceID      string `json:"device_id"`
	FamilyID      string `json:"family_id"`
	OperationID   string `json:"operation_id"`
	KeyThumbprint string `json:"key_thumbprint"`
	Purpose       string `json:"purpose"`
	ExpiresAt     int64  `json:"expires_at"`
	Nonce         string `json:"nonce"`
	SigningInput  string `json:"signing_input,omitempty"`
}

func (c Challenge) signingBytes() []byte { c.SigningInput = ""; b, _ := json.Marshal(c); return b } //nolint:errcheck // This internal snapshot contains only JSON-supported values.
// Challenge creates a purpose-bound installation proof challenge.
func (s *Store) challengeFor(f Family, id, thumb, purpose, nonce string, expiry int64) Challenge {
	c := Challenge{Issuer: s.realm.issuer, OwnerID: f.OwnerID, ClientID: f.ClientID, DeviceID: f.DeviceID, FamilyID: f.ID, OperationID: id, KeyThumbprint: thumb, Purpose: purpose, ExpiresAt: expiry, Nonce: nonce}
	c.SigningInput = base64.RawURLEncoding.EncodeToString(c.signingBytes())
	return c
}

// Challenge creates a purpose-bound installation proof challenge.
func (s *Store) Challenge(ctx context.Context, r fosite.Requester) (*Challenge, error) {
	f, err := s.familyIn(ctx, r.GetID())
	if err != nil {
		return nil, err
	}
	if f.Stage == Active {
		return nil, nil
	}
	q, err := s.transaction(ctx)
	if err != nil {
		return nil, err
	}
	var thumb string
	if checkErr1 := q.QueryRowContext(ctx, `SELECT thumbprint FROM oauth_devices WHERE owner_id=? AND id=?`, f.OwnerID, f.DeviceID).Scan(&thumb); checkErr1 != nil {
		return nil, checkErr1
	}
	nonce, err := Secret()
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	purpose := "activate_device"
	if f.Stage == Recovery {
		purpose = "recover_device"
	}
	_, err = q.ExecContext(ctx, `INSERT INTO oauth_nonces(id,owner_id,device_id,family_id,purpose,key_thumbprint,nonce_hash,expires_at) VALUES(?,?,?,?,?,?,?,?)`, id, f.OwnerID, f.DeviceID, f.ID, purpose, thumb, s.realm.digest("nonce", nonce), f.Deadline.Unix())
	if err != nil {
		return nil, err
	}
	c := s.challengeFor(f, id, thumb, purpose, nonce, f.Deadline.Unix())
	return &c, nil
}

// BindInstallation must run after Fosite PKCE validation in TokenRequest.
func (s *Store) BindInstallation(ctx context.Context, r fosite.Requester, j InstallationJWK) error {
	if _, err := j.PublicKey(); err != nil {
		return err
	}
	session, validSession := r.GetSession().(*Session)
	if !validSession || session == nil {
		return fosite.ErrInvalidGrant
	}
	b := session.Binding
	if b.KeyThumbprint != j.Thumbprint() {
		return fosite.ErrInvalidGrant
	}
	q, err := s.transaction(ctx)
	if err != nil {
		return err
	}
	var old string
	if err = q.QueryRowContext(ctx, `SELECT public_jwk FROM oauth_devices WHERE owner_id=? AND id=? AND revoked=0`, b.OwnerID, b.DeviceID).Scan(&old); err != nil {
		return fosite.ErrInvalidGrant
	}
	if old != "" && old != j.Canonical() {
		return fosite.ErrInvalidGrant
	}
	if b.Stage == Recovery && old == "" {
		return fosite.ErrInvalidGrant
	}
	_, err = q.ExecContext(ctx, `UPDATE oauth_devices SET public_jwk=? WHERE owner_id=? AND id=? AND public_jwk IN ('',?)`, j.Canonical(), b.OwnerID, b.DeviceID, j.Canonical())
	return err
}

// FinishInstallation verifies the bound challenge and ECDSA proof, then records
// idempotent completion. Credentials retain their original provisional stage.
func (s *Store) FinishInstallation(ctx context.Context, c *Claims, operation, nonce string, signature []byte, jwk *InstallationJWK, label string) (Device, error) {
	if c == nil || !canonicalID(operation) || !ValidSecret(nonce) || len(signature) > 80 || len(label) > 128 || (c.Stage != Pending && c.Stage != Recovery) {
		return Device{}, ErrInvalid
	}
	var device Device
	err := s.WithRequest(ctx, func(ctx context.Context) error {
		q, operationErr3 := s.transaction(ctx)
		if operationErr3 != nil {
			return operationErr3
		}
		f, err := s.familyIn(ctx, c.FamilyID)
		if err != nil || s.live(ctx, f) != nil || f.OwnerID != c.Subject || f.DeviceID != c.DeviceID || f.Stage != c.Stage {
			return fosite.ErrInvalidGrant
		}
		var keyJSON, thumb string
		var epoch uint64
		var revoked, activated bool
		if err = q.QueryRowContext(ctx, `SELECT public_jwk,thumbprint,epoch,revoked,activated FROM oauth_devices WHERE owner_id=? AND id=?`, c.Subject, c.DeviceID).Scan(&keyJSON, &thumb, &epoch, &revoked, &activated); err != nil || revoked {
			return ErrInvalid
		}
		key, err := ParseInstallation([]byte(keyJSON))
		if err != nil {
			return err
		}
		if jwk != nil && jwk.Canonical() != keyJSON {
			return ErrInvalid
		}
		if c.Stage == Pending && jwk == nil {
			return ErrInvalid
		}
		if c.Stage == Recovery && !activated {
			return ErrInvalid
		}
		purpose := "activate_device"
		if c.Stage == Recovery {
			purpose = "recover_device"
		}
		var stored []byte
		var expiry int64
		if err = q.QueryRowContext(ctx, `SELECT nonce_hash,expires_at FROM oauth_nonces WHERE id=? AND owner_id=? AND device_id=? AND family_id=? AND purpose=? AND key_thumbprint=?`, operation, c.Subject, c.DeviceID, c.FamilyID, purpose, thumb).Scan(&stored, &expiry); err != nil || !equalHash(stored, s.realm.digest("nonce", nonce)) || expiry <= time.Now().Unix() {
			return ErrInvalid
		}
		challenge := s.challengeFor(f, operation, thumb, purpose, nonce, expiry)
		sum := sha256.Sum256(challenge.signingBytes())
		public, operationErr4 := key.PublicKey()
		if operationErr4 != nil {
			return operationErr4
		}
		if !ecdsa.VerifyASN1(public, sum[:], signature) {
			return ErrInvalid
		}
		// Digest includes the semantic proof and label, so a replay cannot rename
		// or bind a different operation to a consumed nonce.
		data, _ := json.Marshal(struct { //nolint:errcheck // This internal snapshot contains only JSON-supported values.
			O, N, L string
			S       []byte
		}{operation, nonce, label, signature})
		digest := sha256.Sum256(data)
		var old []byte
		var previous string
		err = q.QueryRowContext(ctx, `SELECT operation_id,digest FROM oauth_completions WHERE family_id=?`, f.ID).Scan(&previous, &old)
		if err == nil {
			if previous != operation || !equalHash(old, digest[:]) {
				return ErrConflict
			}
			device = Device{ID: f.DeviceID, OwnerID: f.OwnerID, KeyThumbprint: thumb, Epoch: epoch}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if c.DeviceEpoch != f.DeviceEpoch || c.OwnerEpoch != f.OwnerEpoch {
			return ErrInvalid
		}
		if checkErr2 := s.ConsumeNonce(ctx, c.Subject, f.ID, operation, purpose, nonce); checkErr2 != nil {
			return checkErr2
		}
		if c.Stage == Recovery {
			if checkErr3 := invalidateDeviceState(ctx, q, c.Subject, c.DeviceID); checkErr3 != nil {
				return checkErr3
			}
			epoch++
			if _, err = q.ExecContext(ctx, `UPDATE oauth_devices SET epoch=? WHERE owner_id=? AND id=?`, epoch, c.Subject, c.DeviceID); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `UPDATE oauth_families SET revoked=1 WHERE owner_id=? AND device_id=? AND id<>?`, c.Subject, c.DeviceID, f.ID); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `UPDATE oauth_families SET device_epoch=? WHERE id=?`, epoch, f.ID); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `UPDATE oauth_sessions SET active=0 WHERE kind='access' AND family_id=?`, f.ID); err != nil {
				return err
			}
		}
		if _, err = q.ExecContext(ctx, `UPDATE oauth_devices SET activated=1,label=CASE WHEN ?<>'' THEN ? ELSE label END WHERE owner_id=? AND id=?`, label, label, c.Subject, c.DeviceID); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO oauth_completions VALUES(?,?,?,?,?)`, f.ID, operation, digest[:], purpose, time.Now().Unix()); err != nil {
			return err
		}
		device = Device{ID: f.DeviceID, OwnerID: f.OwnerID, KeyThumbprint: thumb, Epoch: epoch}
		return nil
	})
	return device, err
}

// FinalizeRefresh is called AFTER NewAccessRequest and BEFORE NewAccessResponse.
// Only one still-unused provisional refresh can finalize committed proof, within
// its original deadline. Old provisional JWTs never acquire active scopes.
func (s *Store) FinalizeRefresh(ctx context.Context, r fosite.AccessRequester) error {
	f, err := s.familyIn(ctx, r.GetID())
	if err != nil {
		return err
	}
	if f.Stage == Active {
		return nil
	}
	q, operationErr5 := s.transaction(ctx)
	if operationErr5 != nil {
		return operationErr5
	}
	var count int
	if checkErr4 := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_completions WHERE family_id=?`, f.ID).Scan(&count); checkErr4 != nil {
		return checkErr4
	}
	if count == 0 {
		return nil
	}
	if !f.Deadline.After(time.Now()) {
		return fosite.ErrInvalidGrant
	}
	if _, err = q.ExecContext(ctx, `UPDATE oauth_families SET stage='active',deadline=0 WHERE id=? AND stage=?`, f.ID, f.Stage); err != nil {
		return err
	}
	session, validSession := r.GetSession().(*Session)
	if !validSession || session == nil {
		return fosite.ErrInvalidGrant
	}
	b := &session.Binding
	b.Stage = Active
	b.DeviceEpoch = f.DeviceEpoch
	b.OwnerEpoch = f.OwnerEpoch
	for _, scope := range f.ConsentScopes {
		r.GrantScope(scope)
	}
	return nil
}
