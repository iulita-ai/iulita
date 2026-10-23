package assistanttools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/mcpclient"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/skill"
	mcpskill "github.com/iulita-ai/iulita/internal/skill/mcp"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

type nativeMCPFixture struct{}

func (nativeMCPFixture) Discover(context.Context, mcpclient.Connection) ([]mcpclient.Item, error) {
	return nil, nil
}
func (nativeMCPFixture) Invoke(context.Context, mcpclient.Connection, mcpclient.Item, json.RawMessage, func() error) (payload json.RawMessage, toolError bool, callErr error) {
	return nil, false, mcpclient.ErrDenied
}
func TestNativeMCPExplicitAdapterAndOwnerScope(t *testing.T) {
	db, e := sqlite.New(filepath.Join(t.TempDir(), "mcp.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.RunMigrations(context.Background()); e != nil {
		t.Fatal(e)
	}
	crypt, _ := config.NewEncryptor(make([]byte, 32))
	vault := credential.NewStore(credential.NewStorageAdapter(db), crypt, zap.NewNop())
	svc, e := mcpclient.New(mcpclient.Options{Repository: db, Vault: vault, Protocol: nativeMCPFixture{}, Enabled: true, LockPath: filepath.Join(t.TempDir(), "mcp.lock")})
	if e != nil {
		t.Fatal(e)
	}
	defer svc.Close()
	owner := uuid.NewString()
	_, e = svc.Create(context.Background(), mcpclient.Scope{OwnerID: owner, CallerID: "dashboard", Private: true}, mcpclient.Draft{Label: "NATIVE-OWNER-ONLY", Endpoint: "https://fixture.example/mcp", Domain: "personal"})
	if e != nil {
		t.Fatal(e)
	}
	registry := skill.NewRegistry()
	registry.Register(mcpskill.New(svc, "connections"))
	registry.Register(mcpskill.New(svc, "connection_draft"))
	catalog := Catalog(registry)(&engine.Engine{})
	a := engine.Authority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString(), Scopes: []string{"chats:write", "assistant:services:read"}}
	ctx := engine.WithAuthority(context.Background(), a)
	tools, e := catalog(ctx, owner)
	if e != nil || len(tools) != 1 || tools[0].Definition.Name != "mcp_connections" {
		t.Fatal(tools, e)
	}
	out, e := tools[0].Execute(ctx, conversations.Fence{OwnerID: owner, ConversationID: uuid.NewString(), Generation: 1}, json.RawMessage(`{}`))
	if e != nil || !strings.Contains(out, "NATIVE-OWNER-ONLY") {
		t.Fatal(out, e)
	}
	other := uuid.NewString()
	a.OwnerID = other
	otherCtx := engine.WithAuthority(context.Background(), a)
	otherTools, e := catalog(otherCtx, other)
	if e != nil || len(otherTools) != 1 {
		t.Fatal(e)
	}
	out, e = otherTools[0].Execute(otherCtx, conversations.Fence{OwnerID: other, ConversationID: uuid.NewString(), Generation: 1}, json.RawMessage(`{}`))
	if e != nil || strings.Contains(out, "NATIVE-OWNER-ONLY") {
		t.Fatal(out, e)
	}
	a.PrincipalType = "service"
	serviceCtx := engine.WithAuthority(context.Background(), a)
	tools, e = catalog(serviceCtx, other)
	if e != nil || len(tools) != 0 {
		t.Fatal("service principal admitted", tools, e)
	}
}
