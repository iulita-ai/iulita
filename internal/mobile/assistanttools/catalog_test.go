package assistanttools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/google"
	"github.com/iulita-ai/iulita/internal/skill/memory"
	legacy "github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogUsesOwnedConnectionsAndLiveRegistry(t *testing.T) {
	ctx := context.Background()
	store, err := legacy.New(filepath.Join(t.TempDir(), "main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	owner, other := uuid.NewString(), uuid.NewString()
	for i, id := range []string{owner, other} {
		if err = store.CreateUser(ctx, &domain.User{ID: id, Username: []string{"owner", "other"}[i], PasswordHash: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	account := &domain.GoogleAccount{UserID: owner, AccountEmail: "owner@example.test", AccountAlias: "personal", IsDefault: true, EncryptedAccessToken: "fixture", EncryptedRefreshToken: "fixture", TokenExpiry: time.Now().Add(time.Hour)}
	if err = store.SaveGoogleAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	r := skill.NewRegistry()
	r.SetCapabilities([]string{"google", "memory"})
	r.Register(google.NewMail(google.NewClient("id", "secret", "", store, nil, zap.NewNop())))
	r.Register(memory.NewRecall(store))
	lookup := Catalog(r)(&engine.Engine{})
	owned := engine.WithAuthority(ctx, engine.Authority{OwnerID: owner, Scopes: []string{"chats:write", "assistant:services:read"}})
	tools, err := lookup(owned, owner)
	if err != nil || len(tools) != 2 {
		t.Fatal("owned connection missing", len(tools), err)
	}
	// Exercise the real Google API client without a network or live account.
	// A global env token must never win over the owned DB credential.
	t.Setenv("IULITA_GOOGLE_TOKEN", "wrong-global-fixture")
	requests := 0
	api := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("Google credential identity substituted")
		}
		body := `{"messages":[{"id":"fixture-message"}]}`
		if strings.HasSuffix(request.URL.Path, "/fixture-message") {
			body = `{"id":"fixture-message","snippet":"owned account mail","payload":{"headers":[{"name":"Subject","value":"Native parity fixture"}]}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	for _, tool := range tools {
		if tool.Definition.Name == "google_mail" {
			out, callErr := tool.Execute(context.WithValue(owned, oauth2.HTTPClient, api), conversations.Fence{OwnerID: owner}, json.RawMessage(`{"action":"unread"}`))
			if callErr != nil || !strings.Contains(out, "Native parity fixture") || requests != 2 {
				t.Fatal("owned Google lookup failed", callErr, requests)
			}
		}
	}
	foreign := engine.WithAuthority(ctx, engine.Authority{OwnerID: other, Scopes: []string{"chats:write", "assistant:services:read"}})
	tools, err = lookup(foreign, other)
	if err != nil || len(tools) != 1 || tools[0].Definition.Name != "recall" {
		t.Fatal("foreign connection advertised", len(tools), err)
	}
	r.DisableSkill("google_mail")
	tools, err = lookup(owned, owner)
	if err != nil || len(tools) != 1 {
		t.Fatal("stale enablement", err)
	}
	r.EnableSkill("google_mail")
	if err = store.DeleteGoogleAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	tools, err = lookup(owned, owner)
	if err != nil || len(tools) != 1 {
		t.Fatal("revoked connection advertised", err)
	}
	service := engine.WithAuthority(ctx, engine.Authority{OwnerID: owner, PrincipalType: "service"})
	tools, err = lookup(service, owner)
	if err != nil || len(tools) != 0 {
		t.Fatal("service inherited saved memory", err)
	}
}
