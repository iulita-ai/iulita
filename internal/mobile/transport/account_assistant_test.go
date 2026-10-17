package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/assistanttools"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/memory"
)

type accountFixtureModel struct{ t *testing.T }

func (p accountFixtureModel) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	if r.Message == "remember fixture" {
		if len(r.ToolExchanges) == 0 {
			if r.ForceTool != "remember" || !r.RequireToolOutcome {
				p.t.Error("memory intent not enforced")
			}
			return llm.Response{ToolCalls: []llm.ToolCall{{ID: "remember-one", Name: "remember", Input: json.RawMessage(`{"content":"SAVED_ACCOUNT_CANARY preference"}`)}}}, nil
		}
		return llm.Response{Content: "saved through tool"}, nil
	}
	if r.Message == "new separate chat" {
		if len(r.History) != 0 {
			p.t.Error("another chat history joined")
		}
		if !strings.Contains(r.SystemPrompt, "SAVED_ACCOUNT_CANARY") {
			p.t.Error("saved account knowledge missing")
		}
		return llm.Response{Content: "separate history, shared account"}, nil
	}
	// A lying model cannot acknowledge a forced memory write without a tool.
	return llm.Response{Content: "I remembered everything successfully"}, nil
}

func TestAccountAssistantThroughBrowserOAuthGRPCAndFencedMemory(t *testing.T) {
	h := setupHarness(t)
	e := h.server.config.Engine
	<-e.Ready()
	r := skill.NewRegistry()
	r.AddCapability("memory")
	r.Register(memory.NewRemember(h.db))
	r.Register(memory.NewRecall(h.db))
	r.Register(memory.NewForget(h.db))
	e.Catalog = assistanttools.Catalog(r, "remember")(e)
	e.AccountMemory = browseroauth.PasswordAccounts{Users: h.db}
	e.Resolve = func(context.Context, string, string) (engine.Profile, error) {
		return engine.Profile{Provider: accountFixtureModel{t}, Revision: 1, Digest: "fixture-model"}, nil
	}
	token, _ := h.login(t, "owner", "chats:read", "chats:write")
	ctx := tokenContext(token)
	chat := v1.NewChatServiceClient(h.conn)
	create := func() *v1.Conversation {
		t.Helper()
		c, err := chat.CreateConversation(ctx, &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "account", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: v1.MemoryMode_MEMORY_MODE_ACCOUNT}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	send := func(c *v1.Conversation, text string) *v1.Turn {
		t.Helper()
		request := &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: c.ConversationId, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: text}
		turn, err := chat.SendMessage(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		again, err := chat.SendMessage(ctx, request)
		if err != nil || again.TurnId != turn.TurnId {
			t.Fatal("message replay", err)
		}
		for until := time.Now().Add(5 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
			turn, err = chat.GetTurn(ctx, &v1.GetTurnRequest{Client: clientContext(), ConversationId: c.ConversationId, Lookup: &v1.GetTurnRequest_TurnId{TurnId: turn.TurnId}})
			if err != nil {
				t.Fatal(err)
			}
			if turn.State == v1.TurnState_TURN_STATE_COMPLETED {
				return turn
			}
		}
		t.Fatal("turn not completed", turn.State)
		return nil
	}
	first := create()
	send(first, "remember fixture")
	facts, err := h.db.SearchFactsByUser(ctx, h.owners["owner"], "SAVED_ACCOUNT_CANARY", 10)
	if err != nil || len(facts) != 1 {
		t.Fatal("fact not committed once", len(facts), err)
	}
	second := create()
	send(second, "new separate chat")
	if _, err = chat.ClearHistory(ctx, &v1.ClearHistoryRequest{Client: clientContext(), Operation: operation(), ConversationId: first.ConversationId, ExpectedHistoryGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	turn := send(second, "remember without calling tool")
	messages, err := chat.ListMessages(ctx, &v1.ListMessagesRequest{Client: clientContext(), ConversationId: second.ConversationId, HistoryGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range messages.Messages {
		if message.MessageId == turn.OutputMessageId {
			found = strings.Contains(message.Text, "Действие не выполнено")
		}
	}
	if !found {
		t.Fatal("model fabricated memory success")
	}
	facts, err = h.db.SearchFactsByUser(ctx, h.owners["owner"], "SAVED_ACCOUNT_CANARY", 10)
	if err != nil || len(facts) != 1 {
		t.Fatal("chat clear erased account memory", err)
	}
}
