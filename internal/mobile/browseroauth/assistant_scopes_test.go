package browseroauth

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

func TestAssistantScopesNeedNewRegistrationAndExplicitBrowserConsent(t *testing.T) {
	f := setup(t)
	allowed := []string{"chats:read", "chats:write", "assistant:services:read", "assistant:services:write"}
	server, err := New(Config{Store: f.store, Accounts: PasswordAccounts{Users: f.users}, Scopes: allowed})
	if err != nil {
		t.Fatal(err)
	}
	f.s = server
	escalated := f.a
	escalated.Scope += " assistant:services:read"
	if reply := f.request(authURL(escalated), nil, nil); reply.Code != 400 {
		t.Fatal("old native registration gained services")
	}
	if err = f.store.RegisterOAuthClient(testContext, oauthstore.ClientRegistration{ClientID: "assistant-native", Name: "Account assistant", Kind: "native", Scopes: allowed, RedirectURIs: []string{testRedirect}}, ""); err != nil {
		t.Fatal(err)
	}
	a := f.a
	a.ClientID = "assistant-native"
	a.Scope = "chats:read chats:write assistant:services:read"
	response := f.request(authURL(a), nil, nil)
	form := fields(t, response)
	cookie := response.Result().Cookies()[0]
	form.Set("username", "owner")
	form.Set("password", "correct horse fixture")
	response = f.request("/oauth2/login", form, cookie)
	if !strings.Contains(response.Body.String(), "Читать данные подключённых сервисов аккаунта") || strings.Contains(response.Body.String(), "Изменять данные подключённых сервисов после подтверждения") {
		t.Fatal("consent did not disclose exact requested rights")
	}
	form = fields(t, response)
	cookie = response.Result().Cookies()[0]
	form.Set("decision", "approve")
	response = f.request("/oauth2/consent", form, cookie)
	if response.Code != 303 {
		t.Fatal("new scope consent failed", response.Code)
	}
	var raw []byte
	if err = f.db.QueryRow(`SELECT consent_scopes FROM oauth_families WHERE client_id=?`, a.ClientID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var granted []string
	if err = json.Unmarshal(raw, &granted); err != nil {
		t.Fatal(err)
	}
	if strings.Join(granted, " ") != a.Scope {
		t.Fatal("family grant widened beyond approved scope subset")
	}
}
