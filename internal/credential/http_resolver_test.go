package credential

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/httpfetch"
)

// mockRepoWithGrants embeds the existing mockRepo.
type mockRepoWithGrants struct{ mockRepo }

// testCrypto is a no-op crypto provider (values stored plaintext in tests).
type testCrypto struct{}

func (testCrypto) Encrypt(p string) (string, error) { return p, nil }
func (testCrypto) Decrypt(c string) (string, error) { return c, nil }
func (testCrypto) EncryptionEnabled() bool          { return false }

func resolverFixture(t *testing.T) (*HTTPResolver, *mockRepoWithGrants) {
	t.Helper()
	repo := &mockRepoWithGrants{mockRepo: *newMockRepo()}
	s := NewStore(repo, testCrypto{}, nil)
	return NewHTTPResolver(s), repo
}

func TestHTTPResolverFailsClosed(t *testing.T) {
	r, _ := resolverFixture(t)
	ctx := context.Background()
	origin := httpfetch.Origin{Scheme: "https", Host: "api.example.test", Port: "443"}
	scope := httpfetch.Scope{UserID: "u1", Caller: httpfetch.Caller{ID: "http_request", Kind: httpfetch.CallerTool}}

	// Anonymous scope never resolves credentials.
	if _, err := r.ResolveHTTP(ctx, httpfetch.Scope{UserID: ""}, "k", origin, "bearer"); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("anonymous resolve: %v", err)
	}
	// Unknown credential name.
	if _, err := r.ResolveHTTP(ctx, scope, "missing", origin, "bearer"); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("missing credential: %v", err)
	}
}

func TestHTTPResolverGrantMatrix(t *testing.T) {
	r, repo := resolverFixture(t)
	ctx := context.Background()
	origin := httpfetch.Origin{Scheme: "https", Host: "api.example.test", Port: "443"}
	otherOrigin := httpfetch.Origin{Scheme: "https", Host: "evil.test", Port: "443"}
	scope := httpfetch.Scope{UserID: "u1", Caller: httpfetch.Caller{ID: "http_request", Kind: httpfetch.CallerTool}}

	// User-owned http_basic credential.
	cred := &domain.Credential{
		Name: "example-api", Type: domain.CredentialTypeHTTPBasic,
		Scope: domain.CredentialScopeUser, OwnerID: "u1",
		Value: `{"username":"alice","password":"pw"}`,
	}
	if err := repo.SaveCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	// Without a grant: denied (no fallback of any kind).
	if _, err := r.ResolveHTTP(ctx, scope, "example-api", origin, "basic"); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("no-grant resolve: %v", err)
	}

	// Grant for the exact origin, bound to the http_request caller, basic usage.
	grant := &domain.HTTPCredentialGrant{
		CredentialID: cred.ID, PrincipalType: domain.HTTPGrantPrincipalUser, PrincipalID: "u1",
		CallerID: "http_request", Scheme: "https", Host: "api.example.test", Port: "443", Usages: `["basic"]`,
	}
	if err := repo.SaveHTTPCredentialGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}

	got, err := r.ResolveHTTP(ctx, scope, "example-api", origin, "basic")
	if err != nil {
		t.Fatalf("granted resolve: %v", err)
	}
	if got.Username != "alice" || got.Password != "pw" {
		t.Fatalf("resolved = %+v", got)
	}

	// Different origin: denied.
	if _, err := r.ResolveHTTP(ctx, scope, "example-api", otherOrigin, "basic"); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("cross-origin resolve: %v", err)
	}

	// Different user: credential not even found (owner scoping), denied.
	otherScope := httpfetch.Scope{UserID: "u2", Caller: scope.Caller}
	if _, err := r.ResolveHTTP(ctx, otherScope, "example-api", origin, "basic"); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("other-user resolve: %v", err)
	}

	// Caller mismatch: denied.
	blockedCaller := httpfetch.Scope{UserID: "u1", Caller: httpfetch.Caller{ID: "other-skill", Kind: httpfetch.CallerNativeSkill}}
	if _, err := r.ResolveHTTP(ctx, blockedCaller, "example-api", origin, "basic"); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("caller-mismatch resolve: %v", err)
	}

	// Expired grant: denied.
	expired := &domain.HTTPCredentialGrant{
		CredentialID: cred.ID, PrincipalType: domain.HTTPGrantPrincipalUser, PrincipalID: "u1",
		Scheme: "https", Host: "api.example.test", Port: "443", Usages: `["bearer"]`,
		ExpiresAt: ptrTime(time.Now().Add(-time.Hour)),
	}
	if err := repo.SaveHTTPCredentialGrant(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveHTTP(ctx, scope, "example-api", origin, "bearer"); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("expired-grant resolve: %v", err)
	}
	if _, err := r.ResolveHTTP(ctx, scope, "example-api", origin, "basic"); err != nil {
		t.Fatalf("valid grant shadowed by expired one: %v", err)
	}
}

func TestHTTPResolverGlobalNeedsPublishedGrant(t *testing.T) {
	r, repo := resolverFixture(t)
	ctx := context.Background()
	origin := httpfetch.Origin{Scheme: "https", Host: "api.example.test", Port: "443"}
	scope := httpfetch.Scope{UserID: "u1", Caller: httpfetch.Caller{ID: "http_request", Kind: httpfetch.CallerTool}}

	// A GLOBAL credential (e.g. an LLM key by name) must never resolve for
	// the HTTP tool without an explicit published global grant.
	secret := &domain.Credential{
		Name: "claude.api_key", Type: domain.CredentialTypeAPIKey,
		Scope: domain.CredentialScopeGlobal, Value: "sk-live-secret",
	}
	if err := repo.SaveCredential(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveHTTP(ctx, scope, "claude.api_key", origin, "header"); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("global without published grant resolved: %v", err)
	}

	pub := &domain.HTTPCredentialGrant{
		CredentialID: secret.ID, PrincipalType: domain.HTTPGrantPrincipalGlobal,
		CallerID: "http_request", Scheme: "https", Host: "api.example.test", Port: "443",
		Usages: `["header"]`,
	}
	if err := repo.SaveHTTPCredentialGrant(ctx, pub); err != nil {
		t.Fatal(err)
	}
	got, err := r.ResolveHTTP(ctx, scope, "claude.api_key", origin, "header")
	if err != nil {
		t.Fatalf("published global grant: %v", err)
	}
	if got.Secret != "sk-live-secret" {
		t.Fatalf("resolved = %+v", got)
	}
	// The published grant is origin+caller bound.
	if _, err := r.ResolveHTTP(ctx, scope, "claude.api_key", otherOriginT(), "header"); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("published grant leaked cross-origin: %v", err)
	}
}

func otherOriginT() httpfetch.Origin {
	return httpfetch.Origin{Scheme: "https", Host: "elsewhere.test", Port: "443"}
}

func ptrTime(t time.Time) *time.Time { return &t }
