package oauthstore

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/securetoken"
)

const keyFile = "mobile-realm-v1.json"
const pendingFile = "rotation-pending.json"

// ErrKeys rejects an invalid or mismatched realm key set.
var ErrKeys = errors.New("mobile OAuth keys are unavailable or invalid")

// Stage identifies an OAuth credential lifecycle stage.
type Stage string

// Credential lifecycle stages are independent of access-token generations.
const (
	// Pending identifies credentials awaiting installation activation.
	Pending  Stage = "pending_activation"
	Recovery Stage = "provisional_recovery"
	Active   Stage = "active"
)

func (s Stage) valid() bool { return s == Pending || s == Recovery || s == Active }
func (s Stage) ttl() time.Duration {
	switch s {
	case Pending:
		return 10 * time.Minute
	case Recovery:
		return 5 * time.Minute
	default:
		return 15 * time.Minute
	}
}
func canonicalID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u != uuid.Nil && u.String() == id
}
func trustedIssuer(v string) bool {
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.String() == strings.TrimRight(v, "/")
}

// Realm contains only unexported secret fields. JWKS exposes public RSA material.
// Initialize is explicit; Open never regenerates keys after a missing/corrupt file.
type Realm struct {
	issuer, audience, id, kid   string
	epoch                       uint64
	private                     *rsa.PrivateKey
	hmacKey, hashKey, cursorKey []byte
}
type diskKeys struct {
	Version                        int
	RealmID, Kid, Issuer, Audience string
	SigningEpoch                   uint64
	PrivateDER, HMAC, Hash, Cursor []byte
}

func privateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return ErrKeys
	}
	return nil
}

// InitializeRealm creates a new realm key set without replacing an existing realm.
func InitializeRealm(dir, issuer, audience string) (*Realm, error) {
	if !trustedIssuer(issuer) || audience == "" {
		return nil, ErrKeys
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, ErrKeys
	}
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(dir, pendingFile)); !os.IsNotExist(err) {
		return nil, ErrKeys
	}
	path := filepath.Join(dir, keyFile)
	if _, err := os.Lstat(path); err == nil {
		return LoadRealm(dir, issuer, audience)
	} else if !os.IsNotExist(err) {
		return nil, ErrKeys
	}
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, err
	}
	d := diskKeys{Version: 1, RealmID: uuid.NewString(), Kid: uuid.NewString(), Issuer: issuer, Audience: audience, SigningEpoch: 1, PrivateDER: x509.MarshalPKCS1PrivateKey(key), HMAC: make([]byte, 32), Hash: make([]byte, 32), Cursor: make([]byte, 32)}
	for _, b := range [][]byte{d.HMAC, d.Hash, d.Cursor} {
		if _, err = rand.Read(b); err != nil {
			return nil, err
		}
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, ErrKeys
	}
	f, err := os.CreateTemp(dir, ".mobile-keys-*")
	if err != nil {
		return nil, ErrKeys
	}
	defer os.Remove(f.Name()) //nolint:errcheck // Best-effort cleanup of a private temporary file.
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return nil, ErrKeys
	}
	// Link publishes a complete fsynced file atomically, without replacing a
	// concurrently initialized realm. Existing files are never silently reset.
	if err = os.Link(f.Name(), path); err != nil && !os.IsExist(err) {
		return nil, ErrKeys
	}
	df, err := os.Open(dir) //nolint:gosec // Path comes from explicit local runtime configuration, not remote input.
	if err != nil {
		return nil, ErrKeys
	}
	err = df.Sync()
	df.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return nil, ErrKeys
	}
	return LoadRealm(dir, issuer, audience)
}

// LoadRealm loads and validates an initialized realm key set.
func LoadRealm(dir, issuer, audience string) (*Realm, error) {
	return loadRealm(dir, issuer, audience, false)
}
func loadRealm(dir, issuer, audience string, maintenance bool) (*Realm, error) {
	if !maintenance {
		if _, err := os.Lstat(filepath.Join(dir, pendingFile)); !os.IsNotExist(err) {
			return nil, ErrKeys
		}
	}
	if !trustedIssuer(issuer) || audience == "" || privateDir(dir) != nil {
		return nil, ErrKeys
	}
	path := filepath.Join(dir, keyFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 16384 {
		return nil, ErrKeys
	}
	b, err := os.ReadFile(path) //nolint:gosec // Path comes from explicit local runtime configuration, not remote input.
	if err != nil {
		return nil, ErrKeys
	}
	var d diskKeys
	if json.Unmarshal(b, &d) != nil || d.Version != 1 || d.Issuer != issuer || d.Audience != audience || !canonicalID(d.RealmID) || !canonicalID(d.Kid) || d.SigningEpoch == 0 || len(d.HMAC) != 32 || len(d.Hash) != 32 || len(d.Cursor) != 32 {
		return nil, ErrKeys
	}
	key, err := x509.ParsePKCS1PrivateKey(d.PrivateDER)
	if err != nil || key.N.BitLen() < 3072 || key.Validate() != nil {
		return nil, ErrKeys
	}
	return &Realm{issuer: issuer, audience: audience, id: d.RealmID, kid: d.Kid, epoch: d.SigningEpoch, private: key, hmacKey: d.HMAC, hashKey: d.Hash, cursorKey: d.Cursor}, nil
}

// CursorSigner returns a signer using the dedicated cursor key.
func (r *Realm) CursorSigner() (*securetoken.Signer, error) { return securetoken.New(r.cursorKey) }
func (r *Realm) digest(purpose, value string) []byte {
	m := hmac.New(sha256.New, r.hashKey)
	m.Write([]byte(purpose + "\x00" + value))
	return m.Sum(nil)
}

// JWK contains the public portion of a realm signing key.
type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS returns only the public realm signing keys.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWKS returns only the public realm signing keys.
func (r *Realm) JWKS() JWKS {
	return JWKS{Keys: []JWK{{Kty: "RSA", Use: "sig", Alg: "RS256", Kid: r.kid, N: base64.RawURLEncoding.EncodeToString(r.private.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(r.private.E)).Bytes())}}}
}

// Claims contains the strict mobile access-token claims.
type Claims struct {
	jwt.RegisteredClaims
	Audience     string `json:"aud"`
	ClientID     string `json:"client_id"`
	DeviceID     string `json:"device_id"`
	FamilyID     string `json:"family_id"`
	Stage        Stage  `json:"token_stage"`
	Scope        string `json:"scope"`
	OwnerEpoch   uint64 `json:"owner_security_epoch"`
	DeviceEpoch  uint64 `json:"device_epoch"`
	SigningEpoch uint64 `json:"signing_epoch"`
	AuthTime     int64  `json:"auth_time,omitempty"`
}

// GetAudience returns the configured mobile token audience.
func (c Claims) GetAudience() (jwt.ClaimStrings, error) { return jwt.ClaimStrings{c.Audience}, nil }
func (r *Realm) validClaims(c *Claims) bool {
	if c.IssuedAt != nil && c.IssuedAt.Unix() <= 0 {
		return false
	}
	if c.Issuer != r.issuer || c.Audience != r.audience || !canonicalID(c.Subject) || !canonicalID(c.DeviceID) || !canonicalID(c.FamilyID) || c.ClientID == "" || c.ID == "" || !c.Stage.valid() || c.OwnerEpoch == 0 || c.DeviceEpoch == 0 || c.SigningEpoch != r.epoch || c.IssuedAt == nil || c.ExpiresAt == nil || c.NotBefore != nil {
		return false
	}
	ttl := c.ExpiresAt.Sub(c.IssuedAt.Time)
	if c.AuthTime < 0 || ttl <= 0 || ttl > c.Stage.ttl() || c.IssuedAt.After(time.Now().Add(time.Second)) || c.AuthTime > c.IssuedAt.Unix() {
		return false
	}
	return (c.Stage == Active && strings.TrimSpace(c.Scope) != "") || (c.Stage != Active && c.Scope == "")
}

// SignAccess SignAccess/VerifySignature are cryptographic primitives, not resource
// authorization. ValidateAccess additionally checks persisted sessions/epochs.
func (r *Realm) SignAccess(c Claims) (string, error) {
	c.Issuer = r.issuer
	c.Audience = r.audience
	c.SigningEpoch = r.epoch
	if !r.validClaims(&c) {
		return "", fositeInvalid()
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	t.Header["typ"] = "at+jwt"
	t.Header["kid"] = r.kid
	return t.SignedString(r.private)
}
func fositeInvalid() error { return errors.New("invalid mobile OAuth credential") }

// VerifySignature verifies the strict realm access-token signature and claims.
func (r *Realm) VerifySignature(token string) (*Claims, error) {
	if len(token) > 16384 {
		return nil, fositeInvalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fositeInvalid()
	}
	payload, decodeErr := base64.RawURLEncoding.DecodeString(parts[1])
	if decodeErr != nil {
		return nil, fositeInvalid()
	}
	c := new(Claims)
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(c) != nil {
		return nil, fositeInvalid()
	}
	_, err := jwt.ParseWithClaims(token, c, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodRS256 || t.Header["typ"] != "at+jwt" || t.Header["kid"] != r.kid {
			return nil, fositeInvalid()
		}
		return &r.private.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(r.issuer), jwt.WithAudience(r.audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !r.validClaims(c) {
		return nil, fositeInvalid()
	}
	return c, nil
}

// PublicJWKS returns only the public realm signing keys.
func (s *Store) PublicJWKS() JWKS { return s.realm.JWKS() }
