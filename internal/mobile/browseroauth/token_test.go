package browseroauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

type tokenReply struct {
	TokenType string                `json:"token_type"`
	Access    string                `json:"access_token"`
	Refresh   string                `json:"refresh_token"`
	Stage     oauthstore.Stage      `json:"iulita_token_stage"`
	Scope     string                `json:"scope"`
	Expires   int                   `json:"expires_in"`
	Challenge *oauthstore.Challenge `json:"iulita_challenge"`
}

func installation(t *testing.T) (*ecdsa.PrivateKey, oauthstore.InstallationJWK) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	j := oauthstore.InstallationJWK{Crv: "P-256", Kty: "EC", X: base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))), Y: base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32)))}
	return key, j
}
func (f *fixture) code(t *testing.T, a oauthstore.Authorization) string {
	t.Helper()
	v, c := f.login(t, a)
	w := f.request("/oauth2/consent", v, c)
	if w.Code != 303 {
		t.Fatalf("authorization: %d %s", w.Code, w.Body.String())
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	return u.Query().Get("code")
}
func (f *fixture) exchange(t *testing.T, form url.Values) tokenReply {
	t.Helper()
	w := f.request("/oauth2/token", form, nil)
	var reply tokenReply
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil || reply.Access == "" || reply.Refresh == "" {
		t.Fatalf("token exchange: %d %s", w.Code, w.Body.String())
	}
	if reply.TokenType != "Bearer" {
		t.Fatal("token_type does not match the published UserTokenResponse schema")
	}
	if strings.Contains(w.Body.String(), "debug") {
		t.Fatal("debug leaked")
	}
	return reply
}
func codeExchange(code string, j oauthstore.InstallationJWK) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {"mobile"}, "redirect_uri": {testRedirect}, "code": {code}, "code_verifier": {testVerifier}, "iulita_installation_jwk": {j.Canonical()}}
}
func refreshExchange(secret string) url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "client_id": {"mobile"}, "refresh_token": {secret}}
}
func proof(t *testing.T, key *ecdsa.PrivateKey, c *oauthstore.Challenge) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(c.SigningInput)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestActualMobileTokenActivationRefreshAndReplay(t *testing.T) {
	f := setup(t)
	key, j := installation(t)
	a := f.a
	a.KeyThumbprint = j.Thumbprint()
	code := f.code(t, a)
	wrong := codeExchange(code, j)
	wrong.Set("code_verifier", strings.Repeat("x", 50))
	if w := f.request("/oauth2/token", wrong, nil); w.Code != 400 {
		t.Fatal("wrong PKCE accepted")
	}
	pending := f.exchange(t, codeExchange(code, j))
	if pending.Stage != oauthstore.Pending || pending.Scope != "" || pending.Challenge == nil || pending.Expires > 600 {
		t.Fatal("pending authority", pending)
	}
	c, err := f.store.ValidateAccess(testContext, pending.Access)
	if err != nil || c.Stage != oauthstore.Pending {
		t.Fatal("actual JWT validation", err)
	}
	if _, err = f.store.FinishInstallation(testContext, c, pending.Challenge.OperationID, pending.Challenge.Nonce, []byte("forged"), &j, "Pixel"); err == nil {
		t.Fatal("forged proof activated")
	}
	sig := proof(t, key, pending.Challenge)
	device, err := f.store.FinishInstallation(testContext, c, pending.Challenge.OperationID, pending.Challenge.Nonce, sig, &j, "Pixel")
	if err != nil {
		t.Fatal("activation", err)
	}
	again, err := f.store.FinishInstallation(testContext, c, pending.Challenge.OperationID, pending.Challenge.Nonce, sig, &j, "Pixel")
	if err != nil || again.ID != device.ID {
		t.Fatal("activation lost ACK", err)
	}
	if old, stepErr1 := f.store.ValidateAccess(testContext, pending.Access); stepErr1 != nil || old.Stage != oauthstore.Pending {
		t.Fatal("old JWT silently changed stage", stepErr1)
	}
	active := f.exchange(t, refreshExchange(pending.Refresh))
	if active.Stage != oauthstore.Active || active.Scope == "" || active.Challenge != nil || active.Expires > 900 {
		t.Fatal("finalization", active)
	}
	first, err := f.store.ValidateAccess(testContext, active.Access)
	if err != nil || first.Stage != oauthstore.Active {
		t.Fatal(err)
	}
	if _, err = f.store.ValidateAccess(testContext, pending.Access); err == nil {
		t.Fatal("old provisional access remained live")
	}
	next := f.exchange(t, refreshExchange(active.Refresh))
	second, err := f.store.ValidateAccess(testContext, next.Access)
	if err != nil || second.ID == first.ID {
		t.Fatal("JWT jti reused", err)
	}
	if w := f.request("/oauth2/token", refreshExchange(active.Refresh), nil); w.Code != 400 {
		t.Fatal("refresh reused")
	}
	if _, err = f.store.ValidateAccess(testContext, next.Access); err == nil {
		t.Fatal("reuse revocation lost")
	}
	var leaked int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM oauth_sessions WHERE CAST(data AS TEXT) LIKE ?`, "%"+active.Refresh+"%").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("raw secret stored")
	}
}

func TestRecoveryProofLostACKAndCredentialRevocation(t *testing.T) {
	f := setup(t)
	key, j := installation(t)
	a := f.a
	a.KeyThumbprint = j.Thumbprint()
	pending := f.exchange(t, codeExchange(f.code(t, a), j))
	c, _ := f.store.ValidateAccess(testContext, pending.Access)
	device, err := f.store.FinishInstallation(testContext, c, pending.Challenge.OperationID, pending.Challenge.Nonce, proof(t, key, pending.Challenge), &j, "Pixel")
	if err != nil {
		t.Fatal(err)
	}
	old := f.exchange(t, refreshExchange(pending.Refresh))
	a.DeviceHint = device.ID
	recovery := f.exchange(t, codeExchange(f.code(t, a), j))
	if recovery.Stage != oauthstore.Recovery || recovery.Challenge == nil {
		t.Fatal("recovery", recovery)
	}
	provisional, err := f.store.ValidateAccess(testContext, recovery.Access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.FinishInstallation(testContext, provisional, recovery.Challenge.OperationID, recovery.Challenge.Nonce, proof(t, key, recovery.Challenge), nil, ""); err != nil {
		t.Fatal("recovery completion", err)
	}
	for _, token := range []string{old.Access, recovery.Access} {
		if _, err = f.store.ValidateAccess(testContext, token); err == nil {
			t.Fatal("recovery did not fence old access")
		}
	}
	// The completion ACK may be lost. Finalization needs only the unused
	// provisional refresh, not the now-revoked old access credential.
	active := f.exchange(t, refreshExchange(recovery.Refresh))
	if active.Stage != oauthstore.Active {
		t.Fatal("recovery finalization")
	}
	f.user.Disabled = true
	if err = f.users.UpdateUser(testContext, f.user); err != nil {
		t.Fatal(err)
	}
	if w := f.request("/oauth2/token", refreshExchange(active.Refresh), nil); w.Code != 400 {
		t.Fatal("disabled owner refreshed")
	}
	if _, err = f.store.ValidateAccess(testContext, active.Access); err == nil {
		t.Fatal("account epoch revocation not persistent")
	}
}
