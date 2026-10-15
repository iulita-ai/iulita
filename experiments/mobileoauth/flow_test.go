package mobileoauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/handler/oauth2"
	fjwt "github.com/ory/fosite/token/jwt"
)

const redirectURI = "https://mobile.example.test/oauth/callback"
const verifier = "test-verifier-must-have-at-least-forty-three-characters-12345"

var ctx = context.Background()

type stageExtension struct{}

var _ fosite.TokenEndpointHandler = stageExtension{}

func (stageExtension) CanHandleTokenEndpointRequest(_ context.Context, r fosite.AccessRequester) bool {
	return r.GetGrantTypes().ExactOne("authorization_code") || r.GetGrantTypes().ExactOne("refresh_token")
}
func (stageExtension) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool { return false }
func (stageExtension) HandleTokenEndpointRequest(_ context.Context, r fosite.AccessRequester) error {
	s, ok := r.GetSession().(*stageSession)
	if !ok {
		return fosite.ErrInvalidGrant
	}
	switch s.Stage {
	case "active":
		if s.Challenge != nil {
			return fosite.ErrInvalidGrant
		}
	case "pending_activation", "provisional_recovery":
		if s.Challenge == nil || !s.Challenge.ExpiresAt.After(time.Now()) || len(r.GetGrantedScopes()) != 0 {
			return fosite.ErrInvalidGrant
		}
		// Pending/recovery deadlines survive refresh; no sliding deadline.
		for _, typ := range []fosite.TokenType{fosite.AccessToken, fosite.RefreshToken} {
			if s.GetExpiresAt(typ).After(s.Challenge.ExpiresAt) {
				s.SetExpiresAt(typ, s.Challenge.ExpiresAt)
			}
		}
	default:
		return fosite.ErrInvalidGrant
	}
	claims := s.GetJWTClaims().(*fjwt.JWTClaims)
	if claims.Extra == nil {
		claims.Extra = map[string]any{}
	}
	// Fosite retains the persisted JWT claim object across refreshes. Issue a
	// fresh jti/iat per token; immutable authorization data remains in the session.
	claims.JTI = ""
	claims.IssuedAt = time.Time{}
	claims.Extra["token_stage"] = s.Stage
	return nil
}
func (stageExtension) PopulateTokenEndpointResponse(_ context.Context, r fosite.AccessRequester, w fosite.AccessResponder) error {
	s := r.GetSession().(*stageSession)
	w.SetExtra("token_stage", s.Stage)
	if s.Challenge != nil {
		w.SetExtra("challenge", s.Challenge)
	}
	return nil
}

type harness struct {
	*store
	provider fosite.OAuth2Provider
	strategy oauth2.CoreStrategy
}

func newHarness(t *testing.T, path string, key *rsa.PrivateKey) *harness {
	t.Helper()
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	cfg := &fosite.Config{GlobalSecret: []byte("synthetic-32-byte-global-secret-1234567890"), EnforcePKCE: true, EnablePKCEPlainChallengeMethod: false, ScopeStrategy: fosite.ExactScopeStrategy, RefreshTokenScopes: []string{}, AccessTokenLifespan: 15 * time.Minute, RefreshTokenLifespan: 24 * time.Hour, AuthorizeCodeLifespan: time.Minute, AccessTokenIssuer: "https://mobile.example.test", JWTScopeClaimKey: fjwt.JWTScopeFieldString}
	strategy := compose.NewOAuth2JWTStrategy(func(context.Context) (any, error) { return key, nil }, compose.NewOAuth2HMACStrategy(cfg), cfg)
	provider := compose.Compose(cfg, s, strategy, compose.OAuth2AuthorizeExplicitFactory, compose.OAuth2RefreshTokenGrantFactory, compose.OAuth2PKCEFactory, func(fosite.Configurator, interface{}, interface{}) interface{} { return stageExtension{} })
	return &harness{s, provider, strategy}
}
func fixture(t *testing.T) (*harness, string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "oauth.sqlite")
	h := newHarness(t, path, key)
	for _, id := range []string{"mobile", "other"} {
		if err = h.putClient(&fosite.DefaultClient{ID: id, Public: true, RedirectURIs: []string{redirectURI}, GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scopes: []string{"chat"}}); err != nil {
			t.Fatal(err)
		}
	}
	return h, path, key
}
func challengeFor(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func authorize(h *harness, stage, method, challengeValue string) (string, error) {
	q := url.Values{"client_id": {"mobile"}, "redirect_uri": {redirectURI}, "response_type": {"code"}, "state": {"long-random-state-fixture"}}
	if stage == "active" {
		q.Set("scope", "chat")
	}
	if method != "" {
		q.Set("code_challenge_method", method)
		q.Set("code_challenge", challengeValue)
	}
	r, err := h.provider.NewAuthorizeRequest(ctx, httptest.NewRequest("GET", "https://mobile.example.test/oauth2/authorize?"+q.Encode(), nil))
	if err != nil {
		return "", err
	}
	// Simulated trusted login, never an owner/stage taken from token request input.
	for _, scope := range r.GetRequestedScopes() {
		r.GrantScope(scope)
	}
	s := &stageSession{Stage: stage, JWTSession: oauth2.JWTSession{Subject: "owner", JWTClaims: &fjwt.JWTClaims{Subject: "owner"}}}
	s.GetJWTHeader().Add("typ", "at+jwt")
	s.GetJWTHeader().Add("kid", "fixture-key")
	if stage != "active" {
		s.Challenge = &challenge{Nonce: "synthetic-random-nonce", DeviceID: "installation", ExpiresAt: time.Now().UTC().Add(3 * time.Minute).Truncate(time.Second)}
	}
	w, err := h.provider.NewAuthorizeResponse(ctx, r, s)
	if err != nil {
		return "", err
	}
	return w.GetCode(), nil
}
func authForm(code string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {"mobile"}, "redirect_uri": {redirectURI}, "code": {code}, "code_verifier": {verifier}}
}
func refreshForm(token string) url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "client_id": {"mobile"}, "refresh_token": {token}}
}
func (h *harness) prepare(form url.Values) (fosite.AccessRequester, error) {
	req := httptest.NewRequest("POST", "https://mobile.example.test/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return h.provider.NewAccessRequest(ctx, req, new(stageSession))
}
func (h *harness) mint(r fosite.AccessRequester) (fosite.AccessResponder, error) {
	w, err := h.provider.NewAccessResponse(ctx, r)
	if errors.Is(err, fosite.ErrSerializationFailure) {
		// Prototype adapter for the mobile policy: Fosite's default asks to retry.
		// Its internal transaction has already rolled back here. Revoke durably
		// before returning invalid_grant; never let the winner survive a reuse race.
		tx, e := h.BeginTX(ctx)
		if e != nil {
			return nil, e
		}
		if e = h.RevokeRefreshToken(tx, r.GetID()); e == nil {
			e = h.RevokeAccessToken(tx, r.GetID())
		}
		if e != nil {
			_ = h.Rollback(tx)
			return nil, e
		}
		if e = h.Commit(tx); e != nil {
			return nil, e
		}
		return nil, fosite.ErrInvalidGrant.WithHint("Start a fresh authorization with PKCE.")
	}
	return w, err
}
func (h *harness) exchange(form url.Values) (fosite.AccessResponder, error) {
	r, err := h.prepare(form)
	if err != nil {
		return nil, err
	}
	return h.mint(r)
}
func issue(t *testing.T, h *harness, stage string) fosite.AccessResponder {
	t.Helper()
	code, err := authorize(h, stage, "S256", challengeFor(verifier))
	if err != nil {
		t.Fatal(err)
	}
	w, err := h.exchange(authForm(code))
	if err != nil {
		t.Fatal(fosite.ErrorToRFC6749Error(err).GetDescription())
	}
	return w
}
func refreshToken(t *testing.T, w fosite.AccessResponder) string {
	t.Helper()
	token, ok := w.GetExtra("refresh_token").(string)
	if !ok || token == "" {
		t.Fatal("missing opaque refresh")
	}
	return token
}
func assertInvalid(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, fosite.ErrInvalidGrant) {
		t.Fatalf("want invalid_grant, got %#v", err)
	}
}
func assertAccess(t *testing.T, h *harness, access string, want bool) {
	t.Helper()
	_, err := h.GetAccessTokenSession(ctx, h.strategy.AccessTokenSignature(ctx, access), new(stageSession))
	if (err == nil) != want {
		t.Fatalf("access usable=%v, want=%v: %v", err == nil, want, err)
	}
	if want {
		if err = h.strategy.ValidateAccessToken(ctx, nil, access); err != nil {
			t.Fatal(err)
		}
	}
}
func TestPersistentRestartAndLostResponse(t *testing.T) {
	h, path, key := fixture(t)
	code, err := authorize(h, "active", "S256", challengeFor(verifier))
	if err != nil {
		t.Fatal(err)
	}
	// Close every DB connection and reconstruct both provider and store.
	if err = h.db.Close(); err != nil {
		t.Fatal(err)
	}
	h = newHarness(t, path, key)
	w, err := h.exchange(authForm(code))
	if err != nil {
		t.Fatal(err)
	}
	first := refreshToken(t, w)
	assertAccess(t, h, w.GetAccessToken(), true)
	if err = h.db.Close(); err != nil {
		t.Fatal(err)
	}
	h = newHarness(t, path, key)
	w2, err := h.exchange(refreshForm(first))
	if err != nil {
		t.Fatal(err)
	}
	next := refreshToken(t, w2)
	if jwtClaims(t, w.GetAccessToken())["jti"] == jwtClaims(t, w2.GetAccessToken())["jti"] {
		t.Fatal("JWT identifier reused during refresh")
	}
	if next == first || strings.Count(next, ".") == 2 {
		t.Fatal("refresh not rotated/opaque")
	}
	assertAccess(t, h, w.GetAccessToken(), false)
	assertAccess(t, h, w2.GetAccessToken(), true)
	// Server committed; client lost w2 and only has the old secret after restart.
	if err = h.db.Close(); err != nil {
		t.Fatal(err)
	}
	h = newHarness(t, path, key)
	_, err = h.exchange(refreshForm(first))
	assertInvalid(t, err)
	assertAccess(t, h, w2.GetAccessToken(), false)
	_, err = h.exchange(refreshForm(next))
	assertInvalid(t, err)
	// Fresh PKCE creates an independent, usable family; no response cache needed.
	recovered := issue(t, h, "active")
	assertAccess(t, h, recovered.GetAccessToken(), true)
}
func TestPKCEAndCodeBinding(t *testing.T) {
	for _, name := range []string{"missing", "plain", "wrong verifier", "wrong redirect", "wrong client", "code replay"} {
		t.Run(name, func(t *testing.T) {
			h, _, _ := fixture(t)
			if name == "missing" || name == "plain" {
				method := ""
				if name == "plain" {
					method = "plain"
				}
				_, err := authorize(h, "active", method, verifier)
				if err == nil {
					t.Fatal("insecure PKCE accepted")
				}
				return
			}
			code, err := authorize(h, "active", "S256", challengeFor(verifier))
			if err != nil {
				t.Fatal(err)
			}
			form := authForm(code)
			switch name {
			case "wrong verifier":
				form.Set("code_verifier", strings.Repeat("z", 55))
			case "wrong redirect":
				form.Set("redirect_uri", "https://evil.example.test/cb")
			case "wrong client":
				form.Set("client_id", "other")
			case "code replay":
				if _, err = h.exchange(form); err != nil {
					t.Fatal(err)
				}
			}
			_, err = h.exchange(form)
			if err == nil {
				t.Fatal("invalid code exchange accepted")
			}
		})
	}
}
func TestConcurrentRefreshCASAcrossIndependentStores(t *testing.T) {
	h, path, key := fixture(t)
	other := newHarness(t, path, key)
	initial := issue(t, h, "active")
	token := refreshToken(t, initial)
	// Both HTTP request validation phases see the same active token before either
	// response phase begins, exercising the actual SQL compare-and-swap conflict.
	r1, err := h.prepare(refreshForm(token))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := other.prepare(refreshForm(token))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	responses := make([]fosite.AccessResponder, 2)
	errs := make([]error, 2)
	for i, p := range []struct {
		h *harness
		r fosite.AccessRequester
	}{{h, r1}, {other, r2}} {
		go func(i int, p struct {
			h *harness
			r fosite.AccessRequester
		}) {
			defer wg.Done()
			<-start
			responses[i], errs[i] = p.h.mint(p.r)
		}(i, p)
	}
	close(start)
	wg.Wait()
	success := 0
	for i, err := range errs {
		if err == nil {
			success++
			assertAccess(t, h, responses[i].GetAccessToken(), false)
			_, err = h.exchange(refreshForm(refreshToken(t, responses[i])))
			assertInvalid(t, err)
		} else {
			assertInvalid(t, err)
		}
	}
	if success != 1 {
		t.Fatalf("CAS winners=%d errors=%v", success, errs)
	}
}
func TestRefreshTransactionRollback(t *testing.T) {
	h, _, _ := fixture(t)
	initial := issue(t, h, "active")
	token := refreshToken(t, initial)
	h.failRefreshInsert = true
	_, err := h.exchange(refreshForm(token))
	if err == nil {
		t.Fatal("failure injection not reached")
	}
	h.failRefreshInsert = false
	assertAccess(t, h, initial.GetAccessToken(), true)
	// Old generation is intact because rotation and all inserts rolled back.
	if _, err = h.exchange(refreshForm(token)); err != nil {
		t.Fatal(err)
	}
}
func TestPersistedStageAndChallengeCannotBePromotedByForm(t *testing.T) {
	for _, stage := range []string{"pending_activation", "provisional_recovery", "active"} {
		t.Run(stage, func(t *testing.T) {
			h, path, key := fixture(t)
			initial := issue(t, h, stage)
			token := refreshToken(t, initial)
			original := initial.GetExtra("challenge")
			if err := h.db.Close(); err != nil {
				t.Fatal(err)
			}
			h = newHarness(t, path, key)
			form := refreshForm(token)
			form.Set("token_stage", "active")
			form.Set("challenge", "forged")
			w, err := h.exchange(form)
			if err != nil {
				t.Fatal(err)
			}
			if w.GetExtra("token_stage") != stage {
				t.Fatal("stage promotion")
			}
			a, _ := json.Marshal(original)
			b, _ := json.Marshal(w.GetExtra("challenge"))
			if string(a) != string(b) {
				t.Fatal("challenge/deadline changed")
			}
			if stage != "active" && w.ToMap()["scope"] != "" {
				t.Fatal("provisional token acquired scopes")
			}
			parts := strings.Split(w.GetAccessToken(), ".")
			headerBytes, _ := base64.RawURLEncoding.DecodeString(parts[0])
			var header map[string]any
			_ = json.Unmarshal(headerBytes, &header)
			if header["typ"] != "at+jwt" || header["alg"] != "RS256" {
				t.Fatalf("JWT header: %v", header)
			}
			claimBytes, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims map[string]any
			_ = json.Unmarshal(claimBytes, &claims)
			if claims["token_stage"] != stage || claims["iss"] != "https://mobile.example.test" || claims["sub"] != "owner" {
				t.Fatalf("claims: %v", claims)
			}
			if err = h.strategy.ValidateAccessToken(ctx, nil, w.GetAccessToken()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestStoredPayloadNeverContainsBearerSecrets(t *testing.T) {
	h, _, _ := fixture(t)
	code, err := authorize(h, "active", "S256", challengeFor(verifier))
	if err != nil {
		t.Fatal(err)
	}
	w, err := h.exchange(authForm(code))
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{code, verifier, w.GetAccessToken(), refreshToken(t, w)}
	rows, err := h.db.Query(`SELECT data FROM grants UNION ALL SELECT data FROM clients`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			t.Fatal(err)
		}
		for _, secret := range secrets {
			if strings.Contains(data, secret) {
				t.Fatal("raw credential in SQLite payload")
			}
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func jwtClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("not a JWT")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err = json.Unmarshal(b, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestExpiredProvisionalChallengeCannotMintToken(t *testing.T) {
	h, _, _ := fixture(t)
	initial := issue(t, h, "provisional_recovery")
	r, err := h.prepare(refreshForm(refreshToken(t, initial)))
	if err != nil {
		t.Fatal(err)
	}
	// Session validation is a handler boundary, not input from the client form.
	r.GetSession().(*stageSession).Challenge.ExpiresAt = time.Now().Add(-time.Second)
	err = (stageExtension{}).HandleTokenEndpointRequest(ctx, r)
	assertInvalid(t, err)
}

func TestAuthorizationAndPKCEAreOutsideCoreTransactionHook(t *testing.T) {
	h, _, _ := fixture(t)
	code, err := authorize(h, "active", "S256", challengeFor(verifier))
	if err != nil {
		t.Fatal(err)
	}
	h.failRefreshInsert = true
	if _, err = h.exchange(authForm(code)); err == nil {
		t.Fatal("failure injection not reached")
	}
	h.failRefreshInsert = false
	// Fosite's core transaction rolls back the code consumption and token writes,
	// but its PKCE handler removed its row before that transaction began. This
	// regression documents why production needs a request-wide transaction adapter.
	sig := h.strategy.AuthorizeCodeSignature(ctx, code)
	if _, err = h.GetAuthorizeCodeSession(ctx, sig, new(stageSession)); err != nil {
		t.Fatal(err)
	}
	if _, err = h.GetPKCERequestSession(ctx, sig, new(stageSession)); !errors.Is(err, fosite.ErrNotFound) {
		t.Fatalf("expected independently consumed PKCE: %v", err)
	}
	if _, err = h.exchange(authForm(code)); err == nil {
		t.Fatal("unexpected exchange after lost PKCE state")
	}
	var tokens int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM grants WHERE kind IN ('access','refresh')`).Scan(&tokens); err != nil || tokens != 0 {
		t.Fatalf("partial token commit: %d %v", tokens, err)
	}
}
