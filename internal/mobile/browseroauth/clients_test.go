package browseroauth

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

func (f *fixture) serviceRequest(path string, form url.Values, id, secret string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", testOrigin+path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(id, secret)
	response := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(response, request)
	return response
}
func TestServiceCredentialsIsolationRevocationAndRotation(t *testing.T) {
	f := setup(t)
	secret, _ := oauthstore.Secret()
	if err := f.store.RegisterOAuthClient(testContext, oauthstore.ClientRegistration{ClientID: "batch", Name: "Batch reader", Kind: "service", OwnerID: f.user.ID}, secret); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	response := f.serviceRequest("/oauth2/token", form, "batch", secret)
	var reply oauthstore.TokenReply
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &reply) != nil || reply.AccessToken == "" || reply.RefreshToken != "" || reply.Scope != "chats:read" || reply.ExpiresIn != 300 {
		t.Fatalf("service token: %d %s", response.Code, response.Body.String())
	}
	claims, err := f.store.ValidateAccess(testContext, reply.AccessToken)
	if err != nil || claims.Subject != f.user.ID || claims.PrincipalType != "service" || claims.DeviceID != "" || claims.FamilyID != "" || claims.AuthTime != 0 {
		t.Fatal("service profile", err)
	}
	authority := f.store.Authority(claims)
	if err = f.store.ValidateAuthority(testContext, authority); err != nil {
		t.Fatal("service authority", err)
	}
	form.Set("scope", "chats:write")
	if response = f.serviceRequest("/oauth2/token", form, "batch", secret); response.Code != 400 {
		t.Fatal("scope escalation", response.Code)
	}
	form.Del("scope")
	wrong, _ := oauthstore.Secret()
	if response = f.serviceRequest("/oauth2/token", form, "batch", wrong); response.Code != 401 {
		t.Fatal("wrong secret", response.Code)
	}
	form.Set("client_id", "batch")
	if response = f.serviceRequest("/oauth2/token", form, "batch", secret); response.Code != 400 {
		t.Fatal("mixed authentication accepted")
	}
	form.Del("client_id")
	if response = f.request("/oauth2/revoke", url.Values{"client_id": {"batch"}, "token": {reply.AccessToken}}, nil); response.Code != 401 {
		t.Fatal("public revoke response")
	}
	if _, err = f.store.ValidateAccess(testContext, reply.AccessToken); err != nil {
		t.Fatal("unauthenticated service revoke", err)
	}
	if response = f.serviceRequest("/oauth2/revoke", url.Values{"token": {reply.AccessToken}}, "batch", secret); response.Code != 200 {
		t.Fatal("authenticated revoke")
	}
	if _, err = f.store.ValidateAccess(testContext, reply.AccessToken); err == nil {
		t.Fatal("revoked access live")
	}
	if err = f.store.ValidateAuthority(testContext, authority); err == nil {
		t.Fatal("async authority retained after revoke")
	}
	response = f.serviceRequest("/oauth2/token", form, "batch", secret)
	json.Unmarshal(response.Body.Bytes(), &reply)
	nextSecret, _ := oauthstore.Secret()
	if err = f.store.ChangeOAuthClient(testContext, "batch", nextSecret, false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ValidateAccess(testContext, reply.AccessToken); err == nil {
		t.Fatal("rotation retained old token")
	}
	if response = f.serviceRequest("/oauth2/token", form, "batch", secret); response.Code != 401 {
		t.Fatal("old secret live")
	}
	if response = f.serviceRequest("/oauth2/token", form, "batch", nextSecret); response.Code != 200 {
		t.Fatal("rotated secret unusable", response.Body.String())
	}
	if err = f.store.ChangeOAuthClient(testContext, "batch", "", true); err != nil {
		t.Fatal(err)
	}
	if response = f.serviceRequest("/oauth2/token", form, "batch", nextSecret); response.Code != 401 {
		t.Fatal("disabled client issued token")
	}
	var rawCount int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM oauth_client_policy WHERE CAST(secret_hash AS TEXT) LIKE ?`, "%"+secret+"%").Scan(&rawCount); err != nil || rawCount != 0 {
		t.Fatal("raw secret persisted", err)
	}
}
func startCLI(t *testing.T, f *fixture, key oauthstore.InstallationJWK) oauthstore.DeviceAuthorization {
	t.Helper()
	if err := f.store.RegisterOAuthClient(testContext, oauthstore.ClientRegistration{ClientID: "cli", Name: "Personal console", Kind: "cli"}, ""); err != nil {
		t.Fatal(err)
	}
	response := f.request("/oauth2/device_authorization", url.Values{"client_id": {"cli"}, "iulita_installation_key_thumbprint": {key.Thumbprint()}}, nil)
	var device oauthstore.DeviceAuthorization
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &device) != nil || device.UserCode == "" || device.Interval != 5 || device.ExpiresIn != 600 {
		t.Fatalf("device start %d %s", response.Code, response.Body.String())
	}
	return device
}
func approveCLI(t *testing.T, f *fixture, device oauthstore.DeviceAuthorization, decision string) {
	t.Helper()
	response := f.request("/oauth2/device?user_code="+url.QueryEscape(device.UserCode), nil, nil)
	form := fields(t, response)
	cookie := response.Result().Cookies()[0]
	form.Set("username", "owner")
	form.Set("password", "correct horse fixture")
	response = f.request("/oauth2/login", form, cookie)
	form = fields(t, response)
	cookie = response.Result().Cookies()[0]
	if !strings.Contains(response.Body.String(), "Personal console") || !strings.Contains(response.Body.String(), `name="user_code"`) {
		t.Fatal("missing client identity/code confirmation")
	}
	form.Set("decision", decision)
	form.Set("user_code", "WRNG-WRNG")
	if response = f.request("/oauth2/consent", form, cookie); response.Code != 400 {
		t.Fatal("incorrect confirmation accepted")
	}
	form.Set("user_code", device.UserCode)
	if response = f.request("/oauth2/consent", form, cookie); response.Code != 200 {
		t.Fatalf("device consent %d %s", response.Code, response.Body.String())
	}
	if response = f.request("/oauth2/consent", form, cookie); response.Code != 400 {
		t.Fatal("consent replay accepted")
	}
}
func devicePoll(device oauthstore.DeviceAuthorization, key oauthstore.InstallationJWK) url.Values {
	return url.Values{"grant_type": {oauthstore.DeviceGrant}, "client_id": {"cli"}, "device_code": {device.DeviceCode}, "iulita_installation_jwk": {key.Canonical()}}
}
func expectOAuthError(t *testing.T, response *httptest.ResponseRecorder, code string) {
	t.Helper()
	var body map[string]string
	if response.Code != 400 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body["error"] != code {
		t.Fatalf("wanted %s, got %d %s", code, response.Code, response.Body.String())
	}
}
func TestDeviceFlowBrowserProofRefreshAndReplay(t *testing.T) {
	f := setup(t)
	key, jwk := installation(t)
	device := startCLI(t, f, jwk)
	poll := devicePoll(device, jwk)
	expectOAuthError(t, f.request("/oauth2/token", poll, nil), "authorization_pending")
	expectOAuthError(t, f.request("/oauth2/token", poll, nil), "slow_down")
	var interval int
	if err := f.db.QueryRow(`SELECT interval FROM oauth_device_requests`).Scan(&interval); err != nil || interval != 10 {
		t.Fatal("slow_down not persistent", err)
	}
	_, other := installation(t)
	wrong := devicePoll(device, other)
	expectOAuthError(t, f.request("/oauth2/token", wrong, nil), "invalid_grant")
	approveCLI(t, f, device, "approve")
	// Advance only the persisted poll timestamp, never production deadlines.
	f.db.Exec(`UPDATE oauth_device_requests SET last_poll=0`)
	pending := f.exchange(t, poll)
	if pending.Stage != oauthstore.Pending || pending.Scope != "" || pending.Challenge == nil {
		t.Fatal("pending stage bypass")
	}
	expectOAuthError(t, f.request("/oauth2/token", poll, nil), "invalid_grant")
	claims, err := f.store.ValidateAccess(testContext, pending.Access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.FinishInstallation(testContext, claims, pending.Challenge.OperationID, pending.Challenge.Nonce, proof(t, key, pending.Challenge), &jwk, "Console"); err != nil {
		t.Fatal(err)
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {"cli"}, "refresh_token": {pending.Refresh}}
	active := f.exchange(t, refresh)
	if active.Stage != oauthstore.Active || active.Scope != "chats:read chats:write" {
		t.Fatal("CLI grants", active.Scope)
	}
	activeClaims, err := f.store.ValidateAccess(testContext, active.Access)
	if err != nil || activeClaims.ClientID != "cli" {
		t.Fatal(err)
	}
	expectOAuthError(t, f.request("/oauth2/token", refresh, nil), "invalid_grant")
	if _, err = f.store.ValidateAccess(testContext, active.Access); err == nil {
		t.Fatal("refresh replay failed to revoke CLI family")
	}
	var leaks int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM oauth_device_requests WHERE CAST(code_hash AS TEXT) LIKE ? OR CAST(user_hash AS TEXT) LIKE ?`, "%"+device.DeviceCode+"%", "%"+device.UserCode+"%").Scan(&leaks); err != nil || leaks != 0 {
		t.Fatal("raw device credentials persisted")
	}
}
func TestDeviceFlowDeniedExpiredAndClientPolicy(t *testing.T) {
	for _, state := range []string{"denied", "expired", "disabled"} {
		t.Run(state, func(t *testing.T) {
			f := setup(t)
			_, jwk := installation(t)
			device := startCLI(t, f, jwk)
			switch state {
			case "denied":
				approveCLI(t, f, device, "deny")
			case "expired":
				f.db.Exec(`UPDATE oauth_device_requests SET expires_at=1`)
			case "disabled":
				f.store.ChangeOAuthClient(testContext, "cli", "", true)
			}
			expected := map[string]string{"denied": "access_denied", "expired": "expired_token", "disabled": "unauthorized_client"}[state]
			expectOAuthError(t, f.request("/oauth2/token", devicePoll(device, jwk), nil), expected)
		})
	}
	f := setup(t)
	_, jwk := installation(t)
	if err := f.store.RegisterOAuthClient(testContext, oauthstore.ClientRegistration{ClientID: "bad-cli", Name: "bad", Kind: "cli", Scopes: []string{"devices:receive"}}, ""); err == nil {
		t.Fatal("CLI native receiver scope accepted")
	}
	expectOAuthError(t, f.request("/oauth2/device_authorization", url.Values{"client_id": {"mobile"}, "iulita_installation_key_thumbprint": {jwk.Thumbprint()}}, nil), "unauthorized_client")
}
