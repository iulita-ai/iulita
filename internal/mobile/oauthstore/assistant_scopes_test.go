package oauthstore

import "testing"

func TestAccountServicesScopesStayOutsideCLIAndServiceCeilings(t *testing.T) {
	for _, scope := range []string{"assistant:services:read", "assistant:services:write"} {
		for _, kind := range []string{"cli", "service"} {
			r := ClientRegistration{ClientID: "test", Name: "test", Kind: kind, Scopes: []string{scope}}
			if kind == "service" {
				r.OwnerID = "11111111-1111-4111-8111-111111111111"
			}
			if _, err := normalizeRegistration(r); err == nil {
				t.Fatal("client ceiling expanded", kind, scope)
			}
		}
		if _, err := normalizeRegistration(ClientRegistration{ClientID: "native", Name: "test", Kind: "native", Scopes: []string{"chats:read", "chats:write", scope}, RedirectURIs: []string{"https://mobile.example.test/mobile/callback"}}); err != nil {
			t.Fatal("reviewed native scope rejected", err)
		}
	}
}
