package slack

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"

	"github.com/iulita-ai/iulita/internal/domain"
)

// --- fakes ---

type tokenUpdate struct {
	id      int64
	access  string
	refresh string
	expiry  time.Time
}

type fakeStore struct {
	account *domain.SlackAccount
	updated *tokenUpdate
}

func (f *fakeStore) GetSlackAccountByUserID(_ context.Context, _ string) (*domain.SlackAccount, error) {
	return f.account, nil
}

func (f *fakeStore) UpdateSlackTokens(_ context.Context, id int64, access, refresh string, expiry time.Time) error {
	f.updated = &tokenUpdate{id, access, refresh, expiry}
	return nil
}

// reversingCrypto "encrypts" by prefixing so tests can assert round-trips.
type reversingCrypto struct{ enabled bool }

func (r reversingCrypto) Encrypt(s string) (string, error) { return "enc:" + s, nil }
func (r reversingCrypto) Decrypt(s string) (string, error) { return strings.TrimPrefix(s, "enc:"), nil }
func (r reversingCrypto) EncryptionEnabled() bool          { return r.enabled }

func newClient(store TokenStore, crypto CryptoProvider) *Client {
	return NewClient(ClientOptions{
		ClientID: "cid", ClientSecret: "secret",
		RedirectURL: "https://app.example/api/slack/callback",
		Store:       store, Crypto: crypto,
	})
}

// --- AuthCodeURL ---

func TestAuthCodeURL(t *testing.T) {
	c := newClient(nil, nil)
	got := c.AuthCodeURL("nonce:owner-1")
	if !strings.Contains(got, "user_scope=") {
		t.Errorf("expected user_scope in URL, got %q", got)
	}
	if strings.Contains(got, "&scope=") || strings.Contains(got, "?scope=") {
		t.Errorf("must NOT request bot scope, got %q", got)
	}
	if !strings.Contains(got, "search%3Aread") {
		t.Errorf("expected search:read scope, got %q", got)
	}
	if !strings.Contains(got, "state=nonce%3Aowner-1") {
		t.Errorf("expected state param, got %q", got)
	}
	if !strings.HasPrefix(got, "https://slack.com/oauth/v2/authorize?") {
		t.Errorf("unexpected authorize base: %q", got)
	}
}

// --- ExchangeCode ---

func TestExchangeCode(t *testing.T) {
	c := newClient(nil, nil)
	c.exchangeFn = func(_ context.Context, _ string) (*slackapi.OAuthV2Response, error) {
		return &slackapi.OAuthV2Response{
			AppID: "A1",
			Team:  slackapi.OAuthV2ResponseTeam{ID: "T1", Name: "Acme"},
			AuthedUser: slackapi.OAuthV2ResponseAuthedUser{
				ID: "U1", Scope: "search:read,channels:history",
				AccessToken: "utok-abc", TokenType: "user", RefreshToken: "rtok-1", ExpiresIn: 43200, // gitleaks:allow (fake test fixture)
			},
		}, nil
	}
	res, err := c.ExchangeCode(context.Background(), "code123")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if res.AccessToken != "utok-abc" || res.AuthedUserID != "U1" || res.TeamID != "T1" || res.TeamName != "Acme" {
		t.Errorf("unexpected result: %+v", res)
	}
	if res.RefreshToken != "rtok-1" {
		t.Errorf("refresh token = %q", res.RefreshToken)
	}
	if res.Expiry.IsZero() {
		t.Error("expected non-zero expiry when expires_in > 0")
	}
}

func TestExchangeCode_MissingUserToken(t *testing.T) {
	// Bot-only response (no authed_user.access_token) must be rejected.
	c := newClient(nil, nil)
	c.exchangeFn = func(_ context.Context, _ string) (*slackapi.OAuthV2Response, error) {
		return &slackapi.OAuthV2Response{
			AccessToken: "btok-bot", // gitleaks:allow (fake test fixture)
			Team:        slackapi.OAuthV2ResponseTeam{ID: "T1"},
			AuthedUser:  slackapi.OAuthV2ResponseAuthedUser{ID: "U1"},
		}, nil
	}
	if _, err := c.ExchangeCode(context.Background(), "code"); err == nil {
		t.Fatal("expected error for missing authed_user.access_token")
	}
}

// --- GetUserToken ---

func TestGetUserToken_NonExpiring(t *testing.T) {
	store := &fakeStore{account: &domain.SlackAccount{
		ID: 1, EncryptedAccessToken: "enc:utok-live", TokenExpiry: time.Time{}, // zero value: non-expiring // gitleaks:allow (fake test fixture)
	}}
	c := newClient(store, reversingCrypto{enabled: true})
	tok, err := c.GetUserToken(context.Background(), "owner")
	if err != nil {
		t.Fatalf("GetUserToken: %v", err)
	}
	if tok != "utok-live" {
		t.Errorf("token = %q, want utok-live", tok)
	}
	if store.updated != nil {
		t.Error("non-expiring token must not trigger a refresh/update")
	}
}

func TestGetUserToken_ValidNotYetExpired(t *testing.T) {
	store := &fakeStore{account: &domain.SlackAccount{
		ID: 1, EncryptedAccessToken: "enc:utok-live", TokenExpiry: time.Now().Add(time.Hour), // gitleaks:allow (fake test fixture)
	}}
	c := newClient(store, reversingCrypto{enabled: true})
	tok, err := c.GetUserToken(context.Background(), "owner")
	if err != nil || tok != "utok-live" {
		t.Fatalf("GetUserToken = (%q,%v)", tok, err)
	}
	if store.updated != nil {
		t.Error("valid token must not refresh")
	}
}

func TestGetUserToken_NoAccount(t *testing.T) {
	c := newClient(&fakeStore{account: nil}, reversingCrypto{enabled: true})
	if _, err := c.GetUserToken(context.Background(), "owner"); !errors.Is(err, ErrNoSlackAccount) {
		t.Fatalf("expected ErrNoSlackAccount, got %v", err)
	}
}

func TestGetUserToken_ExpiredNoRefresh(t *testing.T) {
	store := &fakeStore{account: &domain.SlackAccount{
		ID: 1, EncryptedAccessToken: "enc:utok-old", // gitleaks:allow (fake test fixture)
		TokenExpiry: time.Now().Add(-time.Hour), EncryptedRefreshToken: "",
	}}
	c := newClient(store, reversingCrypto{enabled: true})
	if _, err := c.GetUserToken(context.Background(), "owner"); err == nil {
		t.Fatal("expected error when expired with no refresh token")
	}
}

func TestGetUserToken_ExpiredRefreshes(t *testing.T) {
	store := &fakeStore{account: &domain.SlackAccount{
		ID: 7, EncryptedAccessToken: "enc:utok-old", // gitleaks:allow (fake test fixture)
		TokenExpiry: time.Now().Add(-time.Hour), EncryptedRefreshToken: "enc:rtok-1", // gitleaks:allow (fake test fixture)
	}}
	c := newClient(store, reversingCrypto{enabled: true})
	var gotRefreshToken string
	c.refreshFn = func(_ context.Context, refreshToken string) (*slackapi.OAuthV2Response, error) {
		gotRefreshToken = refreshToken
		return &slackapi.OAuthV2Response{AccessToken: "utok-new", RefreshToken: "rtok-2", ExpiresIn: 43200}, nil // gitleaks:allow (fake test fixture)
	}
	tok, err := c.GetUserToken(context.Background(), "owner")
	if err != nil {
		t.Fatalf("GetUserToken: %v", err)
	}
	if gotRefreshToken != "rtok-1" {
		t.Errorf("refresh called with decrypted token %q, want rtok-1", gotRefreshToken)
	}
	if tok != "utok-new" {
		t.Errorf("token = %q, want utok-new", tok)
	}
	if store.updated == nil {
		t.Fatal("expected refreshed tokens to be persisted")
	}
	if store.updated.access != "enc:utok-new" || store.updated.refresh != "enc:rtok-2" {
		t.Errorf("persisted tokens not encrypted correctly: %+v", store.updated)
	}
	if store.updated.expiry.IsZero() {
		t.Error("expected new expiry set from expires_in")
	}
}

func TestSignedState_RoundTrip(t *testing.T) {
	c := newClient(nil, nil)
	state, err := c.NewSignedState("owner-42")
	if err != nil {
		t.Fatalf("NewSignedState: %v", err)
	}
	uid, ok := c.VerifyState(state)
	if !ok || uid != "owner-42" {
		t.Fatalf("VerifyState = (%q,%v), want (owner-42,true)", uid, ok)
	}
}

func TestVerifyState_RejectsForgery(t *testing.T) {
	c := newClient(nil, nil)
	valid, _ := c.NewSignedState("owner")
	parts := strings.Split(valid, ":")

	cases := map[string]string{
		"tampered signature":  parts[0] + ":" + parts[1] + ":deadbeef",
		"tampered user":       parts[0] + ":attacker:" + parts[2],
		"forged from scratch": "nonce:attacker:00",
		"wrong format":        "nonce:attacker",
		"empty":               "",
	}
	for name, state := range cases {
		if _, ok := c.VerifyState(state); ok {
			t.Errorf("%s: VerifyState accepted a forged state %q", name, state)
		}
	}

	// A state signed by a DIFFERENT client (different key) must not verify.
	other := newClient(nil, nil)
	otherState, _ := other.NewSignedState("owner")
	if _, ok := c.VerifyState(otherState); ok {
		t.Error("state signed by another key must not verify")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := newClient(nil, reversingCrypto{enabled: true})
	enc, _ := c.EncryptToken("utok-secret")
	if enc != "enc:utok-secret" {
		t.Fatalf("encrypt = %q", enc)
	}
	dec, _ := c.DecryptToken(enc)
	if dec != "utok-secret" {
		t.Fatalf("decrypt = %q", dec)
	}
	// Empty stays empty (no encryptor call).
	if v, _ := c.EncryptToken(""); v != "" {
		t.Errorf("empty encrypt = %q", v)
	}
}
