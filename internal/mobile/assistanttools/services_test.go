package assistanttools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/craft"
	"github.com/iulita-ai/iulita/internal/skill/todoist"
	"go.uber.org/zap"
)

func TestConfiguredServicesRequireOwnerScopesAndPinnedCredentials(t *testing.T) {
	t.Setenv("IULITA_TODOIST_TOKEN", "wrong-global-fixture")
	owner, other := uuid.NewString(), uuid.NewString()
	var requests atomic.Int32
	api := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer owned-fixture" {
			t.Error("substituted global credential")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"items":[],"results":[]}`))}, nil
	})}
	cc := craft.NewClient("https://craft.fixture.test", "owned-fixture", api)
	tc := todoist.NewClient("owned-fixture", api, zap.NewNop())
	r := skill.NewRegistry()
	r.SetCapabilities([]string{"craft", "todoist"})
	r.Register(craft.NewSearch(cc))
	r.Register(craft.NewWrite(cc))
	r.Register(todoist.NewSkill(tc, zap.NewNop()))
	catalog := Catalog(r)(&engine.Engine{})
	a := engine.Authority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString(), Scopes: []string{"chats:write", "assistant:services:read"}}
	ctx := engine.WithAuthority(context.Background(), a)
	tools, err := catalog(ctx, owner)
	if err != nil || len(tools) != 0 {
		t.Fatal("unassigned global connections exposed", len(tools), err)
	}
	cc.SetNativeOwner(owner)
	tc.SetNativeOwner(owner)
	tools, err = catalog(ctx, owner)
	if err != nil || len(tools) != 2 {
		t.Fatal("read scopes", len(tools), err)
	}
	for _, tool := range tools {
		if tool.Definition.Name == "craft_write" {
			t.Fatal("read token can write")
		}
		if tool.Definition.Name == "todoist" && tool.Validate(json.RawMessage(`{"action":"create","content":"forbidden"}`)) {
			t.Fatal("mutation advertised to read token")
		}
	}
	a.OwnerID = other
	foreign := engine.WithAuthority(context.Background(), a)
	tools, err = catalog(foreign, other)
	if err != nil || len(tools) != 0 {
		t.Fatal("foreign global service access", len(tools), err)
	}
	a.OwnerID = owner
	a.Scopes = append(a.Scopes, "assistant:services:write")
	ctx = engine.WithAuthority(context.Background(), a)
	tools, err = catalog(ctx, owner)
	if err != nil || len(tools) != 3 {
		t.Fatal(err, len(tools))
	}
	for _, tool := range tools {
		if tool.Definition.Name == "todoist" {
			if !tool.Validate(json.RawMessage(`{"action":"create","content":"fixture","labels":["one","two"]}`)) {
				t.Fatal("typed string array rejected")
			}
			tc.UpdateToken("rotated-fixture")
			out, err := tool.Execute(ctx, conversations.Fence{OwnerID: owner}, json.RawMessage(`{"action":"projects"}`))
			if err == nil && !strings.Contains(out, "lookup_failed") {
				t.Fatal("old accepted binding used rotated credential", out)
			}
		}
	}
	if requests.Load() != 0 {
		t.Fatal("rotation dispatched a queued operation")
	}
}

func TestServiceDisconnectCancelsInFlightNativeLookup(t *testing.T) {
	owner := uuid.NewString()
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	api := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
		return nil, r.Context().Err()
	})}
	cc := craft.NewClient("https://craft.fixture.test", "owned-fixture", api)
	cc.SetNativeOwner(owner)
	r := skill.NewRegistry()
	r.AddCapability("craft")
	r.Register(craft.NewSearch(cc))
	ctx := engine.WithAuthority(context.Background(), engine.Authority{OwnerID: owner, Scopes: []string{"chats:write", "assistant:services:read"}})
	tools, err := Catalog(r)(&engine.Engine{})(ctx, owner)
	if err != nil || len(tools) != 1 {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = tools[0].Execute(ctx, conversations.Fence{OwnerID: owner}, json.RawMessage(`{"query":"fixture"}`))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cc.SetNativeOwner("")
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disconnected lookup remained active")
	}
	<-done
}
