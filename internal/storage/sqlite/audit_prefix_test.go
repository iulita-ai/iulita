package sqlite

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestListAuditEntriesByPrefix(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for _, e := range []*domain.AuditEntry{
		{ChatID: "c1", Action: "slack.search.ok", Detail: "{}", Success: true},
		{ChatID: "c1", Action: "slack.post.sent", Detail: "{}", Success: true},
		{ChatID: "c1", Action: "skill.executed", Detail: "{}", Success: true}, // non-slack
	} {
		if err := store.SaveAuditEntry(ctx, e); err != nil {
			t.Fatalf("SaveAuditEntry: %v", err)
		}
	}

	got, err := store.ListAuditEntriesByPrefix(ctx, "slack.", 50)
	if err != nil {
		t.Fatalf("ListAuditEntriesByPrefix: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 slack entries, got %d", len(got))
	}
	for _, e := range got {
		if len(e.Action) < 6 || e.Action[:6] != "slack." {
			t.Errorf("unexpected non-slack action %q", e.Action)
		}
	}
	// Newest-first: slack.post.sent was inserted after slack.search.ok.
	if got[0].Action != "slack.post.sent" {
		t.Errorf("expected newest-first, got %q first", got[0].Action)
	}
	// Limit is respected.
	if lim, _ := store.ListAuditEntriesByPrefix(ctx, "slack.", 1); len(lim) != 1 {
		t.Errorf("limit=1 returned %d", len(lim))
	}
}
