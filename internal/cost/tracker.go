package cost

import (
	"sync"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/llm"
)

// Tracker calculates and tracks LLM API costs.
type Tracker struct {
	prices         map[string]config.ModelPrice
	dailyLimit     float64
	alertThreshold float64

	mu           sync.Mutex
	dailyCostUSD float64
	lastResetDay int // day of year for daily reset
}

// New creates a new cost tracker from configuration.
func New(cfg config.CostConfig) *Tracker {
	alertThreshold := cfg.AlertThreshold
	if alertThreshold <= 0 {
		alertThreshold = 0.8
	}
	prices := cfg.Prices
	if prices == nil {
		prices = make(map[string]config.ModelPrice)
	}
	return &Tracker{
		prices:         prices,
		dailyLimit:     cfg.DailyLimitUSD,
		alertThreshold: alertThreshold,
		lastResetDay:   time.Now().YearDay(),
	}
}

// Calculate returns the cost in USD for a given model and usage.
func (t *Tracker) Calculate(model string, usage llm.Usage) float64 {
	price, ok := t.prices[model]
	if !ok {
		return 0
	}

	inputTokens := float64(usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens)
	outputTokens := float64(usage.OutputTokens)

	inputCost := (inputTokens / 1_000_000) * price.InputPerMillion
	outputCost := (outputTokens / 1_000_000) * price.OutputPerMillion

	return inputCost + outputCost
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
	t.lastResetDay = time.Now().YearDay()
}

// maybeReset auto-resets if the day has changed. Must be called with mu held.
func (t *Tracker) maybeReset() {
	today := time.Now().YearDay()
	if today != t.lastResetDay {
		t.dailyCostUSD = 0
		t.lastResetDay = today
	}
}
