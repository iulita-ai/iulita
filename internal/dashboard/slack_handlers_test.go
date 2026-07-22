package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	slackskill "github.com/iulita-ai/iulita/internal/skill/slack"
	"github.com/iulita-ai/iulita/internal/storage"
)

// --- fakes ---

type fakeSlackStore struct {
	storage.Repository
	users   map[string]*domain.User
	account *domain.SlackAccount
	saved   *domain.SlackAccount
	updated bool
	deleted bool
}

func (f *fakeSlackStore) GetUser(_ context.Context, id string) (*domain.User, error) {
	return f.users[id], nil
}
func (f *fakeSlackStore) GetSlackAccountByUserID(_ context.Context, _ string) (*domain.SlackAccount, error) {
	return f.account, nil
}
func (f *fakeSlackStore) GetAnySlackAccount(_ context.Context) (*domain.SlackAccount, error) {
	return f.account, nil
}
func (f *fakeSlackStore) SaveSlackAccount(_ context.Context, a *domain.SlackAccount) error {
	f.saved = a
	return nil
}
func (f *fakeSlackStore) UpdateSlackTokens(_ context.Context, _ int64, _, _ string, _ time.Time) error {
	f.updated = true
	return nil
}
func (f *fakeSlackStore) DeleteSlackAccount(_ context.Context, _ string) error {
	f.deleted = true
	return nil
}

type fakeSlackOAuth struct {
	notConfigured bool
	encEnabled    bool
	exchange      *slackskill.ExchangeResult
	exchangeErr   error
}

func (f *fakeSlackOAuth) Configured() bool { return !f.notConfigured }
func (f *fakeSlackOAuth) NewSignedState(userID string) (string, error) {
	return "nonce:" + userID + ":sig", nil
}
func (f *fakeSlackOAuth) VerifyState(state string) (string, bool) {
	parts := strings.Split(state, ":")
	if len(parts) != 3 || parts[2] != "sig" {
		return "", false
	}
	return parts[1], true
}
func (f *fakeSlackOAuth) RedirectURL() string { return "https://app.example/api/slack/callback" }
func (f *fakeSlackOAuth) AuthCodeURL(state string) string {
	return "https://slack.com/oauth/v2/authorize?state=" + state
}
func (f *fakeSlackOAuth) ExchangeCode(_ context.Context, _ string) (*slackskill.ExchangeResult, error) {
	return f.exchange, f.exchangeErr
}
func (f *fakeSlackOAuth) EncryptToken(v string) (string, error) { return "enc:" + v, nil }
func (f *fakeSlackOAuth) EncryptionEnabled() bool               { return f.encEnabled }

func newTestServer(store storage.Repository, oauth SlackOAuthClient) *Server {
	return &Server{store: store, slackClient: oauth, logger: zap.NewNop()}
}

func callbackReq(state, cookie string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/slack/callback?code=abc&state="+state, http.NoBody)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: slackStateCookie, Value: cookie})
	}
	return req
}

// --- callback tests ---

func adminStore() *fakeSlackStore {
	return &fakeSlackStore{users: map[string]*domain.User{"owner": {ID: "owner", Role: domain.RoleAdmin}}}
}

func TestSlackCallback_StateMismatch(t *testing.T) {
	s := newTestServer(&fakeSlackStore{}, &fakeSlackOAuth{encEnabled: true})
	app := fiber.New()
	app.Get("/api/slack/callback", s.handleSlackCallback)

	resp, _ := app.Test(callbackReq("nonce:owner:sig", "different-cookie"))
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("state mismatch: status = %d, want 400", resp.StatusCode)
	}
}

func TestSlackCallback_InvalidSignature(t *testing.T) {
	s := newTestServer(&fakeSlackStore{}, &fakeSlackOAuth{encEnabled: true})
	app := fiber.New()
	app.Get("/api/slack/callback", s.handleSlackCallback)

	// Cookie matches state (double-submit passes) but the signature is bad —
	// simulates a forced/forged state cookie. VerifyState must reject it.
	resp, _ := app.Test(callbackReq("nonce:owner:BADSIG", "nonce:owner:BADSIG"))
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("invalid signature: status = %d, want 400", resp.StatusCode)
	}
}

func TestSlackCallback_EncryptionDisabled(t *testing.T) {
	s := newTestServer(adminStore(), &fakeSlackOAuth{encEnabled: false})
	app := fiber.New()
	app.Get("/api/slack/callback", s.handleSlackCallback)

	resp, _ := app.Test(callbackReq("nonce:owner:sig", "nonce:owner:sig"))
	if resp.StatusCode != fiber.StatusPreconditionFailed {
		t.Fatalf("callback with encryption off: status = %d, want 412", resp.StatusCode)
	}
}

func TestSlackCallback_NonAdminOwner(t *testing.T) {
	store := &fakeSlackStore{users: map[string]*domain.User{
		"baduser": {ID: "baduser", Role: domain.RoleRegular},
	}}
	s := newTestServer(store, &fakeSlackOAuth{encEnabled: true})
	app := fiber.New()
	app.Get("/api/slack/callback", s.handleSlackCallback)

	resp, _ := app.Test(callbackReq("nonce:baduser:sig", "nonce:baduser:sig"))
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("non-admin owner: status = %d, want 403", resp.StatusCode)
	}
	if store.saved != nil {
		t.Error("must not persist an account for a non-admin owner")
	}
}

func TestSlackCallback_WriteScopeRejected(t *testing.T) {
	oauth := &fakeSlackOAuth{encEnabled: true, exchange: &slackskill.ExchangeResult{
		AccessToken: "utok-a", AuthedUserID: "U1", TeamID: "T1", Scope: "search:read,chat:write",
	}}
	store := adminStore()
	s := newTestServer(store, oauth)
	app := fiber.New()
	app.Get("/api/slack/callback", s.handleSlackCallback)

	resp, _ := app.Test(callbackReq("nonce:owner:sig", "nonce:owner:sig"))
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("write scope: status = %d, want 403", resp.StatusCode)
	}
	if store.saved != nil {
		t.Error("must not persist a token that carries a write scope")
	}
}

func TestSlackCallback_HappyPath(t *testing.T) {
	store := adminStore()
	oauth := &fakeSlackOAuth{encEnabled: true, exchange: &slackskill.ExchangeResult{
		AccessToken: "utok-a", RefreshToken: "rtok-1", AuthedUserID: "U1",
		TeamID: "T1", TeamName: "Acme", Scope: "search:read,channels:history",
	}}
	s := newTestServer(store, oauth)
	app := fiber.New()
	app.Get("/api/slack/callback", s.handleSlackCallback)

	resp, _ := app.Test(callbackReq("nonce:owner:sig", "nonce:owner:sig"))
	if resp.StatusCode != fiber.StatusFound {
		t.Fatalf("happy path: status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/settings?slack=connected" {
		t.Errorf("redirect = %q", loc)
	}
	if store.saved == nil {
		t.Fatal("expected account persisted")
	}
	if store.saved.EncryptedAccessToken != "enc:utok-a" || store.saved.EncryptedRefreshToken != "enc:rtok-1" {
		t.Errorf("tokens not encrypted before save: %+v", store.saved)
	}
	if store.saved.TeamID != "T1" || store.saved.SlackUserID != "U1" || store.saved.UserID != "owner" {
		t.Errorf("account fields wrong: %+v", store.saved)
	}
}

func TestSlackCallback_CrossUserConflict(t *testing.T) {
	// An account owned by a DIFFERENT user already exists → single-owner reject.
	store := adminStore()
	store.account = &domain.SlackAccount{UserID: "someone-else", TeamID: "T1"}
	oauth := &fakeSlackOAuth{encEnabled: true, exchange: &slackskill.ExchangeResult{
		AccessToken: "utok-a", AuthedUserID: "U1", TeamID: "T1", Scope: "search:read",
	}}
	s := newTestServer(store, oauth)
	app := fiber.New()
	app.Get("/api/slack/callback", s.handleSlackCallback)

	resp, _ := app.Test(callbackReq("nonce:owner:sig", "nonce:owner:sig"))
	if resp.StatusCode != fiber.StatusConflict {
		t.Fatalf("cross-user conflict: status = %d, want 409", resp.StatusCode)
	}
}

func TestSlackCallback_WorkspaceConflict(t *testing.T) {
	store := adminStore()
	store.account = &domain.SlackAccount{UserID: "owner", TeamID: "T-old"}
	oauth := &fakeSlackOAuth{encEnabled: true, exchange: &slackskill.ExchangeResult{
		AccessToken: "utok-a", AuthedUserID: "U1", TeamID: "T-new", Scope: "search:read",
	}}
	s := newTestServer(store, oauth)
	app := fiber.New()
	app.Get("/api/slack/callback", s.handleSlackCallback)

	resp, _ := app.Test(callbackReq("nonce:owner:sig", "nonce:owner:sig"))
	if resp.StatusCode != fiber.StatusConflict {
		t.Fatalf("workspace conflict: status = %d, want 409", resp.StatusCode)
	}
}

func TestSlackCallback_Denied(t *testing.T) {
	s := newTestServer(&fakeSlackStore{}, &fakeSlackOAuth{})
	app := fiber.New()
	app.Get("/api/slack/callback", s.handleSlackCallback)

	req := httptest.NewRequest(http.MethodGet, "/api/slack/callback?error=access_denied", http.NoBody)
	resp, _ := app.Test(req)
	if resp.StatusCode != fiber.StatusFound {
		t.Fatalf("denied: status = %d, want 302 redirect", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/settings?slack=denied" {
		t.Errorf("redirect = %q", loc)
	}
}

// --- auth (start) tests ---

func injectAdmin(userID string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Locals(auth.ContextKeyUser, &auth.Claims{UserID: userID, Role: domain.RoleAdmin})
		return c.Next()
	}
}

func TestSlackAuth_NotConfigured(t *testing.T) {
	s := newTestServer(&fakeSlackStore{}, &fakeSlackOAuth{notConfigured: true, encEnabled: true})
	app := fiber.New()
	app.Get("/api/slack/auth", injectAdmin("owner"), s.handleSlackAuth)

	resp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/api/slack/auth", http.NoBody))
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("unconfigured: status = %d, want 503", resp.StatusCode)
	}
}

func TestSlackAuth_EncryptionDisabled(t *testing.T) {
	s := newTestServer(&fakeSlackStore{}, &fakeSlackOAuth{encEnabled: false})
	app := fiber.New()
	app.Get("/api/slack/auth", injectAdmin("owner"), s.handleSlackAuth)

	resp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/api/slack/auth", http.NoBody))
	if resp.StatusCode != fiber.StatusPreconditionFailed {
		t.Fatalf("encryption disabled: status = %d, want 412", resp.StatusCode)
	}
}

func TestSlackAuth_Success(t *testing.T) {
	s := newTestServer(&fakeSlackStore{}, &fakeSlackOAuth{encEnabled: true})
	app := fiber.New()
	app.Get("/api/slack/auth", injectAdmin("owner"), s.handleSlackAuth)

	resp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/api/slack/auth", http.NoBody))
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("auth start: status = %d, want 200", resp.StatusCode)
	}
	// A state cookie must be set so the callback can verify it.
	if len(resp.Cookies()) == 0 {
		t.Error("expected a state cookie to be set")
	}
}
