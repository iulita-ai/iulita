package browseroauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	legacy "github.com/iulita-ai/iulita/internal/storage/sqlite"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"golang.org/x/crypto/bcrypt"
)

const testOrigin = "https://mobile.example.test"
const testRedirect = testOrigin + "/oauth/callback"
const testVerifier = "cryptographically-random-client-verifier-fixture-1234567890"

var testContext = context.Background()

type fixture struct {
	s     *Server
	store *oauthstore.Store
	users *legacy.Store
	db    *sql.DB
	user  *domain.User
	realm *oauthstore.Realm
	path  string
	a     oauthstore.Authorization
}

func setup(t *testing.T) *fixture {
	t.Helper()
	users, err := legacy.New(filepath.Join(t.TempDir(), "accounts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { users.Close() })
	if err = users.RunMigrations(testContext); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse fixture"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := &domain.User{ID: uuid.NewString(), Username: "owner", PasswordHash: string(hash), DisplayName: "<script>owner</script>"}
	if err = users.CreateUser(testContext, u); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "realm")
	if err = os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	realm, err := oauthstore.InitializeRealm(filepath.Join(dir, "keys"), testOrigin, "iulita-mobile")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "oauth.sqlite")
	store, err := oauthstore.Initialize(testContext, path, realm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, id := range []string{"mobile", "other"} {
		if err = store.RegisterClient(testContext, fosite.DefaultClient{ID: id, Public: true, RedirectURIs: []string{testRedirect}, GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scopes: []string{"chats:read", "chats:write"}, Audience: []string{"iulita-mobile"}}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{Store: store, Accounts: PasswordAccounts{users}, Scopes: []string{"chats:read", "chats:write"}, Limits: Limits{100, 100, 100, 100}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(testContext, path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	thumb, _ := oauthstore.Secret()
	sum := sha256.Sum256([]byte(testVerifier))
	a := oauthstore.Authorization{ClientID: "mobile", RedirectURI: testRedirect, Scope: "chats:read chats:write", State: "sufficiently-random-state-fixture", Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), KeyThumbprint: thumb}
	return &fixture{s, store, users, db, u, realm, path, a}
}
func (f *fixture) request(path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	method := "GET"
	var body *strings.Reader
	if form != nil {
		method = "POST"
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, testOrigin+path, body)
	if form != nil {
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	return w
}

var hidden = regexp.MustCompile(`name="(flow_id|csrf)" value="([A-Za-z0-9_-]+)"`)

func fields(t *testing.T, w *httptest.ResponseRecorder) url.Values {
	t.Helper()
	v := url.Values{}
	for _, m := range hidden.FindAllStringSubmatch(w.Body.String(), -1) {
		v.Set(m[1], m[2])
	}
	if w.Code != 200 || v.Get("flow_id") == "" || v.Get("csrf") == "" {
		t.Fatalf("no form: %d %s", w.Code, w.Body.String())
	}
	return v
}
func (f *fixture) login(t *testing.T, a oauthstore.Authorization) (url.Values, *http.Cookie) {
	t.Helper()
	w := f.request(authURL(a), nil, nil)
	v := fields(t, w)
	cookie := w.Result().Cookies()[0]
	if cookie.Name != cookieName || !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe cookie", cookie)
	}
	v.Set("username", "owner")
	v.Set("password", "correct horse fixture")
	w = f.request("/oauth2/login", v, cookie)
	v = fields(t, w)
	if !strings.Contains(w.Body.String(), "Разрешить вход") || strings.Contains(w.Body.String(), "<script>") || strings.Contains(w.Body.String(), "correct horse") {
		t.Fatalf("unsafe consent: %s", w.Body.String())
	}
	rotated := w.Result().Cookies()[0]
	if rotated.Value == cookie.Value {
		t.Fatal("cookie not rotated")
	}
	v.Set("decision", "approve")
	return v, rotated
}
func (f *fixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	// Table names are hard-coded by these tests.
	if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *fixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserPasswordConsentCodePKCEAndRestart(t *testing.T) {
	f := setup(t)
	v, cookie := f.login(t, f.a)
	w := f.request("/oauth2/consent", v, cookie)
	if w.Code != 303 {
		t.Fatalf("consent: %d %s", w.Code, w.Body.String())
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := u.Query().Get("code")
	if code == "" || u.Query().Get("state") != f.a.State || u.Query().Get("iss") != testOrigin || u.Host != "mobile.example.test" || u.Path != "/oauth/callback" {
		t.Fatal("invalid callback", u)
	}
	if f.count(t, "oauth_families") != 1 || f.count(t, "oauth_sessions") != 2 {
		t.Fatal("missing atomic grant")
	}
	var authTime int64
	if err = f.db.QueryRow(`SELECT auth_time FROM oauth_browser_flows WHERE id=?`, v.Get("flow_id")).Scan(&authTime); err != nil || time.Since(time.Unix(authTime, 0)) > time.Minute {
		t.Fatal("auth_time", err)
	}
	if w = f.request("/oauth2/consent", v, cookie); w.Code != 400 || w.Header().Get("Location") != "" {
		t.Fatal("consent replay")
	}
	// Reopen with the same protected keys; validate real Fosite PKCE without
	// committing a token exchange (the HTTP token endpoint belongs to BE15).
	reopened, err := oauthstore.Open(testContext, f.path, f.realm)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	cfg := reopened.FrameworkConfig()
	strategy := compose.NewOAuth2HMACStrategy(cfg)
	p := compose.Compose(cfg, reopened, strategy, compose.OAuth2AuthorizeExplicitFactory, compose.OAuth2PKCEFactory)
	rollback := errors.New("validation only")
	for _, tc := range []struct {
		name, client, redirect, verifier string
		ok                               bool
	}{
		{"wrong verifier", "mobile", testRedirect, strings.Repeat("a", 50), false},
		{"wrong client", "other", testRedirect, testVerifier, false},
		{"wrong redirect", "mobile", testRedirect + "/foreign", testVerifier, false},
		{"valid", "mobile", testRedirect, testVerifier, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := false
			err := reopened.TokenRequest(testContext, "authorization_code", code, tc.client, tc.redirect, strategy, func(ctx context.Context) error {
				form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {tc.client}, "redirect_uri": {tc.redirect}, "code_verifier": {tc.verifier}}
				r := httptest.NewRequest("POST", testOrigin+"/oauth2/token", strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				ar, err := p.NewAccessRequest(ctx, r, new(oauthstore.Session))
				if err != nil {
					return err
				}
				parsed = true
				s := ar.GetSession().(*oauthstore.Session)
				if len(ar.GetGrantedScopes()) != 0 || s.Binding.OwnerID != f.user.ID || s.Binding.Stage != oauthstore.Pending || s.Binding.KeyThumbprint != f.a.KeyThumbprint {
					t.Fatal("unexpected code authority")
				}
				return rollback
			})
			if parsed != tc.ok || (tc.ok && !errors.Is(err, rollback)) || (!tc.ok && err == nil) {
				t.Fatalf("PKCE parsed=%v error=%v", parsed, err)
			}
		})
	}
	w = f.request("/oauth2/token", url.Values{}, nil)
	if w.Code != 400 {
		t.Fatal("empty token request accepted")
	}
}

func TestBrowserCSRFOriginCookiesAndFreshPassword(t *testing.T) {
	f := setup(t)
	w := f.request(authURL(f.a), nil, nil)
	v := fields(t, w)
	old := w.Result().Cookies()[0]
	for _, csrf := range []string{"", strings.Repeat("a", 43)} {
		bad := url.Values{"flow_id": {v.Get("flow_id")}, "csrf": {csrf}, "username": {"owner"}, "password": {"correct horse fixture"}}
		if w = f.request("/oauth2/login", bad, old); w.Code != 400 {
			t.Fatal("invalid csrf accepted")
		}
	}
	v.Set("username", "owner")
	v.Set("password", "correct horse fixture")
	r := httptest.NewRequest("POST", testOrigin+"/oauth2/login", strings.NewReader(v.Encode()))
	r.AddCookie(old)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.test")
	w = httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("foreign origin")
	}
	w = f.request("/oauth2/login", v, old)
	consent := fields(t, w)
	cookie := w.Result().Cookies()[0]
	consent.Set("decision", "approve")
	if w = f.request("/oauth2/consent", consent, old); w.Code != 400 {
		t.Fatal("old browser binding accepted")
	}
	// An authenticated browser cookie starts a new password form; no silent SSO
	// and no auth_time fabricated from cookie age, even for fresh_auth=true.
	a := f.a
	a.FreshAuth = true
	w = f.request(authURL(a), nil, cookie)
	fresh := fields(t, w)
	if !strings.Contains(w.Body.String(), `name="password"`) {
		t.Fatal("cookie bypassed password")
	}
	var phase string
	var auth int64
	if err := f.db.QueryRow(`SELECT phase,auth_time FROM oauth_browser_flows WHERE id=?`, fresh.Get("flow_id")).Scan(&phase, &auth); err != nil || phase != "login" || auth != 0 {
		t.Fatal("fresh flow already authenticated", err)
	}
	// The original flow remains bound to its own CSRF, owner and real auth_time.
	if w = f.request("/oauth2/consent", consent, cookie); w.Code != 303 {
		t.Fatalf("original flow: %d", w.Code)
	}
}

func TestAccountPolicyAndCredentialChangeBeforeConsent(t *testing.T) {
	f := setup(t)
	for _, flag := range []string{"disabled", "must_change"} {
		t.Run(flag, func(t *testing.T) {
			f.user.Disabled = flag == "disabled"
			f.user.MustChangePass = flag == "must_change"
			if err := f.users.UpdateUser(testContext, f.user); err != nil {
				t.Fatal(err)
			}
			w := f.request(authURL(f.a), nil, nil)
			v := fields(t, w)
			c := w.Result().Cookies()[0]
			v.Set("username", "owner")
			v.Set("password", "correct horse fixture")
			w = f.request("/oauth2/login", v, c)
			if strings.Contains(w.Body.String(), "Разрешить вход") || len(w.Result().Cookies()) != 0 {
				t.Fatal("unavailable account logged in")
			}
		})
	}
	f.user.Disabled = false
	f.user.MustChangePass = false
	if err := f.users.UpdateUser(testContext, f.user); err != nil {
		t.Fatal(err)
	}
	v, c := f.login(t, f.a)
	hash, _ := bcrypt.GenerateFromPassword([]byte("changed password"), bcrypt.MinCost)
	f.user.PasswordHash = string(hash)
	if err := f.users.UpdateUser(testContext, f.user); err != nil {
		t.Fatal(err)
	}
	if w := f.request("/oauth2/consent", v, c); w.Code != 400 || w.Header().Get("Location") != "" {
		t.Fatal("stale credential accepted")
	}
	if f.count(t, "oauth_families") != 0 {
		t.Fatal("invalid consent wrote family")
	}
}

func TestBrowserCodePKCEFailureRollsBackConsentAndDevice(t *testing.T) {
	f := setup(t)
	v, c := f.login(t, f.a)
	f.exec(t, `CREATE TRIGGER reject_pkce BEFORE INSERT ON oauth_sessions WHEN NEW.kind='pkce' BEGIN SELECT RAISE(ABORT,'injected PKCE failure'); END`)
	w := f.request("/oauth2/consent", v, c)
	if w.Code != 400 || w.Header().Get("Location") != "" {
		t.Fatal("published uncommitted code")
	}
	for _, table := range []string{"oauth_families", "oauth_devices", "oauth_sessions"} {
		if f.count(t, table) != 0 {
			t.Fatal("partial grant", table)
		}
	}
	f.exec(t, `DROP TRIGGER reject_pkce`)
	if w = f.request("/oauth2/consent", v, c); w.Code != 303 {
		t.Fatal("rollback consumed consent")
	}
}

func TestLiveAccountFlagsAndFlowExpiryBeforeConsent(t *testing.T) {
	f := setup(t)
	for _, flag := range []string{"disabled", "must_change", "expired"} {
		t.Run(flag, func(t *testing.T) {
			f.user.Disabled, f.user.MustChangePass = false, false
			if err := f.users.UpdateUser(testContext, f.user); err != nil {
				t.Fatal(err)
			}
			v, cookie := f.login(t, f.a)
			want := 403
			if flag == "expired" {
				f.exec(t, `UPDATE oauth_browser_flows SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).Unix(), v.Get("flow_id"))
				want = 400
			} else {
				f.user.Disabled, f.user.MustChangePass = flag == "disabled", flag == "must_change"
				if err := f.users.UpdateUser(testContext, f.user); err != nil {
					t.Fatal(err)
				}
			}
			if w := f.request("/oauth2/consent", v, cookie); w.Code != want || w.Header().Get("Location") != "" {
				t.Fatalf("unavailable account/flow: %d", w.Code)
			}
			if f.count(t, "oauth_families") != 0 {
				t.Fatal("unavailable account/flow created grant")
			}
		})
	}
}

func TestStrictAuthorizationAndTransport(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct{ key, value string }{
		{"redirect_uri", "https://evil.test/callback"}, {"code_challenge_method", "plain"}, {"code_challenge", ""}, {"iulita_installation_key_thumbprint", ""}, {"state", "short"}, {"response_type", "token"}, {"scope", "chats:read chats:read"}, {"scope", "unknown"}, {"unknown", "x"}, {"iulita_device_id", "not-a-uuid"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			u, _ := url.Parse(authURL(f.a))
			q := u.Query()
			q.Set(tc.key, tc.value)
			u.RawQuery = q.Encode()
			w := f.request(u.String(), nil, nil)
			if w.Code != 400 || w.Header().Get("Location") != "" {
				t.Fatalf("invalid request accepted: %d", w.Code)
			}
		})
	}
	if w := f.request(authURL(f.a)+"&state=another-long-value", nil, nil); w.Code != 400 {
		t.Fatal("duplicate parameter")
	}
	for _, address := range []string{"http://mobile.example.test" + authURL(f.a), "https://evil.test" + authURL(f.a)} {
		r := httptest.NewRequest("GET", address, http.NoBody)
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "mobile.example.test")
		w := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("spoofed TLS/host accepted")
		}
	}
	if f.count(t, "oauth_browser_flows") != 0 {
		t.Fatal("invalid authorize stored flow")
	}
}

func TestBrowserMetadataDenialExpiryAndPersistentLimits(t *testing.T) {
	f := setup(t)
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/oauth2/jwks"} {
		w := f.request(path, nil, nil)
		var value map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &value) != nil || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("metadata", path)
		}
		if path == "/oauth2/jwks" && (strings.Contains(w.Body.String(), `"d":`) || strings.Contains(w.Body.String(), `"k":`)) {
			t.Fatal("private signing material published")
		}
	}
	v, c := f.login(t, f.a)
	v.Set("decision", "deny")
	w := f.request("/oauth2/consent", v, c)
	u, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != 303 || u.Query().Get("error") != "access_denied" || u.Query().Get("state") != f.a.State || f.count(t, "oauth_families") != 0 {
		t.Fatal("deny created authority")
	}
	v, c = f.login(t, f.a)
	f.exec(t, `UPDATE oauth_browser_flows SET auth_time=? WHERE id=?`, time.Now().Add(-6*time.Minute).Unix(), v.Get("flow_id"))
	if w = f.request("/oauth2/consent", v, c); w.Code != 400 {
		t.Fatal("stale password login accepted")
	}
	for i := 0; i < 2; i++ {
		ok, err := f.store.AllowBrowserAttempt(testContext, "test", "sensitive-user-ip", 1, 10*time.Minute)
		if err != nil || ok != (i == 0) {
			t.Fatal("limiter", ok, err)
		}
	}
	reopened, err := oauthstore.Open(testContext, f.path, f.realm)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if ok, stepErr1 := reopened.AllowBrowserAttempt(testContext, "test", "sensitive-user-ip", 1, 10*time.Minute); stepErr1 != nil || ok {
		t.Fatal("restart reset limiter")
	}
	var leaked int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM oauth_browser_limits WHERE CAST(subject AS TEXT) LIKE '%sensitive%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("limiter leaked subject")
	}
}

func TestDeviceHintNeverRebindsKeyOrActivatesInstallation(t *testing.T) {
	f := setup(t)
	issue := func(a oauthstore.Authorization) int {
		v, c := f.login(t, a)
		return f.request("/oauth2/consent", v, c).Code
	}
	if issue(f.a) != 303 {
		t.Fatal("first authorization")
	}
	var device string
	if err := f.db.QueryRow(`SELECT id FROM oauth_devices`).Scan(&device); err != nil {
		t.Fatal(err)
	}
	a := f.a
	a.DeviceHint = device
	if issue(a) != 303 {
		t.Fatal("owned pending hint")
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM oauth_families WHERE stage='pending_activation'`).Scan(&n); err != nil || n != 2 {
		t.Fatal("unproved hint treated as activated")
	}
	a.KeyThumbprint, _ = oauthstore.Secret()
	if issue(a) != 400 {
		t.Fatal("key replaced by browser hint")
	}
	a = f.a
	a.DeviceHint = uuid.NewString()
	if issue(a) != 400 {
		t.Fatal("foreign/missing hint accepted")
	}
	// Activation is a future proof operation; simulate its durable result only.
	f.exec(t, `UPDATE oauth_devices SET activated=1 WHERE id=?`, device)
	a = f.a
	a.DeviceHint = device
	if issue(a) != 303 {
		t.Fatal("registered hint recovery")
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM oauth_families WHERE stage='provisional_recovery'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("missing provisional recovery")
	}
}
