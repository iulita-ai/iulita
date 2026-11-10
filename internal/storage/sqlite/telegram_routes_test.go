package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestTelegramRoutesSurviveRestartAndRejectTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.db")
	s, e := New(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	original := &domain.TelegramConversationRoute{ConversationID: "tgconv:one", InstanceID: "bot-a", ChatID: 42, ThreadID: 100, OwnerID: "alice", Private: true}
	if e = s.PutTelegramRoute(ctx, original); e != nil {
		t.Fatal(e)
	}
	other := *original
	other.InstanceID = "bot-b"
	other.OwnerID = "bob"
	if e = s.PutTelegramRoute(ctx, &other); e == nil {
		t.Fatal("route takeover allowed")
	}
	s.Close()
	s, e = New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	r, e := s.GetTelegramRoute(ctx, original.ConversationID)
	if e != nil || r == nil || r.InstanceID != "bot-a" || r.OwnerID != "alice" || r.ThreadID != 100 {
		t.Fatalf("route lost or changed: %+v %v", r, e)
	}
	// Route isolation also preserves history and /clear boundaries.
	for _, id := range []string{"42", "tgconv:one", "tgconv:two"} {
		if e = s.SaveMessage(ctx, &domain.ChatMessage{ChatID: id, UserID: "alice", Role: domain.RoleUser, Content: id}); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.ClearHistory(ctx, "tgconv:one"); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"42", "tgconv:two"} {
		h, e := s.GetHistory(ctx, id, 5)
		if e != nil || len(h) != 1 || h[0].Content != id {
			t.Fatalf("history crossed route %s: %+v %v", id, h, e)
		}
	}
}
