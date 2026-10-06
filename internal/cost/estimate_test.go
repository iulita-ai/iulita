package cost

import (
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/llm"
)

func TestEstimateKnownUnknownAndCached(t *testing.T) {
	tr := New(config.CostConfig{})
	at := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	usage := llm.Usage{InputTokens: 1_000_000, CacheReadInputTokens: 1_000_000, OutputTokens: 1_000_000}
	for model, amount := range map[string]float64{"deepseek-flash": 1.506, "deepseek-v4-pro": 5.324, "glm-5.3-flash": .68, "glm-5.3": 6.06} {
		e := tr.Estimate(model, usage, at)
		if !e.Known || e.USD == nil || !approx(*e.USD, amount) || e.PriceVersion != PriceVersion || !e.At.Equal(at) || e.Source == "" {
			t.Fatalf("estimate %s: %+v", model, e)
		}
	}
	for _, a := range []llm.Attempt{
		{AttemptMetadata: llm.AttemptMetadata{RequestedModel: "deepseek-flash"}, Model: "unknown-served-model", ModelVerified: true, Usage: usage, UsageAvailable: true, StartedAt: at},
		{Model: "deepseek-flash", ChargeUnknown: true, StartedAt: at},
	} {
		e := tr.EstimateAttempt(a)
		if e.Known || e.USD != nil {
			t.Fatalf("unknown charge became zero: %+v", e)
		}
	}
	e := tr.EstimateAttempt(llm.Attempt{Cached: true, Model: "unknown", StartedAt: at})
	if !e.Known || e.USD == nil || *e.USD != 0 || e.Status != "cache_hit" {
		t.Fatalf("cache cost: %+v", e)
	}
}

func TestConservativePeakEstimatesAtUtcBoundaries(t *testing.T) {
	tr := New(config.CostConfig{})
	base := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC) // Monday
	for _, test := range []struct {
		minute int
		peak   bool
	}{{59, false}, {60, true}, {239, true}, {240, false}, {359, false}, {360, true}, {599, true}, {600, false}} {
		at := base.Add(time.Duration(test.minute) * time.Minute)
		if DeepSeekPeakWindow(at.In(time.FixedZone("Moscow", 3*3600))) != test.peak {
			t.Errorf("UTC window wrong at %v", at)
		}
		e := tr.Estimate("deepseek-flash", llm.Usage{InputTokens: 1_000_000}, at)
		if !e.Known || *e.USD != .30 || e.Status != "conservative_peak_estimate" {
			t.Fatalf("offpeak falsely billed as invoice: %+v", e)
		}
	}
	if DeepSeekPeakWindow(base.AddDate(0, 0, 5).Add(2 * time.Hour)) {
		t.Fatal("Saturday counted as a peak weekday")
	}
}

func TestConfiguredPriceVersionAndInvalidUsage(t *testing.T) {
	tr := New(config.CostConfig{Prices: map[string]config.ModelPrice{"custom": {InputPerMillion: 0, OutputPerMillion: 0}}})
	e := tr.Estimate("custom", llm.Usage{InputTokens: 5}, time.Now())
	if !e.Known || e.USD == nil || *e.USD != 0 || e.Source != "configured_override" || e.PriceVersion == PriceVersion {
		t.Fatalf("explicit free override: %+v", e)
	}
	e = tr.Estimate("custom", llm.Usage{CacheReadInputTokens: -1}, time.Now())
	if e.Known || e.USD != nil {
		t.Fatal("negative usage priced")
	}
}

func TestTrackFrozenEstimateAndLocalRejection(t *testing.T) {
	tr := New(config.CostConfig{DailyLimitUSD: .2})
	amount := .15
	if exceeded, total := tr.TrackEstimate(Estimate{Known: true, USD: &amount}); exceeded || !approx(total, amount) {
		t.Fatalf("first budget estimate: %v %v", exceeded, total)
	}
	if exceeded, total := tr.TrackEstimate(Estimate{Status: "price_unknown"}); exceeded || !approx(total, amount) {
		t.Fatalf("unknown changed amount: %v %v", exceeded, total)
	}
	if exceeded, total := tr.TrackEstimate(Estimate{Known: true, USD: &amount}); !exceeded || !approx(total, .30) {
		t.Fatalf("second budget estimate: %v %v", exceeded, total)
	}
	e := tr.EstimateAttempt(llm.Attempt{Status: "rejected", StartedAt: time.Now()})
	if !e.Known || e.USD == nil || *e.USD != 0 || e.Status != "local_rejection" {
		t.Fatalf("local rejection is not known free: %+v", e)
	}
}

func TestBudgetDayUsesUtcCalendarDate(t *testing.T) {
	at := time.Date(2026, 10, 5, 1, 30, 0, 0, time.FixedZone("Moscow", 3*3600))
	if budgetDay(at) != 20261004 || budgetDay(at.AddDate(1, 0, 0)) == budgetDay(at) {
		t.Fatal("budget day does not match durable UTC day")
	}
}
