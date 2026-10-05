package cost

import (
	"math"
	"sync"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/llm"
)

// Tracker calculates and tracks LLM API costs.
type Tracker struct {
	prices         map[string]config.ModelPrice
	customPrices   map[string]bool
	dailyLimit     float64
	alertThreshold float64

	mu           sync.Mutex
	dailyCostUSD float64
	lastResetDay int // UTC calendar date, matching durable usage day boundaries
}

// New creates a new cost tracker from configuration.
func New(cfg config.CostConfig) *Tracker {
	alertThreshold := cfg.AlertThreshold
	if alertThreshold <= 0 {
		alertThreshold = 0.8
	}
	// Always start from the compiled-in price table so a fresh/db-managed install
	// (where cfg.Prices is nil — the price map can't ride the flat structToMap
	// koanf layer) still computes real costs. Any configured entries overlay the
	// defaults per-model, so a partial custom price map augments rather than wipes.
	prices := config.DefaultModelPrices()
	customPrices := make(map[string]bool)
	for model, p := range cfg.Prices {
		compiled, exists := prices[model]
		prices[model] = p
		// Defaults can arrive through the loaded Config too. Only a changed
		// rate or a new model is an actual configured pricing override.
		customPrices[model] = !exists || compiled != p
	}
	return &Tracker{
		prices:         prices,
		customPrices:   customPrices,
		dailyLimit:     cfg.DailyLimitUSD,
		alertThreshold: alertThreshold,
		lastResetDay:   budgetDay(time.Now()),
	}
}

// Calculate returns the cost in USD for a given model and usage.
func (t *Tracker) Calculate(model string, usage llm.Usage) float64 {
	e := t.Estimate(model, usage, time.Now())
	if e.USD == nil {
		return 0
	} // legacy API only; new callers inspect Known.
	return *e.USD
}

// TrackEstimate adds the request-time estimate without repricing an async event.
// Unknown amounts do not lower the limit or turn into a known free request.
func (t *Tracker) TrackEstimate(e Estimate) (exceeded bool, currentCost float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maybeReset()
	if e.Known && e.USD != nil && *e.USD >= 0 && !math.IsNaN(*e.USD) && !math.IsInf(*e.USD, 0) {
		t.dailyCostUSD += *e.USD
	}
	return t.dailyLimit > 0 && t.dailyCostUSD >= t.dailyLimit, t.dailyCostUSD
}

// Track adds cost and returns whether the daily limit is exceeded and the current cost.
func (t *Tracker) Track(model string, usage llm.Usage) (exceeded bool, currentCost float64) {
	cost := t.Calculate(model, usage)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.maybeReset()
	t.dailyCostUSD += cost

	if t.dailyLimit > 0 && t.dailyCostUSD >= t.dailyLimit {
		return true, t.dailyCostUSD
	}
	return false, t.dailyCostUSD
}

// IsExceeded checks if the daily limit is exceeded.
func (t *Tracker) IsExceeded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.maybeReset()
	if t.dailyLimit <= 0 {
		return false
	}
	return t.dailyCostUSD >= t.dailyLimit
}

// DailyCost returns the current daily cost in USD.
func (t *Tracker) DailyCost() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.maybeReset()
	return t.dailyCostUSD
}

// Reset resets the daily counter.
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.dailyCostUSD = 0
	t.lastResetDay = budgetDay(time.Now())
}

// maybeReset auto-resets if the day has changed. Must be called with mu held.
func (t *Tracker) maybeReset() {
	today := budgetDay(time.Now())
	if today != t.lastResetDay {
		t.dailyCostUSD = 0
		t.lastResetDay = today
	}
}

func budgetDay(at time.Time) int {
	u := at.UTC()
	return u.Year()*10_000 + int(u.Month())*100 + u.Day()
}
