package sessionsearch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.RunMigrations(context.Background()); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return st
}

func TestSessionSearch_Execute(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	_ = st.SaveMessage(ctx, &domain.ChatMessage{ChatID: "c1", UserID: "u1", Role: domain.RoleUser, Content: "How do I configure Todoist tokens?"})
	_ = st.SaveMessage(ctx, &domain.ChatMessage{ChatID: "c1", UserID: "u1", Role: domain.RoleAssistant, Content: "Set the API token in settings."})

	sk := New(st)
	cctx := skill.WithUserID(skill.WithChatID(ctx, "c1"), "u1")

	out, err := sk.Execute(cctx, json.RawMessage(`{"query":"Todoist token"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "Todoist") {
		t.Errorf("expected a Todoist match, got: %q", out)
	}

	// No match → friendly message, no error.
	out, err = sk.Execute(cctx, json.RawMessage(`{"query":"nonexistentxyz"}`))
	if err != nil || !strings.Contains(out, "No matching") {
		t.Errorf("no-match: out=%q err=%v", out, err)
	}

	// Empty query → error.
	if _, err := sk.Execute(cctx, json.RawMessage(`{"query":""}`)); err == nil {
		t.Error("empty query should error")
	}
}

func TestSessionSearch_ChatFallbackAndNoContext(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	_ = st.SaveMessage(ctx, &domain.ChatMessage{ChatID: "c9", UserID: "u9", Role: domain.RoleUser, Content: "deployment notes for staging"})

	sk := New(st)

	// Chat-only context (no resolved user) must fall back to chat-scoped search.
	chatOnly := skill.WithChatID(ctx, "c9")
	out, err := sk.Execute(chatOnly, json.RawMessage(`{"query":"deployment"}`))
	if err != nil {
		t.Fatalf("chat fallback: %v", err)
	}
	if !strings.Contains(out, "deployment") {
		t.Errorf("chat fallback did not find message: %q", out)
	}

	// Neither user nor chat in context → error.
	if _, err := sk.Execute(ctx, json.RawMessage(`{"query":"deployment"}`)); err == nil {
		t.Error("missing user+chat context should error")
	}
}

func TestSessionSearch_SchemaHasObjectType(t *testing.T) {
	// DeepSeek requires top-level type:object.
	var m map[string]any
	if err := json.Unmarshal(New(nil).InputSchema(), &m); err != nil {
		t.Fatalf("schema invalid: %v", err)
	}
	if m["type"] != "object" {
		t.Errorf("schema type = %v, want object", m["type"])
	}
}
