package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
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

type denialConfirmer struct{}

func (denialConfirmer) Confirm(_ context.Context, _ interact.Confirmation, actor interact.Actor) (interact.Decision, error) {
	return interact.Decision{Actor: actor}, interact.ErrConfirmRejected
}

func TestExplicitChatDenialRemovesDashboardApproval(t *testing.T) {
	ctx := context.Background()
	db, e := sqlite.New(filepath.Join(t.TempDir(), "mcp.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	crypt, _ := config.NewEncryptor(make([]byte, 32))
	vault := credential.NewStore(credential.NewStorageAdapter(db), crypt, zap.NewNop())
	service, e := mcpclient.New(mcpclient.Options{Repository: db, Vault: vault, Protocol: denialProtocol{}, Enabled: true, AllowedOrigins: []string{"https://fixture.example"}, LockPath: filepath.Join(t.TempDir(), "mcp.lock")})
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
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
	chat := mcpclient.WithScope(ctx, mcpclient.Scope{OwnerID: "owner", CallerID: "main", ConversationID: "private", Private: true})
	chat = interact.WithConfirmer(chat, denialConfirmer{})
	input, _ := json.Marshal(map[string]any{"connection_id": c.ID, "item_id": "item", "descriptor_revision": "hash", "arguments": map[string]any{}})
	if _, e = New(service, "call_tool").Execute(chat, input); !errors.Is(e, mcpclient.ErrDenied) {
		t.Fatal(e)
	}
	plans, e := service.Plans(ctx, sc)
	if e != nil || len(plans) != 0 {
		t.Fatal("refused request remained in dashboard", plans, e)
	}
}
