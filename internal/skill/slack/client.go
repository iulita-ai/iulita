// Package slack implements the owner's personal Slack user-token (xoxp-) OAuth
// connection: authorize URL, code exchange, encrypted storage, and refresh.
// It is separate from internal/channel/slack, which is the bot-token channel.
package slack

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
)

// ErrNoSlackAccount is returned when the owner has not connected a Slack account.
var ErrNoSlackAccount = errors.New("no slack account connected")

// TokenStore persists and refreshes the owner's Slack account tokens.
type TokenStore interface {
	GetSlackAccountByUserID(ctx context.Context, userID string) (*domain.SlackAccount, error)
	UpdateSlackTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiry time.Time) error
}

// CryptoProvider encrypts/decrypts tokens at rest.
type CryptoProvider interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
	EncryptionEnabled() bool
}

// ExchangeResult is the outcome of an OAuth code exchange, decoupled from the
// slack-go response type so it doesn't leak into the dashboard layer.
type ExchangeResult struct {
	AccessToken  string
	RefreshToken string
	AuthedUserID string
	TeamID       string
	TeamName     string
	Scope        string
	Expiry       time.Time // zero when the token does not expire (rotation off)
}

// ClientOptions configures the Slack OAuth client.
type ClientOptions struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Store        TokenStore
	Crypto       CryptoProvider
	HTTPClient   *http.Client
	Logger       *zap.Logger
}

// Client manages the owner's Slack personal OAuth credentials.
type Client struct {
	clientID     string
	clientSecret string
	redirectURL  string
	store        TokenStore
	crypto       CryptoProvider
	http         *http.Client
	logger       *zap.Logger
	stateKey     []byte // HMAC key for signing OAuth state (per-process; single replica)
	mu           sync.Mutex

	// exchangeFn/refreshFn wrap the slack-go OAuth calls so tests can inject
	// canned responses (slack-go's APIURL is a const and cannot be redirected).
	exchangeFn func(ctx context.Context, code string) (*slackapi.OAuthV2Response, error)
	refreshFn  func(ctx context.Context, refreshToken string) (*slackapi.OAuthV2Response, error)
}

// NewClient creates a Slack OAuth client.
func NewClient(opts ClientOptions) *Client {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	stateKey := make([]byte, 32)
	if _, err := rand.Read(stateKey); err != nil {
		// crypto/rand failure is fatal for signing; fall back to a fixed key would
		// be insecure, so panic — this only happens if the OS RNG is broken.
		panic(fmt.Sprintf("slack oauth: generating state key: %v", err))
	}
	c := &Client{
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		redirectURL:  opts.RedirectURL,
		store:        opts.Store,
		crypto:       opts.Crypto,
		http:         httpClient,
		logger:       logger,
		stateKey:     stateKey,
	}
	c.exchangeFn = func(ctx context.Context, code string) (*slackapi.OAuthV2Response, error) {
		return slackapi.GetOAuthV2ResponseContext(ctx, c.http, c.clientID, c.clientSecret, code, c.redirectURL)
	}
	c.refreshFn = func(ctx context.Context, refreshToken string) (*slackapi.OAuthV2Response, error) {
		return slackapi.RefreshOAuthV2TokenContext(ctx, c.http, c.clientID, c.clientSecret, refreshToken)
	}
	return c
}

// Configured reports whether the OAuth app credentials are present.
func (c *Client) Configured() bool {
	return c.clientID != "" && c.clientSecret != ""
}

// RedirectURL returns the configured OAuth callback URL (for diagnostics).
func (c *Client) RedirectURL() string { return c.redirectURL }

// AuthCodeURL builds the Slack v2 authorize URL for a personal USER token.
// It requests user_scope (not bot scope), which is what yields an xoxp- token.
func (c *Client) AuthCodeURL(state string) string {
	v := url.Values{
		"client_id":    {c.clientID},
		"redirect_uri": {c.redirectURL},
		"user_scope":   {strings.Join(RequiredUserScopes(), ",")},
		"state":        {state},
	}
	return "https://slack.com/oauth/v2/authorize?" + v.Encode()
}

// NewSignedState returns an unforgeable OAuth state binding the initiating owner:
// "<nonce>:<userID>:<hmac>". The HMAC (keyed by a per-process secret) means a
// forced/planted state cookie cannot be crafted to pass VerifyState, closing
// OAuth code/account-injection via cookie forcing on top of the double-submit
// cookie check.
func (c *Client) NewSignedState(userID string) (string, error) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("generating state nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	payload := nonce + ":" + userID
	return payload + ":" + c.signState(payload), nil
}

// VerifyState validates a signed state and returns the embedded owner user id.
// The comparison is constant-time.
func (c *Client) VerifyState(state string) (string, bool) {
	parts := strings.Split(state, ":")
	if len(parts) != 3 {
		return "", false
	}
	payload := parts[0] + ":" + parts[1]
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(c.signState(payload))) != 1 {
		return "", false
	}
	if parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func (c *Client) signState(payload string) string {
	mac := hmac.New(sha256.New, c.stateKey)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// ExchangeCode exchanges an authorization code for the owner's user token.
func (c *Client) ExchangeCode(ctx context.Context, code string) (*ExchangeResult, error) {
	resp, err := c.exchangeFn(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchanging slack code: %w", err)
	}
	au := resp.AuthedUser
	if au.AccessToken == "" {
		return nil, fmt.Errorf("slack oauth response missing authed_user.access_token (did the app request user_scope?)")
	}
	var expiry time.Time
	if au.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(au.ExpiresIn) * time.Second)
	}
	return &ExchangeResult{
		AccessToken:  au.AccessToken,
		RefreshToken: au.RefreshToken,
		AuthedUserID: au.ID,
		TeamID:       resp.Team.ID,
		TeamName:     resp.Team.Name,
		Scope:        au.Scope,
		Expiry:       expiry,
	}, nil
}

// GetUserClient returns a slack-go client authenticated as the owner's user
// token (xoxp-), decrypting and refreshing via GetUserToken. The token carries
// only read scopes (see RequiredUserScopes / HasWriteScope), so this client is
// read-only in practice; callers must still never wire it to a write path.
// The ErrNoSlackAccount sentinel is preserved (via errors.Is) for a fail-closed
// "not connected" branch.
func (c *Client) GetUserClient(ctx context.Context, ownerUserID string) (*slackapi.Client, error) {
	token, err := c.GetUserToken(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	return slackapi.New(token, slackapi.OptionHTTPClient(c.http)), nil
}

// EncryptToken encrypts a token for storage; returns the value unchanged when no
// encryptor is configured. The dashboard start-guard refuses to begin the flow
// unless encryption is enabled, so plaintext storage never happens in practice.
func (c *Client) EncryptToken(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if c.crypto != nil && c.crypto.EncryptionEnabled() {
		return c.crypto.Encrypt(value)
	}
	return value, nil
}

// DecryptToken reverses EncryptToken.
func (c *Client) DecryptToken(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if c.crypto != nil && c.crypto.EncryptionEnabled() {
		return c.crypto.Decrypt(value)
	}
	return value, nil
}

// EncryptionEnabled reports whether token encryption is active.
func (c *Client) EncryptionEnabled() bool {
	return c.crypto != nil && c.crypto.EncryptionEnabled()
}

// GetUserToken returns a valid access token for the owner, refreshing and
// persisting if the stored token is expiring and a refresh token is present.
// A zero TokenExpiry means the token does not expire (rotation off) and is
// returned as-is — it must NOT be treated as already-expired.
func (c *Client) GetUserToken(ctx context.Context, ownerUserID string) (string, error) {
	// The lock is intentionally held across the network refresh + DB write so a
	// burst of callers cannot double-spend a rotating refresh token (Slack rejects
	// reuse). For a single-owner token this serialization is not a throughput
	// concern; do not narrow it.
	c.mu.Lock()
	defer c.mu.Unlock()

	account, err := c.store.GetSlackAccountByUserID(ctx, ownerUserID)
	if err != nil {
		return "", fmt.Errorf("loading slack account: %w", err)
	}
	if account == nil {
		return "", ErrNoSlackAccount
	}

	accessToken, err := c.DecryptToken(account.EncryptedAccessToken)
	if err != nil {
		return "", fmt.Errorf("decrypting slack access token: %w", err)
	}

	// Non-expiring token (rotation off) — use as-is.
	if account.TokenExpiry.IsZero() {
		return accessToken, nil
	}
	// Still valid (with a 1-minute skew) — use as-is.
	if time.Now().Before(account.TokenExpiry.Add(-1 * time.Minute)) {
		return accessToken, nil
	}
	// Expiring/expired and rotation is on — refresh.
	if account.EncryptedRefreshToken == "" {
		return "", fmt.Errorf("slack token expired and no refresh token stored (reconnect required)")
	}
	refreshToken, err := c.DecryptToken(account.EncryptedRefreshToken)
	if err != nil {
		return "", fmt.Errorf("decrypting slack refresh token: %w", err)
	}
	resp, err := c.refreshFn(ctx, refreshToken)
	if err != nil {
		return "", fmt.Errorf("refreshing slack token: %w", err)
	}
	// Validate before persisting: a blank access token, or a rotating token that
	// comes back without an expiry, must not silently clobber/downgrade the stored
	// credential and brick future refreshes.
	if resp.AccessToken == "" {
		return "", fmt.Errorf("slack refresh returned an empty access token")
	}
	if resp.ExpiresIn <= 0 {
		return "", fmt.Errorf("slack refresh returned no expiry for a rotating token")
	}
	newRefresh := resp.RefreshToken
	if newRefresh == "" {
		// Keep the existing refresh token rather than blanking it.
		newRefresh = refreshToken
	}
	newExpiry := time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	encAccess, err := c.EncryptToken(resp.AccessToken)
	if err != nil {
		return "", fmt.Errorf("encrypting refreshed access token: %w", err)
	}
	encRefresh, err := c.EncryptToken(newRefresh)
	if err != nil {
		return "", fmt.Errorf("encrypting refreshed refresh token: %w", err)
	}
	if err := c.store.UpdateSlackTokens(ctx, account.ID, encAccess, encRefresh, newExpiry); err != nil {
		return "", fmt.Errorf("persisting refreshed slack token: %w", err)
	}
	return resp.AccessToken, nil
}
