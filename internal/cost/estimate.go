package cost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/llm"
)

const PriceVersion = "2026-10-05-conservative-peak-v1"

// Estimate is an indicative USD amount, never a provider invoice. USD remains
// nil when price or usage is unavailable; a known zero is a separate state.
type Estimate struct {
	USD          *float64  `json:"usd"`
	Known        bool      `json:"known"`
	Status       string    `json:"status"`
	PriceVersion string    `json:"price_version"`
	Source       string    `json:"source"`
	At           time.Time `json:"at"`
}

func (t *Tracker) Estimate(model string, usage llm.Usage, at time.Time) Estimate {
	e := Estimate{Status: "price_unknown", At: at.UTC(), PriceVersion: PriceVersion}
	price, ok := t.prices[model]
	if !ok {
		return e
	}
	for _, rate := range []float64{price.InputPerMillion, price.OutputPerMillion, price.CacheHitPerMillion} {
		if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
			e.Status = "price_invalid"
			return e
		}
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheReadInputTokens < 0 || usage.CacheCreationInputTokens < 0 {
		e.Status = "usage_invalid"
		return e
	}
	hitRate := price.CacheHitPerMillion
	if hitRate == 0 {
		hitRate = price.InputPerMillion
	}
	amount := ((float64(usage.InputTokens)+float64(usage.CacheCreationInputTokens))*price.InputPerMillion +
		float64(usage.CacheReadInputTokens)*hitRate + float64(usage.OutputTokens)*price.OutputPerMillion) / 1_000_000
	if math.IsInf(amount, 0) || math.IsNaN(amount) {
		e.Status = "usage_invalid"
		return e
	}
	e.USD, e.Known, e.Status = &amount, true, "estimated"
	switch {
	case model == "deepseek-chat" || model == "deepseek-reasoner":
		e.Source, e.Status = "compiled_legacy_prices", "legacy_estimate"
	case strings.HasPrefix(model, "deepseek"):
		e.Source, e.Status = "https://api-docs.deepseek.com/quick_start/pricing/", "conservative_peak_estimate"
	case strings.HasPrefix(model, "glm-"):
		e.Source = "https://docs.z.ai/guides/overview/pricing"
	default:
		e.Source, e.Status = "compiled_legacy_prices", "legacy_estimate"
	}
	if t.customPrices[model] {
		raw, _ := json.Marshal(price)
		hash := sha256.Sum256(raw)
		e.Source, e.Status, e.PriceVersion = "configured_override", "configured_estimate", "configured:"+hex.EncodeToString(hash[:8])
	}
	return e
}

func (t *Tracker) EstimateAttempt(a llm.Attempt) Estimate {
	if a.Status == "rejected" && !a.ChargeUnknown {
		zero := 0.0
		return Estimate{USD: &zero, Known: true, Status: "local_rejection", At: a.StartedAt.UTC(), PriceVersion: "no-submission", Source: "local_validation"}
	}
	if a.Cached {
		zero := 0.0
		return Estimate{USD: &zero, Known: true, Status: "cache_hit", At: a.StartedAt.UTC(), PriceVersion: "local-cache", Source: "local_response_cache"}
	}
	if !a.UsageAvailable || a.ChargeUnknown {
		return Estimate{Status: "usage_unavailable", At: a.StartedAt.UTC(), PriceVersion: PriceVersion}
	}
	model := a.Model
	if !a.ModelVerified && a.RequestedModel != "" {
		model = a.RequestedModel
	}
	e := t.Estimate(model, a.Usage, a.StartedAt)
	if e.Known && !a.ModelVerified {
		e.Status += "_model_unverified"
	}
	return e
}

// DeepSeekPeakWindow reports the documented weekday UTC time windows only.
// China public holidays can change the billing window. Estimates deliberately
// use peak rates at every hour until a versioned holiday calendar is available.
func DeepSeekPeakWindow(at time.Time) bool {
	u := at.UTC()
	if u.Weekday() == time.Saturday || u.Weekday() == time.Sunday {
		return false
	}
	return (u.Hour() >= 1 && u.Hour() < 4) || (u.Hour() >= 6 && u.Hour() < 10)
}
