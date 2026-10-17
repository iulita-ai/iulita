package transport

import (
	"context"
	"testing"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/assistanttools"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCapabilitiesUsesAuthenticatedOwnerAndActualConversationMode(t *testing.T) {
	h := setupHarness(t)
	<-h.server.config.Engine.Ready()
	r := skill.NewRegistry()
	r.AddCapability("memory")
	r.Register(memory.NewRecall(h.db))
	h.server.config.Engine.Catalog = assistanttools.Catalog(r)(h.server.config.Engine)
	h.server.config.Engine.AccountMemory = browseroauth.PasswordAccounts{Users: h.db}
	token, _ := h.login(t, "owner", "chats:read", "chats:write")
	other, _ := h.login(t, "other", "chats:read", "chats:write")
	chat := v1.NewChatServiceClient(h.conn)
	extra := v1.NewChatExtrasServiceClient(h.conn)
	for _, mode := range []v1.MemoryMode{v1.MemoryMode_MEMORY_MODE_ACCOUNT, v1.MemoryMode_MEMORY_MODE_ISOLATED} {
		c, err := chat.CreateConversation(tokenContext(token), &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "capabilities", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: mode}})
		if err != nil {
			t.Fatal(err)
		}
		request := &v1.GetChatCapabilitiesRequest{Client: clientContext(), ConversationId: c.ConversationId}
		out, err := extra.GetChatCapabilities(tokenContext(token), request)
		if err != nil || out.MemoryMode != mode {
			t.Fatal("actual mode", err)
		}
		found := false
		for _, tool := range out.Tools {
			if tool.Name == "recall" {
				found = true
			}
		}
		if found != (mode == v1.MemoryMode_MEMORY_MODE_ACCOUNT) {
			t.Fatal("memory tool in wrong mode", out)
		}
		if _, err = extra.GetChatCapabilities(tokenContext(other), request); status.Code(err) != codes.NotFound {
			t.Fatal("foreign capability read", err)
		}
		if _, err = extra.GetChatCapabilities(context.Background(), request); status.Code(err) != codes.Unauthenticated {
			t.Fatal("anonymous capability read", err)
		}
		r.DisableSkill("recall")
		out, err = extra.GetChatCapabilities(tokenContext(token), request)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range out.Tools {
			if tool.Name == "recall" {
				t.Fatal("disabled tool still advertised")
			}
		}
		r.EnableSkill("recall")
	}
}
