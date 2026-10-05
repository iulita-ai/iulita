package metrics

import (
	"context"
	"math"

	"github.com/iulita-ai/iulita/internal/config"

	"github.com/iulita-ai/iulita/internal/eventbus"
)

// RegisterSubscribers registers event bus subscribers that update Prometheus metrics
// on each relevant event.
func (m *Metrics) RegisterSubscribers(bus *eventbus.Bus) {
	bus.Subscribe(eventbus.LLMUsage, func(_ context.Context, evt eventbus.Event) error {
		p, ok := evt.Payload.(eventbus.LLMUsagePayload)
		if !ok {
			return nil
		}
		provider := boundedProvider(p.Provider)
		input := positiveTokens(p.InputTokens) + positiveTokens(p.CacheReadInputTokens) + positiveTokens(p.CacheCreationInputTokens)
		m.LLMTokensInput.WithLabelValues(provider).Add(input)
		m.LLMTokensOutput.WithLabelValues(provider).Add(positiveTokens(p.OutputTokens))
		if p.AttemptID != "" {
			model := boundedModel(p.Model)
			status := boundedCategory(p.Status, "success", "error", "cancelled", "incomplete", "cache_hit", "rejected")
			m.LLMAttempts.WithLabelValues(provider, model,
				boundedCategory(p.Role, "everyday", "complex", "vision", "background", "classifier"),
				boundedCategory(p.Operation, "chat", "conversation", "completion", "classifier", "probe", "compression", "background", "agent", "job", "delegate", "synthesis", "heartbeat", "insight", "techfact", "bookmark_refine", "skill-review"), status).Inc()
			m.LLMRequests.WithLabelValues(provider, model, status).Inc()
			if d := p.CompletedAt.Sub(p.StartedAt).Seconds(); d >= 0 && !p.StartedAt.IsZero() {
				m.LLMLatency.WithLabelValues(provider).Observe(d)
			}
			if p.CostEstimate == nil || !p.CostEstimate.Known || p.CostEstimate.USD == nil {
				m.LLMUnknownCost.WithLabelValues(provider).Inc()
			} else if v := *p.CostEstimate.USD; v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
				m.LLMCostUSD.Add(v)
			}
			if p.Cached {
				m.CacheHits.WithLabelValues("response").Inc()
			}
		}
		return nil
	})

	bus.Subscribe(eventbus.SkillExecuted, func(_ context.Context, evt eventbus.Event) error {
		p, ok := evt.Payload.(eventbus.SkillExecutedPayload)
		if !ok {
			return nil
		}
		status := "success"
		if !p.Success {
			status = "error"
		}
		m.SkillExecutions.WithLabelValues(p.SkillName, status).Inc()
		return nil
	})

	bus.Subscribe(eventbus.TaskCompleted, func(_ context.Context, evt eventbus.Event) error {
		p, ok := evt.Payload.(eventbus.TaskCompletedPayload)
		if !ok {
			return nil
		}
		m.TasksTotal.WithLabelValues(p.TaskType, "completed").Inc()
		return nil
	})

	bus.Subscribe(eventbus.TaskFailed, func(_ context.Context, evt eventbus.Event) error {
		p, ok := evt.Payload.(eventbus.TaskFailedPayload)
		if !ok {
			return nil
		}
		m.TasksTotal.WithLabelValues(p.TaskType, "failed").Inc()
		return nil
	})

	bus.Subscribe(eventbus.MessageReceived, func(_ context.Context, evt eventbus.Event) error {
		_, ok := evt.Payload.(eventbus.MessageReceivedPayload)
		if !ok {
			return nil
		}
		m.MessagesTotal.WithLabelValues("inbound").Inc()
		return nil
	})

	bus.Subscribe(eventbus.ResponseSent, func(_ context.Context, evt eventbus.Event) error {
		_, ok := evt.Payload.(eventbus.ResponseSentPayload)
		if !ok {
			return nil
		}
		m.MessagesTotal.WithLabelValues("outbound").Inc()
		return nil
	})

	// Claude export import lifecycle.
	bus.Subscribe(eventbus.ImportStarted, func(_ context.Context, _ eventbus.Event) error {
		m.ImportsInFlight.Inc()
		return nil
	})
	bus.Subscribe(eventbus.ImportDone, func(_ context.Context, evt eventbus.Event) error {
		m.ImportsInFlight.Dec()
		p, ok := evt.Payload.(eventbus.ImportDonePayload)
		if !ok {
			return nil
		}
		m.ImportDuration.Observe(p.DurationSeconds)
		m.ImportRowsInserted.Add(float64(p.MessagesStored + p.Facts))
		m.ImportChunks.Add(float64(p.ChunksEmbedded))
		return nil
	})
	bus.Subscribe(eventbus.ImportFailed, func(_ context.Context, _ eventbus.Event) error {
		m.ImportsInFlight.Dec()
		m.ImportFailures.Inc()
		return nil
	})
	// Reclaim/abort: balance the gauge without counting a failure.
	bus.Subscribe(eventbus.ImportAborted, func(_ context.Context, _ eventbus.Event) error {
		m.ImportsInFlight.Dec()
		return nil
	})

	// Slack observability.
	bus.Subscribe(eventbus.SlackSearch, func(_ context.Context, evt eventbus.Event) error {
		p, ok := evt.Payload.(eventbus.SlackSearchPayload)
		if !ok {
			return nil
		}
		m.SlackUserSearches.WithLabelValues(p.Outcome).Inc()
		if p.ResultCount > 0 {
			m.SlackSearchResults.Add(float64(p.ResultCount))
		}
		return nil
	})
	bus.Subscribe(eventbus.SlackTokenRefresh, func(_ context.Context, evt eventbus.Event) error {
		if p, ok := evt.Payload.(eventbus.SlackTokenRefreshPayload); ok {
			m.SlackUserTokenRefresh.WithLabelValues(p.Outcome).Inc()
		}
		return nil
	})
	bus.Subscribe(eventbus.SlackPost, func(_ context.Context, evt eventbus.Event) error {
		p, ok := evt.Payload.(eventbus.SlackPostPayload)
		if !ok {
			return nil
		}
		if p.Success {
			m.SlackAutoposts.WithLabelValues(p.Mode).Inc()
		} else {
			// Decision carries the true failure kind (blocked_guardrail, denied,
			// blocked_secret, error, discarded, approval_failed), not the post mode.
			m.SlackPostFailures.WithLabelValues(p.Decision).Inc()
		}
		return nil
	})
	bus.Subscribe(eventbus.SlackReconnect, func(_ context.Context, evt eventbus.Event) error {
		if p, ok := evt.Payload.(eventbus.SlackReconnectPayload); ok {
			m.SlackSocketReconnects.WithLabelValues(p.InstanceID).Inc()
		}
		return nil
	})
}

func positiveTokens(v int64) float64 {
	if v < 0 {
		return 0
	}
	return float64(v)
}

func boundedCategory(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "other"
}

func boundedProvider(provider string) string {
	if provider == "anthropic" {
		return "claude"
	}
	return boundedCategory(provider, "claude", "openai", "deepseek", "zai", "ollama")
}

func boundedModel(model string) string {
	// Configured custom model/profile names and gateway aliases never create
	// unbounded time series or leak user-controlled names into metric labels.
	if _, ok := config.DefaultModelPrices()[model]; ok {
		return model
	}
	return "other"
}
