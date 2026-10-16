package browseroauth

import (
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"
)

// Explicit local interop fixture for the native verifier. All authority belongs
// to this test's temporary realm/account; never uses production credentials.
func TestWriteNativeInteropFixture(t *testing.T) {
	path := os.Getenv("IULITA_NATIVE_INTEROP_FIXTURE")
	if path == "" {
		t.Skip("explicit fixture output required")
	}
	f := setup(t)
	key, j := installation(t)
	a := f.a
	a.KeyThumbprint = j.Thumbprint()
	v, c := f.login(t, a)
	authorization := f.request("/oauth2/consent", v, c)
	if authorization.Code != 303 {
		t.Fatal("fixture authorization failed")
	}
	callback, err := url.Parse(authorization.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	w := f.request("/oauth2/token", codeExchange(callback.Query().Get("code"), j), nil)
	if w.Code != 200 {
		t.Fatal("fixture token failed")
	}
	var reply map[string]any
	if json.Unmarshal(w.Body.Bytes(), &reply) != nil {
		t.Fatal("fixture JSON invalid")
	}
	var pending tokenReply
	if json.Unmarshal(w.Body.Bytes(), &pending) != nil {
		t.Fatal("pending fixture invalid")
	}
	claims, err := f.realm.VerifySignature(pending.Access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.FinishInstallation(testContext, claims, pending.Challenge.OperationID, pending.Challenge.Nonce, proof(t, key, pending.Challenge), &j, "Synthetic native fixture"); err != nil {
		t.Fatal(err)
	}
	activeW := f.request("/oauth2/token", refreshExchange(pending.Refresh), nil)
	if activeW.Code != 200 {
		t.Fatal("active fixture token failed")
	}
	var activeReply map[string]any
	if json.Unmarshal(activeW.Body.Bytes(), &activeReply) != nil {
		t.Fatal("active fixture invalid")
	}
	out := map[string]any{"issuer": testOrigin, "audience": "iulita-mobile", "client_id": "mobile", "redirect_uri": testRedirect, "state": a.State, "callback_parameters": callback.Query(), "token_reply": reply, "active_token_reply": activeReply, "jwks": f.realm.JWKS(), "installation_thumbprint": j.Thumbprint(), "now_ms": time.Now().UnixMilli()}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("Synthetic native interop fixture written; no credentials logged")
}
