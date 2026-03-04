package google

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
)

// mockCrypto implements CryptoProvider for testing.
type mockCrypto struct {
	enabled bool
}

func (m *mockCrypto) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}

func (m *mockCrypto) Decrypt(ciphertext string) (string, error) {
	if len(ciphertext) > 4 && ciphertext[:4] == "enc:" {
		return ciphertext[4:], nil
	}
	return ciphertext, nil
}

func (m *mockCrypto) EncryptionEnabled() bool {
	return m.enabled
}

// mockTokenStore implements TokenStore for testing.
type mockTokenStore struct {
	accounts []domain.GoogleAccount
}

func (m *mockTokenStore) GetGoogleAccount(_ context.Context, id int64) (*domain.GoogleAccount, error) {
	for _, a := range m.accounts {
		if a.ID == id {
			return &a, nil
		}
	}
	return nil, context.DeadlineExceeded
}

func (m *mockTokenStore) GetGoogleAccountByEmail(_ context.Context, userID, email string) (*domain.GoogleAccount, error) {
	for _, a := range m.accounts {
		if a.UserID == userID && a.AccountEmail == email {
			return &a, nil
		}
	}
	return nil, context.DeadlineExceeded
}

func (m *mockTokenStore) GetDefaultGoogleAccount(_ context.Context, userID string) (*domain.GoogleAccount, error) {
	for _, a := range m.accounts {
		if a.UserID == userID && a.IsDefault {
			return &a, nil
		}
	}
	return nil, context.DeadlineExceeded
}

func (m *mockTokenStore) ListGoogleAccounts(_ context.Context, userID string) ([]domain.GoogleAccount, error) {
	var result []domain.GoogleAccount
	for _, a := range m.accounts {
		if a.UserID == userID {
			result = append(result, a)
		}
	}
	return result, nil
}

func (m *mockTokenStore) UpdateGoogleTokens(_ context.Context, id int64, accessToken, refreshToken string, expiry time.Time) error {
	for i, a := range m.accounts {
		if a.ID == id {
			m.accounts[i].EncryptedAccessToken = accessToken
			m.accounts[i].EncryptedRefreshToken = refreshToken
			m.accounts[i].TokenExpiry = expiry
		}
	}
	return nil
}

func TestClient_EncryptToken_Enabled(t *testing.T) {
	c := &Client{crypto: &mockCrypto{enabled: true}}

	encrypted, err := c.EncryptToken("my-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if encrypted != "enc:my-token" {
		t.Errorf("expected enc:my-token, got %s", encrypted)
	}
}

func TestClient_EncryptToken_Disabled(t *testing.T) {
	c := &Client{crypto: &mockCrypto{enabled: false}}

	encrypted, err := c.EncryptToken("my-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if encrypted != "my-token" {
		t.Errorf("expected my-token, got %s", encrypted)
	}
}

func TestClient_EncryptToken_NilCrypto(t *testing.T) {
	c := &Client{crypto: nil}

	encrypted, err := c.EncryptToken("my-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if encrypted != "my-token" {
		t.Errorf("expected my-token, got %s", encrypted)
	}
}

func TestClient_DecryptToken(t *testing.T) {
	c := &Client{crypto: &mockCrypto{enabled: true}}

	decrypted, err := c.DecryptToken("enc:my-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decrypted != "my-token" {
		t.Errorf("expected my-token, got %s", decrypted)
	}
}

func TestClient_HasAccounts(t *testing.T) {
	store := &mockTokenStore{
		accounts: []domain.GoogleAccount{
			{ID: 1, UserID: "user1", AccountEmail: "a@b.com", IsDefault: true},
		},
	}
	c := &Client{store: store}

	if !c.HasAccounts(context.Background(), "user1") {
		t.Error("expected HasAccounts to return true for user1")
	}
	if c.HasAccounts(context.Background(), "user2") {
		t.Error("expected HasAccounts to return false for user2")
	}
}

func TestClient_ResolveAccount_Default(t *testing.T) {
	store := &mockTokenStore{
		accounts: []domain.GoogleAccount{
			{ID: 1, UserID: "u1", AccountEmail: "default@g.com", IsDefault: true},
			{ID: 2, UserID: "u1", AccountEmail: "other@g.com", AccountAlias: "work"},
		},
	}
	c := &Client{store: store}

	acc, err := c.resolveAccount(context.Background(), "u1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.AccountEmail != "default@g.com" {
		t.Errorf("expected default@g.com, got %s", acc.AccountEmail)
	}
}

func TestClient_ResolveAccount_ByAlias(t *testing.T) {
	store := &mockTokenStore{
		accounts: []domain.GoogleAccount{
			{ID: 1, UserID: "u1", AccountEmail: "default@g.com", IsDefault: true},
			{ID: 2, UserID: "u1", AccountEmail: "work@g.com", AccountAlias: "work"},
		},
	}
	c := &Client{store: store}

	acc, err := c.resolveAccount(context.Background(), "u1", "work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.AccountEmail != "work@g.com" {
		t.Errorf("expected work@g.com, got %s", acc.AccountEmail)
	}
}

func TestClient_ResolveAccount_ByEmail(t *testing.T) {
	store := &mockTokenStore{
		accounts: []domain.GoogleAccount{
			{ID: 1, UserID: "u1", AccountEmail: "default@g.com", IsDefault: true},
			{ID: 2, UserID: "u1", AccountEmail: "work@g.com", AccountAlias: "work"},
		},
	}
	c := &Client{store: store}

	acc, err := c.resolveAccount(context.Background(), "u1", "work@g.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.AccountEmail != "work@g.com" {
		t.Errorf("expected work@g.com, got %s", acc.AccountEmail)
	}
}

func TestClient_ResolveAccount_NotFound(t *testing.T) {
	store := &mockTokenStore{
		accounts: []domain.GoogleAccount{
			{ID: 1, UserID: "u1", AccountEmail: "default@g.com", IsDefault: true},
		},
	}
	c := &Client{store: store}

	_, err := c.resolveAccount(context.Background(), "u1", "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent alias")
	}
}

func TestClient_AccountInfo(t *testing.T) {
	store := &mockTokenStore{
		accounts: []domain.GoogleAccount{
			{ID: 1, UserID: "u1", AccountEmail: "a@g.com", AccountAlias: "personal", IsDefault: true},
			{ID: 2, UserID: "u1", AccountEmail: "b@g.com"},
		},
	}
	c := &Client{store: store}

	info, err := c.AccountInfo(context.Background(), "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == "" {
		t.Error("expected non-empty account info")
	}
	if !contains(info, "a@g.com") || !contains(info, "(default)") {
		t.Errorf("expected account info to contain email and default marker, got: %s", info)
	}
}

func TestClient_AccountInfo_NoAccounts(t *testing.T) {
	store := &mockTokenStore{}
	c := &Client{store: store}

	info, err := c.AccountInfo(context.Background(), "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(info, "No Google accounts") {
		t.Errorf("expected 'No Google accounts' message, got: %s", info)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestNewClientWithOptions_DefaultScopes(t *testing.T) {
	c := NewClientWithOptions(ClientOptions{
		Logger: zap.NewNop(),
	})
	if len(c.configScopes) == 0 {
		t.Fatal("expected default scopes when none provided")
	}
	if c.configScopes[0] != DefaultScopes()[0] {
		t.Errorf("expected first default scope, got %s", c.configScopes[0])
	}
}

func TestNewClientWithOptions_CustomScopes(t *testing.T) {
	custom := []string{"https://mail.google.com/", "https://www.googleapis.com/auth/drive"}
	c := NewClientWithOptions(ClientOptions{
		Scopes: custom,
		Logger: zap.NewNop(),
	})
	if len(c.configScopes) != 2 {
		t.Fatalf("expected 2 scopes, got %d", len(c.configScopes))
	}
	if c.oauthConfig.Scopes[0] != custom[0] {
		t.Errorf("oauth config scopes mismatch: %v", c.oauthConfig.Scopes)
	}
}

func TestNewClientWithOptions_CredentialsFile(t *testing.T) {
	c := NewClientWithOptions(ClientOptions{
		CredentialsFile: "/path/to/creds.json",
		Logger:          zap.NewNop(),
	})
	if c.configFilePath != "/path/to/creds.json" {
		t.Errorf("expected /path/to/creds.json, got %s", c.configFilePath)
	}
}

func TestClient_SetCredentialsFile(t *testing.T) {
	c := NewClientWithOptions(ClientOptions{Logger: zap.NewNop()})
	c.SetCredentialsFile("/new/path.json")
	if c.GetCredentialsFile() != "/new/path.json" {
		t.Errorf("expected /new/path.json, got %s", c.GetCredentialsFile())
	}
}

func TestClient_SetScopes(t *testing.T) {
	c := NewClientWithOptions(ClientOptions{Logger: zap.NewNop()})
	newScopes := []string{"https://mail.google.com/"}
	c.SetScopes(newScopes)
	if len(c.GetConfigScopes()) != 1 {
		t.Fatalf("expected 1 scope, got %d", len(c.GetConfigScopes()))
	}
	if c.oauthConfig.Scopes[0] != "https://mail.google.com/" {
		t.Errorf("oauth config not updated: %v", c.oauthConfig.Scopes)
	}
}

func TestClient_ResolveTokenSource_EnvToken(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "env-tok-123")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", "")

	c := NewClientWithOptions(ClientOptions{Logger: zap.NewNop()})
	ts, err := c.resolveTokenSource(context.Background(), "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("token error: %v", err)
	}
	if tok.AccessToken != "env-tok-123" {
		t.Errorf("expected env-tok-123, got %s", tok.AccessToken)
	}
}

func TestClient_ResolveTokenSource_FallsThroughToDB(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", "")

	store := &mockTokenStore{
		accounts: []domain.GoogleAccount{
			{ID: 1, UserID: "u1", AccountEmail: "a@g.com", IsDefault: true,
				EncryptedAccessToken: "access", EncryptedRefreshToken: "refresh"},
		},
	}
	c := NewClientWithOptions(ClientOptions{
		Store:  store,
		Logger: zap.NewNop(),
	})
	// resolveTokenSource should fall through to DB path (steps 1-3 skip, step 4 returns nil,nil)
	// then resolveAccount + tokenSource
	ts, err := c.resolveTokenSource(context.Background(), "u1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts == nil {
		t.Fatal("expected non-nil token source from DB fallback")
	}
}

func TestClient_GetStatus(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "tok")
	c := NewClientWithOptions(ClientOptions{Logger: zap.NewNop()})
	status := c.GetStatus(context.Background(), "")
	if status.Source != "env_token" {
		t.Errorf("expected env_token, got %s", status.Source)
	}
}
