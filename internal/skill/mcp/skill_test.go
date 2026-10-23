package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/mcpclient"
	"github.com/iulita-ai/iulita/internal/skill/interact"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

type denialProtocol struct{}

func (denialProtocol) Discover(context.Context, mcpclient.Connection) ([]mcpclient.Item, error) {
	return []mcpclient.Item{{ID: "item", Kind: "tool", Name: "write", Revision: "hash", Descriptor: json.RawMessage(`{"inputSchema":{"type":"object"}}`)}}, nil
}
func (denialProtocol) Invoke(context.Context, mcpclient.Connection, mcpclient.Item, json.RawMessage, func() error) (payload json.RawMessage, toolError bool, callErr error) {
	return nil, false, errors.New("must not dispatch")
}

type denialConfirmer struct{ preview *string }

func (c denialConfirmer) Confirm(_ context.Context, request interact.Confirmation, actor interact.Actor) (interact.Decision, error) {
	if c.preview != nil {
		*c.preview = request.Preview
	}
	return interact.Decision{Actor: actor}, interact.ErrConfirmRejected
}

func newConfirmationService(t *testing.T) (*mcpclient.Service, mcpclient.Scope, mcpclient.Connection) {
	t.Helper()
	ctx := context.Background()
	db, e := sqlite.New(filepath.Join(t.TempDir(), "mcp.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	if e = db.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	crypt, _ := config.NewEncryptor(make([]byte, 32))
	vault := credential.NewStore(credential.NewStorageAdapter(db), crypt, zap.NewNop())
	service, e := mcpclient.New(mcpclient.Options{Repository: db, Vault: vault, Protocol: denialProtocol{}, Enabled: true, AllowedOrigins: []string{"https://fixture.example"}, LockPath: filepath.Join(t.TempDir(), "mcp.lock")})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = service.Close() })
	sc := mcpclient.Scope{OwnerID: "owner", CallerID: "dashboard", Private: true}
	c, e := service.Create(ctx, sc, mcpclient.Draft{Label: "Fixture", Domain: "personal", Endpoint: "https://fixture.example/mcp"})
	if e != nil {
		t.Fatal(e)
	}
	c, e = service.Action(ctx, sc, c.ID, "connect", c.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if e = service.Review(ctx, sc, c.ID, c.Revision, mcpclient.Review{ItemID: "item", Revision: "hash", Effect: "write", Allowed: true}); e != nil {
		t.Fatal(e)
	}
	c, e = service.Connection(ctx, sc, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	return service, sc, c
}

func TestExplicitChatDenialRemovesDashboardApproval(t *testing.T) {
	ctx := context.Background()
	service, sc, c := newConfirmationService(t)
	var e error
	chat := mcpclient.WithScope(ctx, mcpclient.Scope{OwnerID: "owner", CallerID: "main", ConversationID: "private", Private: true})
	chat = interact.WithConfirmer(chat, denialConfirmer{})
	input, _ := json.Marshal(map[string]any{"connection_id": c.ID, "item_id": "item", "descriptor_revision": "hash", "arguments": map[string]any{}})
	if _, e = New(service, "call_tool").Execute(chat, input); !errors.Is(e, mcpclient.ErrConfirmationRejected) {
		t.Fatal(e)
	}
	plans, e := service.Plans(ctx, sc)
	if e != nil || len(plans) != 0 {
		t.Fatal("refused request remained in dashboard", plans, e)
	}
	var preview string
	chat = interact.WithConfirmer(chat, denialConfirmer{preview: &preview})
	action, _ := json.Marshal(map[string]any{"connection_id": c.ID, "action": "check"})
	if _, e = New(service, "connection_action").Execute(chat, action); !errors.Is(e, mcpclient.ErrConfirmationRejected) {
		t.Fatal(e)
	}
	if !strings.Contains(preview, "https://fixture.example") || strings.Contains(preview, "/mcp") {
		t.Fatal("chat confirmation lost its redacted destination", preview)
	}
}

type outcomeConfirmer struct {
	err        error
	wrongActor bool
}

func (c outcomeConfirmer) Confirm(_ context.Context, _ interact.Confirmation, actor interact.Actor) (interact.Decision, error) {
	if c.wrongActor {
		actor.UserID = "another-owner"
	}
	return interact.Decision{Approved: c.err == nil, Actor: actor}, c.err
}

func TestConfirmationOutcomesDoNotRevokeAccessOrDispatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		confirmer interact.ConfirmationAsker
		want      error
	}{
		{"expired", outcomeConfirmer{err: interact.ErrPromptTimeout}, mcpclient.ErrConfirmationExpired},
		{"deadline", outcomeConfirmer{err: context.DeadlineExceeded}, mcpclient.ErrConfirmationExpired},
		{"declined", outcomeConfirmer{err: interact.ErrConfirmRejected}, mcpclient.ErrConfirmationRejected},
		{"cancelled", outcomeConfirmer{err: interact.ErrCancelled}, mcpclient.ErrConfirmationCancelled},
		{"context_cancelled", outcomeConfirmer{err: context.Canceled}, mcpclient.ErrConfirmationCancelled},
		{"channel_failed", outcomeConfirmer{err: errors.New("channel failed")}, mcpclient.ErrConfirmationUnavailable},
		{"wrong_actor", outcomeConfirmer{wrongActor: true}, mcpclient.ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, sc, before := newConfirmationService(t)
			ctx := mcpclient.WithScope(context.Background(), mcpclient.Scope{OwnerID: "owner", CallerID: "main", ConversationID: "private", Private: true})
			ctx = interact.WithConfirmer(ctx, tc.confirmer)
			input, _ := json.Marshal(map[string]any{"connection_id": before.ID, "item_id": "item", "descriptor_revision": "hash", "arguments": map[string]any{}})
			if _, err := New(service, "call_tool").Execute(ctx, input); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			plans, err := service.Plans(context.Background(), sc)
			if err != nil || len(plans) != 0 {
				t.Fatalf("failed confirmation left a runnable plan: %v %v", plans, err)
			}
			after, err := service.Connection(context.Background(), sc, before.ID)
			if err != nil || after.Revision != before.Revision || after.Generation != before.Generation || !after.Items[0].Allowed {
				t.Fatalf("confirmation changed permission: %+v %v", after, err)
			}
			state, err := service.Snapshot(context.Background(), sc)
			if err != nil || len(state.Executions) != 0 {
				t.Fatalf("confirmation dispatched remote execution: %+v %v", state.Executions, err)
			}
		})
	}
}
