package browseroauth

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

func TestTokenRejectsUntrustedJOSEInputsBeforeFramework(t *testing.T) {
	// There is deliberately no database/provider: these unsupported inputs must
	// be rejected before Fosite can parse attacker-controlled JOSE/JWK data.
	for _, key := range []string{"client_assertion", "client_assertion_type", "assertion", "request", "request_uri", "jwks", "jwks_uri"} {
		t.Run(key, func(t *testing.T) {
			form := url.Values{"grant_type": {"authorization_code"}, "client_id": {"mobile"}, key: {"untrusted.jwt.payload"}}
			request := httptest.NewRequest("POST", testOrigin+"/oauth2/token", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			new(Server).token(response, request)
			expectOAuthError(t, response, "invalid_request")
		})
	}
}

func TestInstallationRejectsPrivateRSAJWK(t *testing.T) {
	// Small dummy parameters exercise the boundary without an expensive DoS PoC.
	if _, err := oauthstore.ParseInstallation([]byte(`{"kty":"RSA","n":"AQAB","e":"AQAB","d":"Aw","p":"Aw","q":"Aw"}`)); err == nil {
		t.Fatal("installation accepted private RSA JWK")
	}
}
