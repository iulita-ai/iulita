package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	if e = f.s.RefreshAuth(context.Background(), f.sc, c.ID); e != nil {
		t.Fatal(e)
	}
	v, _ := f.s.Connection(context.Background(), f.sc, c.ID)
	if v.Desired != "disabled" || v.AuthState != "valid" {
		t.Fatal(v)
	}
}
