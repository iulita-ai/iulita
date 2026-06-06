package insights

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func newInsightStore(t *testing.T) *sqlite.Store {
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

// TestListInsights_UserScoped reproduces the prod bug: generated insights are
// stored with user_id set and chat_id empty, so a chat-scoped list returned
// "No insights available yet". The skill must use the user-scoped query.
func TestListInsights_UserScoped(t *testing.T) {
	st := newInsightStore(t)
	ctx := context.Background()
	if err := st.SaveInsight(ctx, &domain.Insight{
		ChatID: "", UserID: "user-1", Content: "Batch related API calls", Quality: 4,
	}); err != nil {
		t.Fatalf("save insight: %v", err)
	}

	sk := NewList(st)
	// Telegram-style: a real chat_id plus the resolved user UUID in context.
	cctx := skill.WithUserID(skill.WithChatID(ctx, "-100999"), "user-1")
	out, err := sk.Execute(cctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out, "No insights available") {
		t.Fatalf("expected user-scoped insight, got empty: %q", out)
	}
	if !strings.Contains(out, "Batch related API calls") {
		t.Errorf("insight content missing: %q", out)
	}
}
