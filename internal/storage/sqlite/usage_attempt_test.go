package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

func TestUsageAttemptAtomicDedupAndLegacyUncertainty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 3, 2, 0, 0, time.UTC)
	amount := .15
	a := &domain.LLMUsageAttempt{AttemptID: "unique", ChatID: "chat", Model: "deepseek-flash", Provider: "deepseek", RequestedModel: "deepseek-flash", StartedAt: at, CompletedAt: at.Add(time.Second), Status: "success", EstimatedUSD: &amount, CostStatus: "estimated", PriceVersion: "frozen", PriceSource: "fixture"}
	rec := storage.UsageUpsert{ChatID: "chat", Model: a.Model, Provider: a.Provider, Hour: at.Truncate(time.Hour), InputTokens: 10, Requests: 1, CostUSD: amount, CostKnownRequests: 1}
	for i := 0; i < 2; i++ {
		if err := s.SaveUsageAttempt(ctx, a, rec); err != nil {
			t.Fatal(err)
		}
	}
	// Rows from old binaries have no classification counts. They remain
	// uncertain after migration rather than being silently shown as free.
	if err := s.UpsertUsage(ctx, storage.UsageUpsert{ChatID: "chat", Model: a.Model, Provider: a.Provider, Hour: at.Truncate(time.Hour), Requests: 2}); err != nil {
		t.Fatal(err)
	}
	summary, err := s.GetUsageSummary(ctx, storage.UsageFilter{ChatID: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalRequests != 3 || summary.TotalInputTokens != 10 || summary.TotalCostUSD != amount || summary.TotalCostUnknownRequests != 2 {
		t.Fatalf("summary: %+v", summary)
	}
	if err = s.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	days, err := s.GetUsageByDay(ctx, storage.UsageFilter{ChatID: "chat"})
	if err != nil || len(days) != 1 || days[0].Date != "2026-10-01" || days[0].CostUnknownRequests != 2 {
		t.Fatalf("restart day: %+v %v", days, err)
	}
	models, err := s.GetUsageByModel(ctx, storage.UsageFilter{ChatID: "chat"})
	if err != nil || len(models) != 1 || models[0].CostUnknownRequests != 2 {
		t.Fatalf("model summary: %+v %v", models, err)
	}
	var stored domain.LLMUsageAttempt
	if err = s.db.NewSelect().Model(&stored).Where("attempt_id = ?", "unique").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if stored.PriceVersion != "frozen" || stored.EstimatedUSD == nil || *stored.EstimatedUSD != amount {
		t.Fatalf("attempt: %+v", stored)
	}
}

func TestUsageAggregateKeepsProviderIdentityAcrossRestart(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Hour)
	for _, provider := range []string{"deepseek", "openai"} {
		if err := s.UpsertUsage(ctx, storage.UsageUpsert{ChatID: "chat", Model: "same-model", Provider: provider, Hour: at, Requests: 1, CostUnknownRequests: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"deepseek", "openai"} {
		summary, err := s.GetUsageSummary(ctx, storage.UsageFilter{Provider: provider})
		if err != nil || summary.TotalRequests != 1 {
			t.Fatalf("provider %s lost identity: %+v %v", provider, summary, err)
		}
	}
}

func TestUsageAttemptRollsBackLedgerWhenAggregateFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER usage_fail BEFORE INSERT ON usage_stats BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	a := &domain.LLMUsageAttempt{AttemptID: "rollback", ChatID: "chat", StartedAt: at, CompletedAt: at, Status: "error", CostStatus: "usage_unavailable"}
	if err := s.SaveUsageAttempt(ctx, a, storage.UsageUpsert{ChatID: "chat", Hour: at, Requests: 1, CostUnknownRequests: 1}); err == nil {
		t.Fatal("aggregate failure not surfaced")
	}
	count, err := s.db.NewSelect().Model((*domain.LLMUsageAttempt)(nil)).Count(ctx)
	if err != nil || count != 0 {
		t.Fatalf("ledger was not rolled back: %d %v", count, err)
	}
}
