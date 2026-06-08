package cost

import (
	"math"
	"testing"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/llm"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCalculate_CacheHitDiscount(t *testing.T) {
	tr := New(config.CostConfig{
		Enabled: true,
		Prices: map[string]config.ModelPrice{
			"deepseek-v4-flash": {InputPerMillion: 0.14, OutputPerMillion: 0.28, CacheHitPerMillion: 0.0028},
		},
	})
	// 1M miss input, 1M cache-hit input, 1M output.
	got := tr.Calculate("deepseek-v4-flash", llm.Usage{
		InputTokens:          1_000_000,
		CacheReadInputTokens: 1_000_000,
		OutputTokens:         1_000_000,
	})
	want := 0.14 + 0.0028 + 0.28
	if !approx(got, want) {
		t.Errorf("Calculate = %v, want %v", got, want)
	}
}

func TestCalculate_NoCacheHitRate_FallsBackToInput(t *testing.T) {
	// Providers without a cache discount (CacheHitPerMillion == 0) must bill
	// cache-read tokens at the standard input rate — no behavior change.
	tr := New(config.CostConfig{
		Enabled: true,
		Prices: map[string]config.ModelPrice{
			"claude-sonnet-4-6": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
		},
	})
	got := tr.Calculate("claude-sonnet-4-6", llm.Usage{
		InputTokens:          1_000_000,
		CacheReadInputTokens: 1_000_000,
		OutputTokens:         0,
	})
	want := 3.0 + 3.0 // both billed at input rate
	if !approx(got, want) {
		t.Errorf("Calculate = %v, want %v", got, want)
	}
}

func TestCalculate_UnknownModelIsZero(t *testing.T) {
	// A model that is in neither the configured map nor the compiled-in defaults.
	tr := New(config.CostConfig{Enabled: true, Prices: map[string]config.ModelPrice{}})
	if got := tr.Calculate("fictional-model-not-in-defaults", llm.Usage{InputTokens: 1000}); got != 0 {
		t.Errorf("unknown model cost = %v, want 0", got)
	}
}

func TestNew_NilPricesFallsBackToDefaults(t *testing.T) {
	// The db-managed bug: cost.prices is nil at runtime; the tracker must still
	// price known models from the compiled-in defaults (else cost is always $0).
	tr := New(config.CostConfig{Enabled: true}) // Prices nil
	if got := tr.Calculate("claude-sonnet-4-6", llm.Usage{InputTokens: 1_000_000}); got <= 0 {
		t.Errorf("nil-prices cost for claude-sonnet-4-6 = %v, want >0", got)
	}
	if got := tr.Calculate("deepseek-v4-pro", llm.Usage{OutputTokens: 1_000_000}); got <= 0 {
		t.Errorf("nil-prices cost for deepseek-v4-pro = %v, want >0", got)
	}
}

func TestNew_PartialPricesMergesDefaults(t *testing.T) {
	// A configured partial map overlays the defaults per-model (augments, not wipes).
	tr := New(config.CostConfig{
		Enabled: true,
		Prices:  map[string]config.ModelPrice{"claude-sonnet-4-6": {InputPerMillion: 99.0, OutputPerMillion: 99.0}},
	})
	if got := tr.Calculate("claude-sonnet-4-6", llm.Usage{InputTokens: 1_000_000}); !approx(got, 99.0) {
		t.Errorf("custom override = %v, want 99.0", got)
	}
	// A model not overridden still uses the compiled-in default.
	if got := tr.Calculate("deepseek-v4-pro", llm.Usage{InputTokens: 1_000_000}); !approx(got, 0.435) {
		t.Errorf("default for deepseek-v4-pro = %v, want 0.435", got)
	}
}
