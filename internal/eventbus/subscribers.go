package eventbus

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
)

// RegisterAuditSubscriber logs skill executions to the audit_log table.
func RegisterAuditSubscriber(bus *Bus, store storage.Repository, logger *zap.Logger) {
	bus.SubscribeAsync(SkillExecuted, func(ctx context.Context, evt Event) error {
		p, ok := evt.Payload.(SkillExecutedPayload)
		if !ok {
			return nil
		}
		return store.SaveAuditEntry(ctx, &domain.AuditEntry{
			ChatID:     p.ChatID,
			Action:     "skill.executed",
			Detail:     p.SkillName,
			Success:    p.Success,
			DurationMs: p.DurationMs,
		})
	})
	logger.Info("audit subscriber registered")
}

// UsageCostCalculator computes the cost for a given model and usage.
// Implemented by cost.Tracker. May be nil when cost tracking is disabled.
type UsageCostCalculator interface {
	Calculate(model string, usage llm.Usage) float64
}

// RegisterUsageSubscriber aggregates per-chat token usage in usage_stats table.
// It is the single writer for all usage data — replaces the old dual-subscriber setup.
// costCalc may be nil when cost tracking is disabled.
func RegisterUsageSubscriber(bus *Bus, store storage.Repository, costCalc UsageCostCalculator, logger *zap.Logger) {
	bus.SubscribeAsync(LLMUsage, func(ctx context.Context, evt Event) error {
		p, ok := evt.Payload.(LLMUsagePayload)
		if !ok {
			return nil
		}
		var costUSD float64
		if costCalc != nil {
			costUSD = costCalc.Calculate(p.Model, llm.Usage{
				InputTokens:              p.InputTokens,
				OutputTokens:             p.OutputTokens,
				CacheReadInputTokens:     p.CacheReadInputTokens,
				CacheCreationInputTokens: p.CacheCreationInputTokens,
			})
		}
		return store.UpsertUsage(ctx, storage.UsageUpsert{
			ChatID:              p.ChatID,
			UserID:              p.UserID,
			Model:               p.Model,
			Provider:            p.Provider,
			Hour:                time.Now().Truncate(time.Hour),
			InputTokens:         p.InputTokens,
			OutputTokens:        p.OutputTokens,
			CacheReadTokens:     p.CacheReadInputTokens,
			CacheCreationTokens: p.CacheCreationInputTokens,
			Requests:            1,
			CostUSD:             costUSD,
		})
	})
	logger.Info("usage metrics subscriber registered")
}

// RegisterFailureAlertSubscriber sends a Telegram notification after N
// consecutive failures of the same task type.
func RegisterFailureAlertSubscriber(bus *Bus, sender channel.MessageSender, threshold int, logger *zap.Logger) {
	if threshold <= 0 {
		threshold = 3
	}

	var mu sync.Mutex
	// key: taskType → consecutive failure count
	failures := make(map[string]int)

	bus.Subscribe(TaskCompleted, func(_ context.Context, evt Event) error {
		p, ok := evt.Payload.(TaskCompletedPayload)
		if !ok {
			return nil
		}
		mu.Lock()
		failures[p.TaskType] = 0
		mu.Unlock()
		return nil
	})

	bus.Subscribe(TaskFailed, func(ctx context.Context, evt Event) error {
		p, ok := evt.Payload.(TaskFailedPayload)
		if !ok {
			return nil
		}

		mu.Lock()
		failures[p.TaskType]++
		count := failures[p.TaskType]
		mu.Unlock()

		// Alert only when crossing the threshold (not on every failure after).
		if count == threshold {
			msg := fmt.Sprintf("Task %q failed %d times in a row.\nLast error: %s",
				p.TaskType, count, p.Error)
			logger.Warn("task failure threshold reached",
				zap.String("type", p.TaskType),
				zap.Int("consecutive", count))

			if p.ChatID != "" {
				if err := sender.SendMessage(ctx, p.ChatID, msg); err != nil {
					logger.Error("failed to send failure alert", zap.Error(err))
				}
			}
		}
		return nil
	})

	logger.Info("failure alert subscriber registered", zap.Int("threshold", threshold))
}

// RegisterConfigAuditSubscriber logs all config changes to the audit_log table.
func RegisterConfigAuditSubscriber(bus *Bus, store storage.Repository, logger *zap.Logger) {
	bus.SubscribeAsync(ConfigChanged, func(ctx context.Context, evt Event) error {
		p, ok := evt.Payload.(ConfigChangedPayload)
		if !ok {
			return nil
		}
		return store.SaveAuditEntry(ctx, &domain.AuditEntry{
			ChatID:  "system",
			Action:  "config.changed",
			Detail:  p.Key,
			Success: true,
		})
	})
	logger.Info("config audit subscriber registered")
}

// ConfigChangeAdapter wraps the event bus to satisfy config.ChangePublisher.
type ConfigChangeAdapter struct {
	Bus *Bus
}

// PublishConfigChanged publishes a ConfigChanged event.
func (a *ConfigChangeAdapter) PublishConfigChanged(ctx context.Context, key string) {
	a.Bus.Publish(ctx, Event{
		Type:    ConfigChanged,
		Payload: ConfigChangedPayload{Key: key},
	})
}
