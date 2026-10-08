package metrics

import (
	"context"
	"regexp"
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

// TestLocationMetricsBoundLabels verifies the location counters increment with
// catalog-bounded labels and never carry coordinate-shaped values.
func TestLocationMetricsBoundLabels(t *testing.T) {
	oldRegisterer, oldGatherer := prometheus.DefaultRegisterer, prometheus.DefaultGatherer
	registry := prometheus.NewRegistry()
	prometheus.DefaultRegisterer, prometheus.DefaultGatherer = registry, registry
	t.Cleanup(func() { prometheus.DefaultRegisterer, prometheus.DefaultGatherer = oldRegisterer, oldGatherer })
	m := New()
	bus := eventbus.New(zap.NewNop())
	m.RegisterSubscribers(bus)

	bus.Publish(context.Background(), eventbus.Event{Type: eventbus.LocationSent,
		Payload: eventbus.LocationSentPayload{Kind: "pin", Outcome: "sent"}})
	bus.Publish(context.Background(), eventbus.Event{Type: eventbus.LocationSent,
		Payload: eventbus.LocationSentPayload{Kind: "weird", Outcome: "fallback"}})
	bus.Publish(context.Background(), eventbus.Event{Type: eventbus.GeocodeExecuted,
		Payload: eventbus.GeocodePayload{Direction: "forward", Outcome: "throttled"}})

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`-?\d{1,3}\.\d{4,}`)
	outbound := map[string]float64{}
	geocode := map[string]float64{}
	for _, f := range families {
		for _, metric := range f.Metric {
			for _, label := range metric.Label {
				if re.MatchString(label.GetValue()) {
					t.Fatalf("coordinate-shaped metric label: %+v", label)
				}
			}
		}
		switch f.GetName() {
		case "iulita_location_outbound_total":
			for _, metric := range f.Metric {
				outbound[metric.Label[0].GetValue()+"/"+metric.Label[1].GetValue()] = metric.Counter.GetValue()
			}
		case "iulita_location_geocode_total":
			for _, metric := range f.Metric {
				geocode[metric.Label[0].GetValue()+"/"+metric.Label[1].GetValue()] = metric.Counter.GetValue()
			}
		}
	}
	if outbound["pin/sent"] != 1 {
		t.Fatalf("pin/sent counter wrong: %+v", outbound)
	}
	if outbound["other/fallback"] != 1 {
		t.Fatalf("out-of-catalog kind must collapse to other: %+v", outbound)
	}
	if geocode["forward/throttled"] != 1 {
		t.Fatalf("geocode counter wrong: %+v", geocode)
	}
}
