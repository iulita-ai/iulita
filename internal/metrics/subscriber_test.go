package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/cost"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

func TestUsageMetricsBoundLabelsAndKeepUnknownCost(t *testing.T) {
	oldRegisterer, oldGatherer := prometheus.DefaultRegisterer, prometheus.DefaultGatherer
	registry := prometheus.NewRegistry()
	prometheus.DefaultRegisterer, prometheus.DefaultGatherer = registry, registry
	t.Cleanup(func() { prometheus.DefaultRegisterer, prometheus.DefaultGatherer = oldRegisterer, oldGatherer })
	m := New()
	bus := eventbus.New(zap.NewNop())
	m.RegisterSubscribers(bus)
	p := eventbus.LLMUsagePayload{AttemptID: "fixture", Provider: "private-provider-name", Model: "private-model-name", ProfileID: "private-profile", ChatID: "private-chat", Role: "private-role", Operation: "private-operation", Status: "private-status", InputTokens: 2, CacheReadInputTokens: 3, StartedAt: time.Now(), CompletedAt: time.Now(), CostEstimate: &cost.Estimate{Status: "price_unknown"}}
	bus.Publish(context.Background(), eventbus.Event{Type: eventbus.LLMUsage, Payload: p})
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundAttempt, foundUnknown := false, false
	for _, f := range families {
		if f.GetName() == "iulita_llm_attempts_total" {
			foundAttempt = true
			for _, metric := range f.Metric {
				for _, label := range metric.Label {
					if label.GetValue() != "other" {
						t.Fatalf("unbounded label: %+v", label)
					}
				}
			}
		}
		if f.GetName() == "iulita_llm_cost_unknown_attempts_total" {
			foundUnknown = true
			if f.Metric[0].Counter.GetValue() != 1 {
				t.Fatal("unknown cost not counted")
			}
		}
		if f.GetName() == "iulita_llm_tokens_input_total" && f.Metric[0].Counter.GetValue() != 5 {
			t.Fatal("cached context missing from token counter")
		}
	}
	if !foundAttempt || !foundUnknown {
		t.Fatal("attempt/unknown cost metrics missing")
	}
}
