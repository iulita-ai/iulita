package google

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/oauth2"
	googleoauth "golang.org/x/oauth2/google"
	googlecalendar "google.golang.org/api/calendar/v3"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"
	"google.golang.org/api/tasks/v1"

	"github.com/iulita-ai/iulita/internal/domain"
)

// TokenStore provides encrypted token persistence.
type TokenStore interface {
	GetGoogleAccount(ctx context.Context, id int64) (*domain.GoogleAccount, error)
	GetGoogleAccountByEmail(ctx context.Context, userID, email string) (*domain.GoogleAccount, error)
	GetDefaultGoogleAccount(ctx context.Context, userID string) (*domain.GoogleAccount, error)
	ListGoogleAccounts(ctx context.Context, userID string) ([]domain.GoogleAccount, error)
	UpdateGoogleTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiry time.Time) error
}

// CryptoProvider encrypts/decrypts tokens at rest.
type CryptoProvider interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
	EncryptionEnabled() bool
}

// ClientOptions configures a Google API client.
type ClientOptions struct {
	ClientID        string
	ClientSecret    string
	RedirectURL     string
	Store           TokenStore
	Crypto          CryptoProvider
	Logger          *zap.Logger
	CredentialsFile string   // Path to service_account or authorized_user JSON
	Scopes          []string // Custom scopes (nil = use defaults)
}

// Client manages OAuth2 credentials and Google API service creation.
type Client struct {
	oauthConfig    *oauth2.Config
	store          TokenStore
	crypto         CryptoProvider
	logger         *zap.Logger
	configFilePath string   // credentials file from config
	configScopes   []string // resolved scopes
	mu             sync.Mutex
}

// NewClientWithOptions creates a Google API client with full options.
func NewClientWithOptions(opts ClientOptions) *Client {
	scopes := opts.Scopes
	if len(scopes) == 0 {
		scopes = DefaultScopes()
	}
	return &Client{
		oauthConfig: &oauth2.Config{
			ClientID:     opts.ClientID,
			ClientSecret: opts.ClientSecret,
			RedirectURL:  opts.RedirectURL,
			Scopes:       scopes,
			Endpoint:     googleoauth.Endpoint,
		},
		store:          opts.Store,
		crypto:         opts.Crypto,
		logger:         opts.Logger,
		configFilePath: opts.CredentialsFile,
		configScopes:   scopes,
	}
}

// NewClient creates a Google API client (backward-compatible wrapper).
func NewClient(clientID, clientSecret, redirectURL string, store TokenStore, crypto CryptoProvider, logger *zap.Logger) *Client {
	return NewClientWithOptions(ClientOptions{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Store:        store,
		Crypto:       crypto,
		Logger:       logger,
	})
}

// UpdateOAuthConfig updates OAuth2 credentials at runtime (hot-reload).
func (c *Client) UpdateOAuthConfig(clientID, clientSecret, redirectURL string) {
	c.mu.Lock()
	c.oauthConfig.ClientID = clientID
	c.oauthConfig.ClientSecret = clientSecret
	if redirectURL != "" {
		c.oauthConfig.RedirectURL = redirectURL
	}
	c.mu.Unlock()
}

// AuthCodeURL returns the URL to redirect the user to for OAuth2 consent.
func (c *Client) AuthCodeURL(state string) string {
	return c.oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
}

// ExchangeCode exchanges an authorization code for tokens.
func (c *Client) ExchangeCode(ctx context.Context, code string) (*oauth2.Token, error) {
	token, err := c.oauthConfig.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchanging auth code: %w", err)
	}
	return token, nil
}

// ExchangeCodeRaw exchanges an authorization code and returns raw token values.
// This satisfies the dashboard.GoogleOAuthClient interface without exposing oauth2.Token.
func (c *Client) ExchangeCodeRaw(ctx context.Context, code string) (accessToken, refreshToken string, expiry time.Time, err error) {
	token, err := c.ExchangeCode(ctx, code)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return token.AccessToken, token.RefreshToken, token.Expiry, nil
}

// EncryptToken encrypts a token value for storage.
func (c *Client) EncryptToken(value string) (string, error) {
	if c.crypto != nil && c.crypto.EncryptionEnabled() {
		return c.crypto.Encrypt(value)
	}
	return value, nil
}

// DecryptToken decrypts a stored token value.
func (c *Client) DecryptToken(value string) (string, error) {
	if c.crypto != nil && c.crypto.EncryptionEnabled() {
		return c.crypto.Decrypt(value)
	}
	return value, nil
}

// resolveAccount finds the account by alias or returns the default.
func (c *Client) resolveAccount(ctx context.Context, userID, accountAlias string) (*domain.GoogleAccount, error) {
	if accountAlias != "" {
		// Try alias match.
		accounts, err := c.store.ListGoogleAccounts(ctx, userID)
		if err != nil {
			return nil, err
		}
		for _, a := range accounts {
			if a.AccountAlias == accountAlias || a.AccountEmail == accountAlias {
				return &a, nil
			}
		}
		return nil, fmt.Errorf("google account %q not found", accountAlias)
	}
	return c.store.GetDefaultGoogleAccount(ctx, userID)
}

// tokenSource creates an oauth2.TokenSource for a stored account, persisting refreshed tokens.
func (c *Client) tokenSource(ctx context.Context, account *domain.GoogleAccount) (oauth2.TokenSource, error) {
	accessToken, err := c.DecryptToken(account.EncryptedAccessToken)
	if err != nil {
		return nil, fmt.Errorf("decrypting access token: %w", err)
	}
	refreshToken, err := c.DecryptToken(account.EncryptedRefreshToken)
	if err != nil {
		return nil, fmt.Errorf("decrypting refresh token: %w", err)
	}

	token := &oauth2.Token{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		Expiry:       account.TokenExpiry,
	}

	baseSource := c.oauthConfig.TokenSource(ctx, token)
	return &persistingTokenSource{
		base:    baseSource,
		account: account,
		client:  c,
		ctx:     ctx,
	}, nil
}

// persistingTokenSource wraps an oauth2.TokenSource to persist refreshed tokens.
type persistingTokenSource struct {
	base    oauth2.TokenSource
	account *domain.GoogleAccount
	client  *Client
	ctx     context.Context
	mu      sync.Mutex
}

func (s *persistingTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	token, err := s.base.Token()
	if err != nil {
		return nil, err
	}

	// If token was refreshed, persist to DB.
	if token.Expiry != s.account.TokenExpiry {
		encAccess, err := s.client.EncryptToken(token.AccessToken)
		if err != nil {
			s.client.logger.Warn("failed to encrypt refreshed access token", zap.Error(err))
			return token, nil
		}
		encRefresh := s.account.EncryptedRefreshToken
		if token.RefreshToken != "" {
			if enc, err := s.client.EncryptToken(token.RefreshToken); err == nil {
				encRefresh = enc
			}
		}
		if err := s.client.store.UpdateGoogleTokens(s.ctx, s.account.ID, encAccess, encRefresh, token.Expiry); err != nil {
			s.client.logger.Warn("failed to persist refreshed google tokens", zap.Error(err))
		}
		s.account.TokenExpiry = token.Expiry
	}

	return token, nil
}

// resolveTokenSource walks the full credential priority chain:
// env token → env file → config file → DB account → ADC.
func (c *Client) resolveTokenSource(ctx context.Context, userID, accountAlias string) (oauth2.TokenSource, error) {
	// Try global credential sources first (steps 1-3 and 5).
	result, err := ResolveCredentials(
		ctx, userID, accountAlias,
		c.oauthConfig, c.store, c.crypto,
		c.configFilePath, c.configScopes, c.logger,
	)
	if err != nil {
		return nil, err
	}
	// Non-nil result means a global source was found.
	if result != nil {
		return result.TokenSrc, nil
	}

	// nil result + nil error means "try DB path" (step 4).
	account, err := c.resolveAccount(ctx, userID, accountAlias)
	if err == nil {
		return c.tokenSource(ctx, account)
	}

	// DB lookup failed (no accounts) — fall through to ADC (step 5).
	c.logger.Debug("no DB google account, trying ADC fallback", zap.Error(err))
	ts, adcErr := tokenSourceFromADC(ctx, c.configScopes)
	if adcErr == nil {
		c.logger.Debug("using google Application Default Credentials (after DB miss)")
		return ts, nil
	}

	// Return the original DB error — it's more actionable than "no ADC found".
	return nil, err
}

// GetGmailService creates a Gmail API service for the given user and account.
func (c *Client) GetGmailService(ctx context.Context, userID, accountAlias string) (*gmail.Service, error) {
	ts, err := c.resolveTokenSource(ctx, userID, accountAlias)
	if err != nil {
		return nil, err
	}
	return gmail.NewService(ctx, option.WithTokenSource(ts))
}

// GetCalendarService creates a Calendar API service.
func (c *Client) GetCalendarService(ctx context.Context, userID, accountAlias string) (*googlecalendar.Service, error) {
	ts, err := c.resolveTokenSource(ctx, userID, accountAlias)
	if err != nil {
		return nil, err
	}
	return googlecalendar.NewService(ctx, option.WithTokenSource(ts))
}

// GetPeopleService creates a People API service.
func (c *Client) GetPeopleService(ctx context.Context, userID, accountAlias string) (*people.Service, error) {
	ts, err := c.resolveTokenSource(ctx, userID, accountAlias)
	if err != nil {
		return nil, err
	}
	return people.NewService(ctx, option.WithTokenSource(ts))
}

// GetTasksService creates a Tasks API service.
func (c *Client) GetTasksService(ctx context.Context, userID, accountAlias string) (*tasks.Service, error) {
	ts, err := c.resolveTokenSource(ctx, userID, accountAlias)
	if err != nil {
		return nil, err
	}
	return tasks.NewService(ctx, option.WithTokenSource(ts))
}

// HasAccounts checks if a user has any connected Google accounts.
func (c *Client) HasAccounts(ctx context.Context, userID string) bool {
	accounts, err := c.store.ListGoogleAccounts(ctx, userID)
	return err == nil && len(accounts) > 0
}

// ListAccounts returns connected accounts for display.
func (c *Client) ListAccounts(ctx context.Context, userID string) ([]domain.GoogleAccount, error) {
	return c.store.ListGoogleAccounts(ctx, userID)
}

// AccountInfo returns a formatted list of connected accounts.
func (c *Client) AccountInfo(ctx context.Context, userID string) (string, error) {
	accounts, err := c.store.ListGoogleAccounts(ctx, userID)
	if err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "No Google accounts connected. Connect one in Settings.", nil
	}
	result := fmt.Sprintf("%d Google account(s) connected:\n", len(accounts))
	for _, a := range accounts {
		def := ""
		if a.IsDefault {
			def = " (default)"
		}
		alias := a.AccountAlias
		if alias == "" {
			alias = "-"
		}
		result += fmt.Sprintf("- %s [%s]%s\n", a.AccountEmail, alias, def)
	}
	return result, nil
}

// GetUserTimezone returns the user timezone from profile, falling back to UTC.
func GetUserTimezone(ctx context.Context, store interface {
	GetUser(context.Context, string) (*domain.User, error)
}, userID string) string {
	if userID == "" || store == nil {
		return "UTC"
	}
	u, err := store.GetUser(ctx, userID)
	if err != nil || u.Timezone == "" {
		return "UTC"
	}
	return u.Timezone
}

// GetConfigScopes returns the currently configured scopes.
func (c *Client) GetConfigScopes() []string {
	return c.configScopes
}

// GetCredentialsFile returns the configured credentials file path.
func (c *Client) GetCredentialsFile() string {
	return c.configFilePath
}

// SetCredentialsFile updates the credentials file path at runtime.
func (c *Client) SetCredentialsFile(path string) {
	c.mu.Lock()
	c.configFilePath = path
	c.mu.Unlock()
}

// SetScopes updates the scopes at runtime and refreshes the OAuth config.
func (c *Client) SetScopes(scopes []string) {
	c.mu.Lock()
	c.configScopes = scopes
	c.oauthConfig.Scopes = scopes
	c.mu.Unlock()
}

// GetStatus returns diagnostic credential status.
func (c *Client) GetStatus(ctx context.Context, userID string) CredentialStatus {
	return GetCredentialStatus(ctx, userID, c.store, c.configFilePath, c.configScopes)
}

// GetCredentialStatus returns credential status as a generic map (for dashboard interface).
func (c *Client) GetCredentialStatus(ctx context.Context, userID string) map[string]any {
	status := c.GetStatus(ctx, userID)
	return map[string]any{
		"source":          status.Source,
		"credential_type": status.CredentialType,
		"file_path":       status.FilePath,
		"db_accounts":     status.DBAccounts,
		"active_scopes":   status.ActiveScopes,
	}
}

// UploadCredentials validates and saves a credentials file, then applies it.
// Returns the credential type and destination path on success.
func (c *Client) UploadCredentials(data []byte, filename, dataDir string) (string, string, error) {
	credType, err := detectCredentialType(data)
	if err != nil {
		return "", "", fmt.Errorf("file %q is not a valid Google credentials JSON: %w", filename, err)
	}

	if dataDir == "" {
		return "", "", fmt.Errorf("data directory not configured")
	}

	credDir := filepath.Join(dataDir, "google")
	if err := os.MkdirAll(credDir, 0700); err != nil {
		return "", "", fmt.Errorf("creating credentials directory: %w", err)
	}

	destPath := filepath.Join(credDir, "credentials.json")
	if err := os.WriteFile(destPath, data, 0600); err != nil {
		return "", "", fmt.Errorf("saving credentials file: %w", err)
	}

	c.SetCredentialsFile(destPath)
	return credType, destPath, nil
}

// ParseScopesJSON parses a JSON array of scopes.
func ParseScopesJSON(s string) []string {
	var scopes []string
	json.Unmarshal([]byte(s), &scopes)
	return scopes
}
