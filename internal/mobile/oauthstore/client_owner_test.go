package oauthstore

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNativeClientOptionalOwnerPolicyAndBrowserFences(t *testing.T) {
	f := fixture(t, Active)
	registration := ClientRegistration{ClientID: "admin-native", Name: "Admin native", Kind: "native", OwnerID: f.owner, Scopes: []string{"chats:read", "assistant:services:read", "assistant:services:write"}, RedirectURIs: []string{redirect}}
	if err := f.RegisterOAuthClient(ctx, registration, ""); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"not-an-id", uuid.Nil.String()} {
		r := registration
		r.ClientID = "bad-owner"
		r.OwnerID = bad
		if _, err := normalizeRegistration(r); err == nil {
			t.Fatal("noncanonical native owner accepted", bad)
		}
	}
	r := registration
	r.Kind = "cli"
	r.RedirectURIs = nil
	if _, err := normalizeRegistration(r); err == nil {
		t.Fatal("CLI owner semantics expanded")
	}
	newFlow := func() (BrowserFlow, string, string) {
		cookie, _ := Secret()
		challenge, _ := Secret()
		flow, csrf, err := f.CreateBrowserFlow(ctx, cookie, Authorization{ClientID: registration.ClientID, RedirectURI: redirect, Scope: "chats:read assistant:services:read", State: "long-random-state", Challenge: challenge, KeyThumbprint: challenge})
		if err != nil {
			t.Fatal(err)
		}
		return flow, cookie, csrf
	}
	stamp := sha256.Sum256([]byte("synthetic credential stamp"))
	flow, cookie, csrf := newFlow()
	if _, _, _, err := f.LoginBrowserFlow(ctx, cookie, flow.ID, csrf, f.other, stamp[:], time.Now()); err == nil {
		t.Fatal("foreign owner logged into restricted native client")
	}
	var phase string
	if err := f.db.QueryRow(`SELECT phase FROM oauth_browser_flows WHERE id=?`, flow.ID).Scan(&phase); err != nil || phase != "login" {
		t.Fatal("rejected login changed flow", phase, err)
	}
	flow, cookie, csrf = newFlow()
	_, cookie, csrf, err := f.LoginBrowserFlow(ctx, cookie, flow.ID, csrf, f.owner, stamp[:], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE oauth_browser_flows SET owner_id=? WHERE id=?`, f.other, flow.ID); err != nil {
		t.Fatal(err)
	}
	invoked := false
	err = f.AuthorizeBrowserFlow(ctx, cookie, flow.ID, csrf, stamp[:], func(context.Context, BrowserFlow) error { invoked = true; return nil })
	if err == nil || invoked {
		t.Fatal("foreign retained consent reached issuance", err)
	}
	err = f.WithRequest(ctx, func(c context.Context) error {
		secret, _ := Secret()
		_, prepareErr := f.PrepareAuthorization(c, BrowserFlow{Phase: "consent", OwnerID: f.other, AuthTime: time.Now(), Request: Authorization{ClientID: registration.ClientID, Scope: "chats:read", KeyThumbprint: secret}})
		return prepareErr
	})
	if err == nil {
		t.Fatal("direct foreign authorization prepared")
	}
	own := f.family
	own.ID = uuid.NewString()
	own.ClientID = registration.ClientID
	if err = f.CreateFamily(ctx, own); err != nil {
		t.Fatal("owned family rejected", err)
	}
	foreign := own
	foreign.ID = uuid.NewString()
	foreign.OwnerID = f.other
	foreign.DeviceID = uuid.NewString()
	secret, _ := Secret()
	if err = f.RegisterDevice(ctx, f.other, foreign.DeviceID, secret); err != nil {
		t.Fatal(err)
	}
	if err = f.CreateFamily(ctx, foreign); err == nil {
		t.Fatal("foreign family created")
	}
	policy, err := f.ClientPolicy(ctx, "mobile")
	if err != nil || policy.OwnerID != "" || len(policy.Scopes) != 1 {
		t.Fatal("primary policy changed", policy, err)
	}
}
