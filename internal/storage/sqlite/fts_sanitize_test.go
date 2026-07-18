package sqlite

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

// TestFactSearchSanitizesSpecialChars guards against FTS5 "SQL logic error" on raw
// natural-language queries (hyphens, colons, punctuation) — the recall/remember/forget
// skills pass arbitrary user text to fact/insight search.
func TestFactSearchSanitizesSpecialChars(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.SaveFact(ctx, &domain.Fact{ChatID: "c", UserID: "u", Content: "живёт в Санкт-Петербурге", SourceType: "user"}); err != nil {
		t.Fatal(err)
	}

	// Each of these previously raised "SQL logic error: no such column: ..." / syntax error.
	for _, q := range []string{"Санкт-Петербург", "город: Петербург", "Петербург?", `"quoted"`, "a-b-c", "foo:bar", ""} {
		if _, err := store.SearchFacts(ctx, "c", q, 5); err != nil {
			t.Errorf("SearchFacts(%q) errored: %v", q, err)
		}
		if _, err := store.SearchFactsByUser(ctx, "u", q, 5); err != nil {
			t.Errorf("SearchFactsByUser(%q) errored: %v", q, err)
		}
		if _, err := store.SearchInsights(ctx, "c", q, 5); err != nil {
			t.Errorf("SearchInsights(%q) errored: %v", q, err)
		}
	}

	// A hyphenated term still matches via prefix.
	facts, err := store.SearchFacts(ctx, "c", "Санкт-Петербург", 5)
	if err != nil || len(facts) != 1 {
		t.Errorf("expected 1 hit for hyphenated term, got %d (err=%v)", len(facts), err)
	}

	// An all-punctuation / empty query returns no rows (and never deletes everything).
	if n, err := store.DeleteFactsByQuery(ctx, "c", "  ?? :: "); err != nil || n != 0 {
		t.Errorf("DeleteFactsByQuery with no usable tokens must delete nothing, got n=%d err=%v", n, err)
	}
}
