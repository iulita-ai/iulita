package runtime

import (
	"context"
	"reflect"
	"testing"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

func TestAssistantScopesRequireExplicitRegistrationAndDoNotWidenClients(t *testing.T) {
	s, _, _, _ := runtimeFixture(t)
	ctx := context.Background()
	if err := Initialize(ctx, s); err != nil {
		t.Fatal(err)
	}
	before := registered(s)
	s.AssistantScopes = []string{"assistant:services:read", "assistant:services:write"}
	if err := Initialize(ctx, s); err == nil {
		t.Fatal("existing primary registration widened silently")
	}
	realm, err := oauthstore.LoadRealm(s.RealmDir, s.Issuer, s.Audience)
	if err != nil {
		t.Fatal(err)
	}
	store, err := oauthstore.Open(ctx, s.OAuthDB, realm)
	if err != nil {
		t.Fatal(err)
	}
	client, err := store.GetClient(ctx, s.ClientID)
	if err != nil || !reflect.DeepEqual(client.GetScopes(), before.GetScopes()) {
		t.Fatal("failed opt-in mutated client", err)
	}
	store.Close()
	s.ClientID = "native-assistant-explicit"
	if err = Initialize(ctx, s); err != nil {
		t.Fatal("explicit new registration", err)
	}
	for _, bad := range [][]string{{"phone:ui"}, {"assistant:services:read", "assistant:services:read"}} {
		s.AssistantScopes = bad
		if err = s.Validate(); err == nil {
			t.Fatal("invalid assistant scope selection")
		}
	}
}
