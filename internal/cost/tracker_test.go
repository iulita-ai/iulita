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
	tr := New(config.CostConfig{Enabled: true, Prices: map[string]config.ModelPrice{}})
	if got := tr.Calculate("deepseek-v4-pro", llm.Usage{InputTokens: 1000}); got != 0 {
		t.Errorf("unknown model cost = %v, want 0", got)
	}
}
