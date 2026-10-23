package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type oauthTransport struct {
	exchanges atomic.Int32
}

func (f *oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body string
	switch r.URL.Path {
	case "/.well-known/oauth-protected-resource/mcp":
		body = `{"resource":"https://fixture.example/mcp","authorization_servers":["https://auth.fixture.example"]}`
	case "/.well-known/oauth-authorization-server":
		body = `{"issuer":"https://auth.fixture.example","authorization_endpoint":"https://auth.fixture.example/authorize","token_endpoint":"https://auth.fixture.example/token","code_challenge_methods_supported":["S256"]}`
	case "/token":
		f.exchanges.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("resource") != "https://fixture.example/mcp" || r.Form.Get("code_verifier") == "" && r.Form.Get("grant_type") == "authorization_code" {
			return nil, ErrDenied
		}
		body = `{"access_token":"OAUTH-CANARY","refresh_token":"REFRESH-CANARY","token_type":"Bearer","expires_in":3600,"scope":"read"}`
	default:
		return nil, ErrDenied
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}
func oauthFixture(t *testing.T) (fixtureState, Connection, *oauthTransport) {
	t.Helper()
	f := fixture(t)
	c, _ := setup(t, f)
	s := f.s
	s.mu.Lock()
	s.deploymentOrigins = append(s.deploymentOrigins, "https://auth.fixture.example")
	s.policy.AllowedOrigins = append(s.policy.AllowedOrigins, "https://auth.fixture.example")
	s.mu.Unlock()
	transport := &oauthTransport{}
	s.protocol = &SDKProtocol{Transport: transport}
	c, _ = s.Connection(context.Background(), f.sc, c.ID)
	return f, c, transport
}
func TestOAuthPKCEStateOwnerAndReplay(t *testing.T) {
	f, c, transport := oauthFixture(t)
	target, e := f.s.BeginOAuth(context.Background(), f.sc, c.ID, OAuthStart{ClientID: "registered-client", Issuer: "https://auth.fixture.example", Scopes: "read", Revision: c.Revision}, "https://iulita.example/api/mcp/oauth/callback", "BROWSER-COOKIE")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(target)
	q := u.Query()
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" || q.Get("resource") != c.Endpoint {
		t.Fatal(q)
	}
	state := q.Get("state")
	if e = f.s.FinishOAuth(context.Background(), state, "code", "https://auth.fixture.example", "wrong-cookie"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e = f.s.FinishOAuth(context.Background(), state, "code", "https://other.example", "BROWSER-COOKIE"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e = f.s.FinishOAuth(context.Background(), state, "code", "https://auth.fixture.example", "BROWSER-COOKIE"); e != nil {
		t.Fatal(e)
	}
	if e = f.s.FinishOAuth(context.Background(), state, "code", "https://auth.fixture.example", "BROWSER-COOKIE"); e == nil || transport.exchanges.Load() != 1 {
		t.Fatal(e)
	}
	v, _ := f.s.Connection(context.Background(), f.sc, c.ID)
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "OAUTH-CANARY") || v.Desired != "disabled" {
		t.Fatal(string(raw))
	}
}
func TestOAuthCallbackAfterDisableAndRefresh(t *testing.T) {
	f, c, transport := oauthFixture(t)
	target, e := f.s.BeginOAuth(context.Background(), f.sc, c.ID, OAuthStart{ClientID: "client", Issuer: "https://auth.fixture.example", Scopes: "read", Revision: c.Revision}, "https://iulita.example/api/mcp/oauth/callback", "cookie")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(target)
	current, _ := f.s.Connection(context.Background(), f.sc, c.ID)
	_, _ = f.s.Action(context.Background(), f.sc, c.ID, "disconnect", current.Revision)
	if e = f.s.FinishOAuth(context.Background(), u.Query().Get("state"), "code", "https://auth.fixture.example", "cookie"); !errors.Is(e, ErrConflict) || transport.exchanges.Load() != 0 {
		t.Fatal(e)
	}
	f.s.mu.Lock()
	conn := f.s.connections[c.ID]
	conn.OAuth = &OAuthBinding{Issuer: "https://auth.fixture.example", ClientID: "client", Resource: conn.Endpoint, TokenEndpoint: "https://auth.fixture.example/token", RefreshToken: "REFRESH", Scopes: "read", Expiry: time.Now().Add(-time.Second)}
	conn.AuthMode = "oauth"
	conn.AuthState = "valid"
	conn.Desired = "disabled"
	f.s.mu.Unlock()
	if e = f.s.RefreshAuth(context.Background(), f.sc, c.ID); !errors.Is(e, ErrUnavailable) || transport.exchanges.Load() != 0 {
		t.Fatal("disabled connection refreshed credentials", e)
	}
	// Explicit connection actions may refresh credentials before discovery.
	if _, e = f.s.refreshAuth(context.Background(), f.sc, c.ID, 0, true); e != nil {
		t.Fatal(e)
	}
	v, _ := f.s.Connection(context.Background(), f.sc, c.ID)
	if v.Desired != "disabled" || v.AuthState != "valid" {
		t.Fatal(v)
	}
}

type authRoundTrip func(*http.Request) (*http.Response, error)

func (f authRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOAuthDiscoveryAndIssuerAdvertisement(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprint(required), func(t *testing.T) {
			f, c, transport := oauthFixture(t)
			f.s.protocol = &SDKProtocol{Transport: authRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/mcp" {
					return &http.Response{StatusCode: 401, Header: http.Header{"Www-Authenticate": {`Bearer resource_metadata="https://fixture.example/custom-metadata", scope="read"`}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if r.URL.Path == "/.well-known/oauth-authorization-server" {
					return nil, ErrUnavailable
				}
				if r.URL.Path == "/custom-metadata" {
					clone := r.Clone(r.Context())
					clone.URL.Path = "/.well-known/oauth-protected-resource/mcp"
					return transport.RoundTrip(clone)
				}
				if r.URL.Path == "/.well-known/openid-configuration" {
					body := fmt.Sprintf(`{"issuer":"https://auth.fixture.example","authorization_endpoint":"https://auth.fixture.example/authorize","token_endpoint":"https://auth.fixture.example/token","code_challenge_methods_supported":["S256"],"authorization_response_iss_parameter_supported":%t}`, required)
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}
				return transport.RoundTrip(r)
			})}
			target, e := f.s.BeginOAuth(context.Background(), f.sc, c.ID, OAuthStart{ClientID: "client", Issuer: "https://auth.fixture.example", Revision: c.Revision}, "https://iulita.example/api/mcp/oauth/callback", "cookie")
			if e != nil {
				t.Fatal(e)
			}
			u, _ := url.Parse(target)
			if u.Query().Get("scope") != "read" {
				t.Fatal("lost challenge scopes")
			}
			e = f.s.FinishOAuth(context.Background(), u.Query().Get("state"), "code", "", "cookie")
			if required {
				if !errors.Is(e, ErrDenied) || transport.exchanges.Load() != 0 {
					t.Fatal(e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestOAuthWorkIsCancelledAndDrainedOnShutdown(t *testing.T) {
	f, c, _ := oauthFixture(t)
	entered := make(chan struct{})
	var once sync.Once
	f.s.protocol = &SDKProtocol{Transport: authRoundTrip(func(r *http.Request) (*http.Response, error) {
		once.Do(func() { close(entered) })
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	done := make(chan error, 1)
	go func() {
		_, e := f.s.BeginOAuth(context.Background(), f.sc, c.ID, OAuthStart{ClientID: "client", Issuer: "https://auth.fixture.example", Revision: c.Revision}, "https://iulita.example/callback", "cookie")
		done <- e
	}()
	<-entered
	if e := f.s.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("authorization survived shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("OAuth did not return after shutdown")
	}
}

func TestDisconnectCancelsPendingOAuthRefresh(t *testing.T) {
	f, c, _ := oauthFixture(t)
	f.s.mu.Lock()
	conn := f.s.connections[c.ID]
	conn.OAuth = &OAuthBinding{Issuer: "https://auth.fixture.example", ClientID: "client", Resource: c.Endpoint, TokenEndpoint: "https://auth.fixture.example/token", RefreshToken: "fixture", Expiry: time.Now().Add(-time.Second)}
	conn.AuthMode = "oauth"
	conn.AuthState = "valid"
	f.s.mu.Unlock()
	entered := make(chan struct{})
	f.s.protocol = &SDKProtocol{Transport: authRoundTrip(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	done := make(chan error, 1)
	go func() { done <- f.s.RefreshAuth(context.Background(), f.sc, c.ID) }()
	<-entered
	current, _ := f.s.Connection(context.Background(), f.sc, c.ID)
	if _, e := f.s.Action(context.Background(), f.sc, c.ID, "disconnect", current.Revision); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel refresh")
	}
}
