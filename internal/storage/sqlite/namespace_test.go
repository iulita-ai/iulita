package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// Insert via the DB to simulate future owned storage and ensure the old APIs
// cannot see private rows even when they already exist (including malformed IDs).
func TestLegacyNamespaceIsolation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"mobile:conversation:secret", "MOBILE:malformed", "web:owner", "12345"} {
		for _, row := range []any{
			&domain.ChatMessage{ChatID: id, UserID: "owner", Role: domain.RoleUser, Content: "canary"},
			&domain.Directive{ChatID: id, UserID: "owner", Content: "canary"},
			&domain.Fact{ChatID: id, UserID: "owner", Content: "canary", CreatedAt: time.Now(), LastAccessedAt: time.Now()},
			&domain.Insight{ChatID: id, UserID: "owner", Content: "canary", CreatedAt: time.Now()},
			&domain.TechFact{ChatID: id, UserID: "owner", Category: "test", Key: "canary", Value: "canary"},
		} {
			if _, err := s.db.NewInsert().Model(row).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	ids, err := s.GetChatIDs(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{"12345", "web:owner"}) {
		t.Fatalf("IDs: %v %v", ids, err)
	}
	for _, id := range []string{"mobile:conversation:secret", "MOBILE:malformed"} {
		calls := map[string]func() error{
			"history":          func() error { _, stepErr1 := s.GetHistory(ctx, id, 10); return stepErr1 },
			"page":             func() error { _, stepErr2 := s.GetHistoryBefore(ctx, id, 100, 10); return stepErr2 },
			"search":           func() error { _, stepErr3 := s.SearchMessages(ctx, id, "canary", 10); return stepErr3 },
			"count":            func() error { _, stepErr4 := s.CountMessages(ctx, id); return stepErr4 },
			"directive":        func() error { _, stepErr5 := s.GetDirective(ctx, id); return stepErr5 },
			"clear":            func() error { return s.ClearHistory(ctx, id) },
			"delete page":      func() error { return s.DeleteMessagesBefore(ctx, id, 100) },
			"delete directive": func() error { return s.DeleteDirective(ctx, id) },
			"write message":    func() error { return s.SaveMessage(ctx, &domain.ChatMessage{ChatID: id}) },
			"write directive":  func() error { return s.SaveDirective(ctx, &domain.Directive{ChatID: id}) },
			"write fact":       func() error { return s.SaveFact(ctx, &domain.Fact{ChatID: id}) },
			"write insight":    func() error { return s.SaveInsight(ctx, &domain.Insight{ChatID: id}) },
			"write tech fact":  func() error { return s.UpsertTechFact(ctx, &domain.TechFact{ChatID: id}) },
		}
		for name, call := range calls {
			if stepErr6 := call(); !errors.Is(stepErr6, domain.ErrMobileNamespace) {
				t.Errorf("%s: %v", name, stepErr6)
			}
		}
	}
	// Global/user-wide retrieval must filter before pagination, ranking and counts.
	checks := map[string]func() (any, error){
		"messages":            func() (any, error) { return s.SearchMessagesByUser(ctx, "owner", "canary", 20) },
		"facts":               func() (any, error) { return s.GetAllFactsByUser(ctx, "owner") },
		"global facts":        func() (any, error) { return s.GetAllFacts(ctx, "") },
		"fact FTS":            func() (any, error) { return s.SearchFactsByUser(ctx, "owner", "canary", 20) },
		"recent facts":        func() (any, error) { return s.GetRecentFactsByUser(ctx, "owner", 20) },
		"insight FTS":         func() (any, error) { return s.SearchInsightsByUser(ctx, "owner", "canary", 20) },
		"insights":            func() (any, error) { return s.GetRecentInsightsByUser(ctx, "owner", 20) },
		"tech facts":          func() (any, error) { return s.GetTechFactsByUser(ctx, "owner") },
		"global tech facts":   func() (any, error) { return s.GetTechFacts(ctx, "") },
		"unembedded facts":    func() (any, error) { return s.FactsWithoutEmbeddings(ctx, 20) },
		"unembedded insights": func() (any, error) { return s.InsightsWithoutEmbeddings(ctx, 20) },
	}
	for name, call := range checks {
		rows, stepErr7 := call()
		if stepErr7 != nil {
			t.Errorf("%s: %v", name, stepErr7)
			continue
		}
		if reflect.ValueOf(rows).Len() != 2 {
			t.Errorf("%s: %#v", name, rows)
		}
	}
	d, err := s.GetDirectiveByUser(ctx, "owner")
	if err != nil || d == nil || domain.IsMobileChatID(d.ChatID) {
		t.Fatalf("user directive: %#v %v", d, err)
	}
	for _, kind := range []string{"fact", "insight"} {
		var rows []struct{ ID int64 }
		table := kind + "s"
		if stepErr8 := s.db.NewSelect().TableExpr(table).Column("id").Where("chat_id LIKE 'mobile:%'").Scan(ctx, &rows); stepErr8 != nil {
			t.Fatal(stepErr8)
		}
		for _, row := range rows {
			if kind == "fact" {
				err = s.SaveFactVector(ctx, row.ID, []float32{1, 0})
			} else {
				err = s.SaveInsightVector(ctx, row.ID, []float32{1, 0})
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	facts, err := s.SearchFactsHybridByUser(ctx, "owner", "missing", []float32{1, 0}, 20, 1)
	if err != nil || len(facts) != 0 {
		t.Fatalf("vector facts leak: %#v %v", facts, err)
	}
	insights, err := s.SearchInsightsHybridByUser(ctx, "owner", "missing", []float32{1, 0}, 20, 1)
	if err != nil || len(insights) != 0 {
		t.Fatalf("vector insights leak: %#v %v", insights, err)
	}
	// Failed legacy mutations leave the private transcript intact.
	n, err := s.db.NewSelect().Model((*domain.ChatMessage)(nil)).Where("chat_id LIKE 'mobile:%'").Count(ctx)
	if err != nil || n != 2 {
		t.Fatalf("private rows changed: %d %v", n, err)
	}
	for _, id := range []string{"web:owner", "12345"} {
		rows, err := s.GetHistory(ctx, id, 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("legacy regression: %v %v", rows, err)
		}
	}
}
