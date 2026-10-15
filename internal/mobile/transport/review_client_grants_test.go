package transport

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/ory/fosite"
)

func TestNativeClientCannotUseOtherClientBootstrapGrants(t *testing.T) {
	h := setupHarness(t)
	for _, grant := range []string{"client_credentials", "urn:ietf:params:oauth:grant-type:device_code", "password"} {
		response := h.http("/oauth2/token", url.Values{"grant_type": {grant}, "client_id": {"mobile"}}, nil)
		var body map[string]string
		if response.Code != 400 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body["error"] != map[string]string{"client_credentials": "invalid_request", "urn:ietf:params:oauth:grant-type:device_code": "invalid_request", "password": "unsupported_grant_type"}[grant] {
			t.Fatal("unsupported grant advertised as usable", grant, response.Code)
		}
	}
	// A normal desktop loopback callback is not registered by current runtime.
	err := h.oauth.RegisterClient(context.Background(), fosite.DefaultClient{ID: "console", Public: true, RedirectURIs: []string{"http://127.0.0.1:12345/callback"}, GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scopes: []string{"chats:read", "chats:write"}, Audience: []string{"iulita-mobile"}})
	if err == nil {
		t.Fatal("loopback callback silently enabled")
	}
}
