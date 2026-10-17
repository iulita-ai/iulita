package assistanttools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/skill"
	slackskill "github.com/iulita-ai/iulita/internal/skill/slack"
	legacy "github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func slackBindingFixture(t *testing.T, transport http.RoundTripper) (*legacy.Store, string, engine.Catalog, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := legacy.New(filepath.Join(t.TempDir(), "slack.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err = store.CreateUser(ctx, &domain.User{ID: owner, Username: "slack-owner", PasswordHash: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveSlackAccount(ctx, &domain.SlackAccount{UserID: owner, SlackUserID: "U_ORIGINAL", TeamID: "T_ORIGINAL", Scopes: `["search:read"]`, EncryptedAccessToken: "old"}); err != nil {
		t.Fatal(err)
	}
	r := skill.NewRegistry()
	r.AddCapability("slack_user")
	c := slackskill.NewClient(slackskill.ClientOptions{Store: store, HTTPClient: &http.Client{Transport: transport}, Logger: zap.NewNop()})
	r.Register(slackskill.NewSearchSkill(c, store, zap.NewNop()))
	a := engine.Authority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString(), Scopes: []string{"chats:write", "assistant:services:read"}}
	return store, owner, Catalog(r)(&engine.Engine{}), engine.WithAuthority(ctx, a)
}

func TestSlackReconnectInvalidatesQueuedNativeConnection(t *testing.T) {
	for _, change := range []string{"team", "user", "same-principal-reauthorization"} {
		t.Run(change, func(t *testing.T) {
			var requests atomic.Int32
			store, owner, catalog, ctx := slackBindingFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, io.ErrUnexpectedEOF
			}))
			tools, err := catalog(ctx, owner)
			if err != nil || len(tools) != 1 {
				t.Fatal("initial connection", len(tools), err)
			}
			a, err := store.GetAnySlackAccount(ctx)
			if err != nil {
				t.Fatal(err)
			}
			a.ID = 0
			a.CreatedAt = time.Time{}
			a.UpdatedAt = time.Time{}
			a.EncryptedAccessToken = "new"
			if change == "team" {
				a.TeamID = "T_DIFFERENT"
			}
			if change == "user" {
				a.SlackUserID = "U_DIFFERENT"
			}
			if err = store.DeleteSlackAccount(ctx, owner); err != nil {
				t.Fatal(err)
			}
			if err = store.SaveSlackAccount(ctx, a); err != nil {
				t.Fatal(err)
			}
			fresh, err := catalog(ctx, owner)
			if err != nil || len(fresh) != 1 || fresh[0].Revision == tools[0].Revision {
				t.Fatal("reconnected native binding unchanged", err)
			}
			out, err := tools[0].Execute(ctx, conversations.Fence{OwnerID: owner}, json.RawMessage(`{"mode":"search","query":"fixture"}`))
			if err == nil && !strings.Contains(out, "lookup_failed") {
				t.Fatal("queued read used replacement connection", out)
			}
			if requests.Load() != 0 {
				t.Fatal("replacement dispatched old admitted request")
			}
		})
	}
}

func TestSlackRoutineTokenRotationKeepsNativeConnection(t *testing.T) {
	var requests atomic.Int32
	store, owner, catalog, ctx := slackBindingFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("token") != "rotated-fixture" {
			t.Error("routine rotation did not use refreshed same-connection token")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"messages":{"matches":[],"pagination":{"total_count":0}}}`)), Request: r}, nil
	}))
	tools, err := catalog(ctx, owner)
	if err != nil || len(tools) != 1 {
		t.Fatal(err)
	}
	a, err := store.GetAnySlackAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateSlackTokens(ctx, a.ID, "rotated-fixture", "rotated-refresh-fixture", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	fresh, err := catalog(ctx, owner)
	if err != nil || len(fresh) != 1 || fresh[0].Revision != tools[0].Revision {
		t.Fatal("routine refresh changed connection identity", err)
	}
	_, err = tools[0].Execute(ctx, conversations.Fence{OwnerID: owner}, json.RawMessage(`{"mode":"search","query":"fixture"}`))
	if err != nil || requests.Load() != 1 {
		t.Fatal("routine refresh blocked pinned request", err, requests.Load())
	}
}

func TestSlackReconnectCancelsInFlightNativeRead(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	store, owner, catalog, ctx := slackBindingFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
		return nil, r.Context().Err()
	}))
	tools, err := catalog(ctx, owner)
	if err != nil || len(tools) != 1 {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = tools[0].Execute(ctx, conversations.Fence{OwnerID: owner}, json.RawMessage(`{"mode":"search","query":"fixture"}`))
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	if err = store.DeleteSlackAccount(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveSlackAccount(ctx, &domain.SlackAccount{UserID: owner, SlackUserID: "U_NEW", TeamID: "T_NEW", Scopes: `["search:read"]`, EncryptedAccessToken: "other"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("same-owner workspace replacement left old request active")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled native read did not finish")
	}
}
