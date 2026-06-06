package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func newReviewStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return store
}

// seedTurn writes a few messages and returns the last message ID (the boundary).
func seedTurn(t *testing.T, store *sqlite.Store, chatID, userID string) int64 {
	t.Helper()
	ctx := context.Background()
	msgs := []*domain.ChatMessage{
		{ChatID: chatID, UserID: userID, Role: domain.RoleUser, Content: "deploy the service to staging"},
		{ChatID: chatID, UserID: userID, Role: domain.RoleAssistant, Content: "running build, then kubectl apply, then health check"},
		{ChatID: chatID, UserID: userID, Role: domain.RoleAssistant, Content: "done — staging is healthy"},
	}
	var lastID int64
	for _, m := range msgs {
		if err := store.SaveMessage(ctx, m); err != nil {
			t.Fatalf("save message: %v", err)
		}
		lastID = m.ID
	}
	return lastID
}

func reviewPayload(t *testing.T, chatID, userID string, lastID int64) string {
	t.Helper()
	b, _ := json.Marshal(skillReviewPayload{ChatID: chatID, UserID: userID, LastMessageID: lastID})
	return string(b)
}

func TestSkillReview_SavesLesson(t *testing.T) {
	store := newReviewStore(t)
	lastID := seedTurn(t, store, "chat1", "user1")

	provider := &mockLLMProvider{response: "Run the health check before declaring success to catch rollout failures early."}
	h := NewSkillReviewHandler(store, provider, config.SelfImproveConfig{Enabled: true}, zap.NewNop())

	res, err := h.Handle(context.Background(), reviewPayload(t, "chat1", "user1", lastID))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(res, `"lesson_saved":1`) {
		t.Errorf("expected lesson_saved=1, got %s", res)
	}

	insights, err := store.GetRecentInsights(context.Background(), "chat1", 10)
	if err != nil {
		t.Fatalf("get insights: %v", err)
	}
	if len(insights) != 1 {
		t.Fatalf("expected 1 insight, got %d", len(insights))
	}
	if !strings.HasPrefix(insights[0].Content, "Workflow lesson: ") {
		t.Errorf("insight not prefixed: %q", insights[0].Content)
	}
}

func TestSkillReview_NoneSentinelSkips(t *testing.T) {
	store := newReviewStore(t)
	lastID := seedTurn(t, store, "chat1", "user1")

	provider := &mockLLMProvider{response: "  none  "} // case-insensitive, trimmed
	h := NewSkillReviewHandler(store, provider, config.SelfImproveConfig{Enabled: true}, zap.NewNop())

	res, err := h.Handle(context.Background(), reviewPayload(t, "chat1", "user1", lastID))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(res, `"lesson_saved":0`) {
		t.Errorf("expected lesson_saved=0, got %s", res)
	}

	insights, _ := store.GetRecentInsights(context.Background(), "chat1", 10)
	if len(insights) != 0 {
		t.Errorf("expected no insight for NONE sentinel, got %d", len(insights))
	}
}

func TestSkillReview_DisabledNoop(t *testing.T) {
	store := newReviewStore(t)
	lastID := seedTurn(t, store, "chat1", "user1")

	provider := &mockLLMProvider{response: "some lesson"}
	h := NewSkillReviewHandler(store, provider, config.SelfImproveConfig{Enabled: false}, zap.NewNop())

	res, err := h.Handle(context.Background(), reviewPayload(t, "chat1", "user1", lastID))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(res, `"reviewed":0`) {
		t.Errorf("expected reviewed=0 when disabled, got %s", res)
	}
}
