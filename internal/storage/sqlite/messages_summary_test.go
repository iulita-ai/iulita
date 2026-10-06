package sqlite

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestSummaryReplacementIsAtomicAndChronological(t *testing.T) {
	ctx := context.Background()
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	messages := make([]*domain.ChatMessage, 0, 3)
	for _, text := range []string{"first", "second", "keep"} {
		m := &domain.ChatMessage{ChatID: "chat", Role: domain.RoleUser, Content: text}
		if err = s.SaveMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	// Simulate a storage error exactly at insertion, after the deletion statement.
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER reject_summary BEFORE INSERT ON chat_messages WHEN new.content='summary' BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	summary := &domain.ChatMessage{ChatID: "chat", Role: domain.RoleAssistant, Content: "summary"}
	if err = s.ReplaceMessagesWithSummary(ctx, "chat", messages[1].ID, summary); err == nil {
		t.Fatal("expected insertion failure")
	}
	history, _ := s.GetHistory(ctx, "chat", 0)
	if len(history) != 3 || history[0].Content != "first" {
		t.Fatal("failed transaction lost history")
	}
	_, _ = s.db.ExecContext(ctx, `DROP TRIGGER reject_summary`)
	if err = s.ReplaceMessagesWithSummary(ctx, "chat", messages[1].ID, summary); err != nil {
		t.Fatal(err)
	}
	history, _ = s.GetHistory(ctx, "chat", 0)
	if len(history) != 2 || history[0].Content != "summary" || history[1].Content != "keep" {
		t.Fatalf("summary order: %+v", history)
	}
	hits, err := s.SearchMessages(ctx, "chat", "summary", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("FTS summary %d %v", len(hits), err)
	}
}
