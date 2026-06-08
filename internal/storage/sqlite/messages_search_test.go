package sqlite

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestSearchMessages(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	msgs := []*domain.ChatMessage{
		{ChatID: "chatA", UserID: "u1", Role: domain.RoleUser, Content: "What is the weather in Saint Petersburg?"},
		{ChatID: "chatA", UserID: "u1", Role: domain.RoleAssistant, Content: "It is sunny in Saint Petersburg."},
		{ChatID: "chatA", UserID: "u1", Role: domain.RoleUser, Content: "Remind me about the tennis final."},
	}
	for _, m := range msgs {
		if err := store.SaveMessage(ctx, m); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	// Chat-scoped FTS hit (trigger-populated).
	got, err := store.SearchMessages(ctx, "chatA", "weather", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].Content != "What is the weather in Saint Petersburg?" {
		t.Fatalf("weather search = %+v", got)
	}

	// Multi-token query (AND of phrases) + newest-first ordering.
	got, _ = store.SearchMessages(ctx, "chatA", "Saint Petersburg", 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 'Saint Petersburg' hits, got %d", len(got))
	}
	if got[0].ID < got[1].ID {
		t.Errorf("expected newest-first (id DESC), got %d then %d", got[0].ID, got[1].ID)
	}

	// No match.
	if got, _ = store.SearchMessages(ctx, "chatA", "zzznomatch", 10); len(got) != 0 {
		t.Errorf("expected no matches, got %d", len(got))
	}

	// FTS5-special characters must not error (sanitized).
	if _, err := store.SearchMessages(ctx, "chatA", `weather" OR (x AND`, 10); err != nil {
		t.Errorf("sanitized query should not error: %v", err)
	}

	// Empty/whitespace query → no rows, no error.
	if got, err := store.SearchMessages(ctx, "chatA", "   ", 10); err != nil || len(got) != 0 {
		t.Errorf("empty query: got %d err=%v", len(got), err)
	}
}

func TestSearchMessagesByUser(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_ = store.SaveMessage(ctx, &domain.ChatMessage{ChatID: "cA", UserID: "u1", Role: domain.RoleUser, Content: "golang concurrency patterns"})
	_ = store.SaveMessage(ctx, &domain.ChatMessage{ChatID: "cB", UserID: "u1", Role: domain.RoleUser, Content: "golang generics"})
	_ = store.SaveMessage(ctx, &domain.ChatMessage{ChatID: "cC", UserID: "u2", Role: domain.RoleUser, Content: "golang error handling"})

	// User-scoped search spans channels (cA + cB) but not other users.
	got, err := store.SearchMessagesByUser(ctx, "u1", "golang", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 results for u1, got %d", len(got))
	}
	if got, _ := store.SearchMessagesByUser(ctx, "u2", "golang", 10); len(got) != 1 {
		t.Fatalf("expected 1 result for u2, got %d", len(got))
	}

	// Deleting a message removes it from FTS (AFTER DELETE trigger).
	if err := store.DeleteMessagesBefore(ctx, "cA", 1<<62); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := store.SearchMessagesByUser(ctx, "u1", "concurrency", 10); len(got) != 0 {
		t.Errorf("deleted message still searchable: %+v", got)
	}
}

func TestSanitizeFTS5Query(t *testing.T) {
	cases := map[string]string{
		"hello world":      `"hello"* "world"*`,
		`a "b" c`:          `"a"* "b"* "c"*`,
		"  spaced   out  ": `"spaced"* "out"*`,
		"":                 "",
		`"`:                "",
	}
	for in, want := range cases {
		if got := sanitizeFTS5Query(in); got != want {
			t.Errorf("sanitizeFTS5Query(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMessagesFTS_BackfillOnUpgrade simulates an existing deployment that has
// messages but no FTS table yet (pre-feature). Re-running migrations must
// recreate messages_fts and backfill the existing rows so they're searchable.
func TestMessagesFTS_BackfillOnUpgrade(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.SaveMessage(ctx, &domain.ChatMessage{
		ChatID: "c1", UserID: "u1", Role: domain.RoleUser, Content: "backfill me please",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Drop FTS + triggers to mimic a DB created before this feature existed.
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS messages_ai`,
		`DROP TRIGGER IF EXISTS messages_ad`,
		`DROP TABLE IF EXISTS messages_fts`,
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("drop: %v", err)
		}
	}
	// Sanity: search is now impossible (no fts table) — re-migrate to recover.
	if err := store.RunMigrations(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}

	got, err := store.SearchMessages(ctx, "c1", "backfill", 10)
	if err != nil {
		t.Fatalf("search after backfill: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 backfilled message, got %d", len(got))
	}

	// A new message after re-migration is still synced by the recreated trigger.
	if err := store.SaveMessage(ctx, &domain.ChatMessage{ChatID: "c1", UserID: "u1", Role: domain.RoleUser, Content: "fresh after remigrate"}); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	if got, _ := store.SearchMessages(ctx, "c1", "fresh", 10); len(got) != 1 {
		t.Fatalf("trigger not re-synced after remigrate, got %d", len(got))
	}
}
