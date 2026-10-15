package oauthstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/handler/oauth2"
)

const verifier = "cryptographically-random-client-verifier-fixture-1234567890"

type engine struct {
	f        *fixtureData
	provider fosite.OAuth2Provider
	strategy oauth2.CoreStrategy
}

func newEngine(f *fixtureData) *engine {
	cfg := f.FrameworkConfig()
	strategy := compose.NewOAuth2HMACStrategy(cfg)
	provider := compose.Compose(cfg, f.Store, strategy, compose.OAuth2AuthorizeExplicitFactory, compose.OAuth2RefreshTokenGrantFactory, compose.OAuth2PKCEFactory)
	return &engine{f, provider, strategy}
}
func (e *engine) authorize(method string) (string, error) {
	session, err := e.f.NewSession(ctx, e.f.owner, e.f.family.ID)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {"mobile"}, "redirect_uri": {redirect}, "state": {"sufficiently-long-state-fixture"}, "scope": {"chats:read"}, "code_challenge_method": {method}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}}
	var code string
	err = e.f.WithRequest(ctx, func(c context.Context) error {
		req, stepErr1 := e.provider.NewAuthorizeRequest(c, httptest.NewRequest("GET", issuer+"/oauth2/authorize?"+q.Encode(), http.NoBody))
		if stepErr1 != nil {
			return stepErr1
		}
		req.SetID(e.f.family.ID)
		req.GrantScope("chats:read")
		resp, stepErr1 := e.provider.NewAuthorizeResponse(c, req, session)
		if stepErr1 != nil {
			return stepErr1
		}
		code = resp.GetCode()
		return nil
	})
	return code, err
}
func codeForm(code string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {"mobile"}, "redirect_uri": {redirect}, "code": {code}, "code_verifier": {verifier}}
}
func refreshForm(secret string) url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "client_id": {"mobile"}, "refresh_token": {secret}}
}
func (e *engine) exchange(form url.Values) (fosite.AccessResponder, error) {
	grantType := form.Get("grant_type")
	raw := form.Get("code")
	if grantType == "refresh_token" {
		raw = form.Get("refresh_token")
	}
	var out fosite.AccessResponder
	err := e.f.TokenRequest(ctx, grantType, raw, form.Get("client_id"), form.Get("redirect_uri"), e.strategy, func(c context.Context) error {
		httpReq := httptest.NewRequest("POST", issuer+"/oauth2/token", strings.NewReader(form.Encode()))
		httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request, err := e.provider.NewAccessRequest(c, httpReq, new(Session))
		if err != nil {
			return err
		}
		out, err = e.provider.NewAccessResponse(c, request)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func issue(t *testing.T, e *engine) fosite.AccessResponder {
	t.Helper()
	code, err := e.authorize("S256")
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.exchange(codeForm(code))
	if err != nil {
		t.Fatalf("exchange: %+v", err)
	}
	return out
}
func refresh(t *testing.T, w fosite.AccessResponder) string {
	t.Helper()
	v, ok := w.GetExtra("refresh_token").(string)
	if !ok || v == "" {
		t.Fatal("missing refresh")
	}
	return v
}
func assertActive(t *testing.T, e *engine, access string, want bool) {
	t.Helper()
	err := e.f.WithRequest(ctx, func(c context.Context) error {
		_, err := e.f.GetAccessTokenSession(c, e.strategy.AccessTokenSignature(c, access), new(Session))
		return err
	})
	if (err == nil) != want {
		t.Fatalf("access active=%v want=%v: %v", err == nil, want, err)
	}
}
func TestFositeAtomicCodePKCEAndTokenRollback(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	// Authorize stores code before PKCE rejects plain. The outer transaction
	// must remove the partial code instead of publishing an unusable grant.
	if _, err := e.authorize("plain"); err == nil {
		t.Fatal("plain accepted")
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM oauth_sessions`).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial authorize state committed")
	}
	code, err := e.authorize("S256")
	if err != nil {
		t.Fatal(err)
	}
	// Inject an actual database error after Fosite has consumed PKCE, invalidated
	// code and inserted access. All phases must roll back together.
	if _, err = f.db.Exec(`CREATE TRIGGER fail_refresh BEFORE INSERT ON oauth_sessions WHEN NEW.kind='refresh' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = e.exchange(codeForm(code)); err == nil {
		t.Fatal("injected failure not reached")
	}
	if _, err = f.db.Exec(`DROP TRIGGER fail_refresh`); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM oauth_sessions WHERE kind IN ('access','refresh')`).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial tokens survived failure")
	}
	// Unlike the BE12 prototype, both the code AND PKCE state are still usable.
	out, err := e.exchange(codeForm(code))
	if err != nil {
		t.Fatalf("PKCE state lost: %+v", err)
	}
	assertActive(t, e, out.GetAccessToken(), true)
	if _, err = e.exchange(codeForm(code)); !errors.Is(err, fosite.ErrInvalidGrant) {
		t.Fatal(err)
	}
	assertActive(t, e, out.GetAccessToken(), false)
	family, err := f.Family(ctx, f.owner, f.family.ID)
	if err != nil || !family.Revoked {
		t.Fatal("rejected code replay rolled back its revocation")
	}
}
func TestFositePersistentRestartRefreshAndLostResponse(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	code, err := e.authorize("S256")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	realm, err := LoadRealm(filepath.Join(f.dir, "keys"), issuer, audience)
	if err != nil {
		t.Fatal(err)
	}
	f.Store, err = Open(ctx, f.path, realm)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	e = newEngine(f)
	first, err := e.exchange(codeForm(code))
	if err != nil {
		t.Fatal(err)
	}
	old := refresh(t, first)
	next, err := e.exchange(refreshForm(old))
	if err != nil {
		t.Fatal(err)
	}
	assertActive(t, e, first.GetAccessToken(), false)
	assertActive(t, e, next.GetAccessToken(), true)
	family, err := f.Family(ctx, f.owner, f.family.ID)
	if err != nil || family.Generation != 1 {
		t.Fatalf("generation: %#v %v", family, err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	f.Store, err = Open(ctx, f.path, realm)
	if err != nil {
		t.Fatal(err)
	}
	e = newEngine(f)
	family, err = f.Family(ctx, f.owner, f.family.ID)
	if err != nil || family.Generation != 1 {
		t.Fatal("restart lost generation")
	}
	// Successful rotation committed; response was lost to the client.
	if _, err = e.exchange(refreshForm(old)); !errors.Is(err, fosite.ErrInvalidGrant) {
		t.Fatal(err)
	}
	assertActive(t, e, next.GetAccessToken(), false)
	if _, err = e.exchange(refreshForm(refresh(t, next))); !errors.Is(err, fosite.ErrInvalidGrant) {
		t.Fatal(err)
	}
}
func TestFositeConcurrentRefreshAcrossStores(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	first := issue(t, e)
	secret := refresh(t, first)
	secondStore, err := Open(ctx, f.path, f.realm)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	copyFixture := *f
	copyFixture.Store = secondStore
	other := newEngine(&copyFixture)
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	results := make([]fosite.AccessResponder, 2)
	errs := make([]error, 2)
	for i, item := range []*engine{e, other} {
		go func(i int, engine *engine) {
			defer wg.Done()
			<-start
			results[i], errs[i] = engine.exchange(refreshForm(secret))
		}(i, item)
	}
	close(start)
	wg.Wait()
	winners := 0
	for i, err := range errs {
		if err == nil {
			winners++
			assertActive(t, e, results[i].GetAccessToken(), false)
		} else if !errors.Is(err, fosite.ErrInvalidGrant) {
			t.Fatalf("unsafe conflict result: %+v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d errors=%v", winners, errs)
	}
	family, err := f.Family(ctx, f.owner, f.family.ID)
	if err != nil || !family.Revoked {
		t.Fatal("concurrent reuse did not persist revocation")
	}
}
func TestReplaySideEffectsRequireClientAndCryptographicBinding(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	first := issue(t, e)
	old := refresh(t, first)
	next, err := e.exchange(refreshForm(old))
	if err != nil {
		t.Fatal(err)
	}
	wrongClient := refreshForm(old)
	wrongClient.Set("client_id", "other")
	if _, err = e.exchange(wrongClient); !errors.Is(err, fosite.ErrInvalidGrant) {
		t.Fatal(err)
	}
	assertActive(t, e, next.GetAccessToken(), true)
	// Preserve the old HMAC signature to hit its DB row, alter the random body.
	parts := strings.Split(old, ".")
	parts[0] += "forged"
	forged := strings.Join(parts, ".")
	if _, err = e.exchange(refreshForm(forged)); !errors.Is(err, fosite.ErrInvalidGrant) {
		t.Fatal(err)
	}
	assertActive(t, e, next.GetAccessToken(), true)
	if _, err = e.exchange(refreshForm(old)); !errors.Is(err, fosite.ErrInvalidGrant) {
		t.Fatal(err)
	}
	assertActive(t, e, next.GetAccessToken(), false)
}
func TestSessionPersistenceContainsNoRawSecrets(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	code, err := e.authorize("S256")
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.exchange(codeForm(code))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.db.Query(`SELECT data FROM oauth_sessions UNION ALL SELECT data FROM oauth_clients`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{code, verifier, out.GetAccessToken(), refresh(t, out), base64.RawURLEncoding.EncodeToString(f.realm.hmacKey)} {
			if strings.Contains(data, raw) {
				t.Fatal("raw secret persisted")
			}
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidVerifierRollsBackPKCEDeletion(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	code, err := e.authorize("S256")
	if err != nil {
		t.Fatal(err)
	}
	bad := codeForm(code)
	bad.Set("code_verifier", strings.Repeat("x", 55))
	if _, err = e.exchange(bad); err == nil {
		t.Fatal("wrong verifier accepted")
	}
	if _, err = e.exchange(codeForm(code)); err != nil {
		t.Fatalf("valid retry lost PKCE: %+v", err)
	}
}

func TestFositeRefreshFailureRestoresGeneration(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	first := issue(t, e)
	secret := refresh(t, first)
	if _, err := f.db.Exec(`CREATE TRIGGER fail_refresh BEFORE INSERT ON oauth_sessions WHEN NEW.kind='refresh' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.exchange(refreshForm(secret)); err == nil {
		t.Fatal("injected failure not reached")
	}
	if _, err := f.db.Exec(`DROP TRIGGER fail_refresh`); err != nil {
		t.Fatal(err)
	}
	family, err := f.Family(ctx, f.owner, f.family.ID)
	if err != nil || family.Generation != 0 || family.Revoked {
		t.Fatalf("partial generation: %#v %v", family, err)
	}
	assertActive(t, e, first.GetAccessToken(), true)
	if _, err = e.exchange(refreshForm(secret)); err != nil {
		t.Fatalf("rolled-back refresh consumed: %v", err)
	}
}

func TestReplayAuthHeaderCannotChangeBoundClient(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	first := issue(t, e)
	old := refresh(t, first)
	next, err := e.exchange(refreshForm(old))
	if err != nil {
		t.Fatal(err)
	}
	err = f.TokenRequest(ctx, "refresh_token", old, "mobile", "", e.strategy, func(c context.Context) error {
		httpReq := httptest.NewRequest("POST", issuer+"/oauth2/token", strings.NewReader(refreshForm(old).Encode()))
		httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		httpReq.SetBasicAuth("other", "")
		_, stepErr2 := e.provider.NewAccessRequest(c, httpReq, new(Session))
		return stepErr2
	})
	if err == nil {
		t.Fatal("auth header changed the pre-bound client")
	}
	assertActive(t, e, next.GetAccessToken(), true)
}
