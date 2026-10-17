package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

func TestOfflineClientOperationsAndExclusiveSecrets(t *testing.T) {
	settings, accounts, _, _ := runtimeFixture(t)
	ctx := context.Background()
	if err := Initialize(ctx, settings); err != nil {
		t.Fatal(err)
	}
	owner, err := accounts.GetUserByUsername(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	registration := oauthstore.ClientRegistration{ClientID: "job", Name: "Personal job", Kind: "service", OwnerID: owner.ID}
	secretPath := filepath.Join(t.TempDir(), "client-secret")
	operation := ClientOperation{Action: "register", Registration: registration, SecretPath: secretPath}
	if _, err = ManageClients(ctx, settings, operation); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(secretPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("secret permissions", err)
	}
	secret, err := os.ReadFile(secretPath)
	if err != nil || !oauthstore.ValidSecret(strings.TrimSpace(string(secret))) {
		t.Fatal("secret file", err)
	}
	if _, err = ManageClients(ctx, settings, operation); err == nil {
		t.Fatal("secret overwritten")
	}
	unchanged, _ := os.ReadFile(secretPath)
	if !bytes.Equal(unchanged, secret) {
		t.Fatal("secret changed after failed operation")
	}
	clients, err := ManageClients(ctx, settings, ClientOperation{Action: "list"})
	if err != nil || len(clients) != 2 {
		t.Fatal("list", clients, err)
	}
	for _, client := range clients {
		if client.ClientID == "job" && (client.Scopes[0] != "chats:read" || client.PrincipalID == "") {
			t.Fatal("service policy", client)
		}
	}
	newPath := filepath.Join(t.TempDir(), "rotated")
	if _, err = ManageClients(ctx, settings, ClientOperation{Action: "rotate-secret", ClientID: "job", SecretPath: newPath}); err != nil {
		t.Fatal(err)
	}
	if _, err = ManageClients(ctx, settings, ClientOperation{Action: "disable", ClientID: "job"}); err != nil {
		t.Fatal(err)
	}
	lock, err := oauthstore.LockRealm(settings.RealmDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ManageClients(ctx, settings, ClientOperation{Action: "list"}); err == nil {
		t.Fatal("online realm mutation allowed")
	}
	lock.Close()
	if _, err = ManageClients(ctx, settings, ClientOperation{Action: "disable", ClientID: settings.ClientID}); err == nil {
		t.Fatal("primary native disabled")
	}
	registration.ClientID = "missing-owner"
	registration.OwnerID = "00000000-0000-4000-8000-000000000001"
	missingPath := filepath.Join(t.TempDir(), "must-not-exist")
	if _, err = ManageClients(ctx, settings, ClientOperation{Action: "register", Registration: registration, SecretPath: missingPath}); err == nil {
		t.Fatal("nonexistent service owner")
	}
	if _, err = os.Stat(missingPath); !os.IsNotExist(err) {
		t.Fatal("failed owner created secret")
	}
}

func TestAsyncServiceAuthorityRefreshesAccountWithoutAnotherRPC(t *testing.T) {
	settings, users, _, _ := runtimeFixture(t)
	ctx := context.Background()
	if err := Initialize(ctx, settings); err != nil {
		t.Fatal(err)
	}
	realm, err := oauthstore.LoadRealm(settings.RealmDir, settings.Issuer, settings.Audience)
	if err != nil {
		t.Fatal(err)
	}
	store, err := oauthstore.Open(ctx, settings.OAuthDB, realm)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accounts := browseroauth.PasswordAccounts{Users: users}
	owner, err := users.GetUserByUsername(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	account, err := accounts.Lookup(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := oauthstore.Secret()
	if err = store.RegisterOAuthClient(ctx, oauthstore.ClientRegistration{ClientID: "job", Name: "job", Kind: "service", OwnerID: owner.ID}, secret); err != nil {
		t.Fatal(err)
	}
	if err = store.SyncAccount(ctx, owner.ID, account.CredentialVersion, true); err != nil {
		t.Fatal(err)
	}
	token, err := store.IssueServiceToken(ctx, "job", secret, "")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := store.ValidateAccess(ctx, token.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	authority := store.Authority(claims)
	if err = verifyExecutionAuthority(ctx, accounts, store, authority); err != nil {
		t.Fatal(err)
	}
	owner.SecurityEpoch++
	if err = users.UpdateUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err = verifyExecutionAuthority(ctx, accounts, store, authority); err == nil {
		t.Fatal("late service completion retained stale account epoch")
	}
}

func TestOfflineNativeOwnerRegistrationKeepsPrimaryUnchanged(t *testing.T) {
	settings, accounts, _, _ := runtimeFixture(t)
	ctx := context.Background()
	if err := Initialize(ctx, settings); err != nil {
		t.Fatal(err)
	}
	owner, err := accounts.GetUserByUsername(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	registration := oauthstore.ClientRegistration{ClientID: "admin-native", Name: "Admin native", Kind: "native", OwnerID: owner.ID, RedirectURIs: []string{settings.RedirectURI}, Scopes: append(append([]string(nil), scopes...), "assistant:services:read", "assistant:services:write")}
	if _, err = ManageClients(ctx, settings, ClientOperation{Action: "register", Registration: registration}); err != nil {
		t.Fatal(err)
	}
	registrations, err := ManageClients(ctx, settings, ClientOperation{Action: "list"})
	if err != nil || len(registrations) != 2 {
		t.Fatal(registrations, err)
	}
	for _, r := range registrations {
		if r.ClientID == settings.ClientID && (r.OwnerID != "" || len(r.Scopes) != 8) {
			t.Fatal("primary widened", r)
		}
		if r.ClientID == registration.ClientID && (r.OwnerID != owner.ID || len(r.Scopes) != 10) {
			t.Fatal("secondary policy", r)
		}
	}
	registration.ClientID = "missing-native-owner"
	registration.OwnerID = "00000000-0000-4000-8000-000000000001"
	if _, err = ManageClients(ctx, settings, ClientOperation{Action: "register", Registration: registration}); err == nil {
		t.Fatal("missing native owner registered")
	}
	owner.Disabled = true
	if err = accounts.UpdateUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	registration.ClientID = "disabled-native-owner"
	registration.OwnerID = owner.ID
	if _, err = ManageClients(ctx, settings, ClientOperation{Action: "register", Registration: registration}); err == nil {
		t.Fatal("disabled native owner registered")
	}
}
