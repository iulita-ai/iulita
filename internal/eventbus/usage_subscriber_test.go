package eventbus

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/cost"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
	"go.uber.org/zap"
)

func TestUsageSubscriberFrozenPriceRequestHourAndDedup(t *testing.T) {
	s := newTestStore(t)
	bus := New(zap.NewNop())
	// This calculator deliberately has a different price; observed events must
	// retain the original amount, including across an hour boundary/redelivery.
	tr := cost.New(config.CostConfig{Prices: map[string]config.ModelPrice{"deepseek-flash": {InputPerMillion: 99}}})
	RegisterUsageSubscriber(bus, s, tr, zap.NewNop())
	at := time.Date(2026, 10, 1, 23, 59, 0, 0, time.UTC)
	amount := .125
	a := llm.Attempt{AttemptMetadata: llm.AttemptMetadata{Provider: "deepseek", RequestedModel: "deepseek-flash", ProfileID: "profile", Role: "complex", PolicyRevision: 2}, AttemptID: "frozen", ChatID: "chat", UserID: "user", Model: "deepseek-flash", StartedAt: at, CompletedAt: at.Add(2 * time.Minute), Status: "incomplete", Usage: llm.Usage{InputTokens: 10}, UsageAvailable: true}
	p := NewAttemptUsagePayload(a, cost.Estimate{USD: &amount, Known: true, Status: "estimated", PriceVersion: "original", At: at})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bus.Publish(ctx, Event{Type: LLMUsage, Payload: p})
	bus.Publish(ctx, Event{Type: LLMUsage, Payload: p})
	unknown := a
	unknown.AttemptID = "unknown"
	unknown.Model = "unknown-model"
	unknown.Usage = llm.Usage{}
	unknown.UsageAvailable = false
	unknown.ChargeUnknown = true
	bus.Publish(ctx, Event{Type: LLMUsage, Payload: NewAttemptUsagePayload(unknown, tr.EstimateAttempt(unknown))})
	bus.Shutdown()
	summary, err := s.GetUsageSummary(context.Background(), storage.UsageFilter{UserID: "user"})
	if err != nil || summary.TotalRequests != 2 || summary.TotalCostUSD != amount || summary.TotalCostUnknownRequests != 1 || summary.TotalUsageUnknownRequests != 1 {
		t.Fatalf("summary: %+v %v", summary, err)
	}
	rows, err := s.GetUsageByDay(context.Background(), storage.UsageFilter{ChatID: "chat"})
	if err != nil || len(rows) != 1 || rows[0].Date != "2026-10-01" {
		t.Fatalf("used completion/event time: %+v %v", rows, err)
	}
}
