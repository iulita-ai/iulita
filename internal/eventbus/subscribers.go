package eventbus

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/cost"
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
			UserID:     p.UserID,
			Action:     "skill.executed",
			Detail:     p.SkillName,
			Success:    p.Success,
			DurationMs: p.DurationMs,
		})
	})
	logger.Info("audit subscriber registered")
}

// RegisterSkillTelemetrySubscriber persists each skill execution into the
// skill_executions ledger — a typed, queryable outcome record (success rates,
// latency, usage frequency) distinct from the generic audit_log.
func RegisterSkillTelemetrySubscriber(bus *Bus, store storage.Repository, logger *zap.Logger) {
	bus.SubscribeAsync(SkillExecuted, func(ctx context.Context, evt Event) error {
		p, ok := evt.Payload.(SkillExecutedPayload)
		if !ok {
			return nil
		}
		origin := p.Origin
		if origin == "" {
			origin = domain.SkillOriginMain
		}
		return store.SaveSkillExecution(ctx, &domain.SkillExecution{
			ChatID:     p.ChatID,
			UserID:     p.UserID,
			SkillName:  p.SkillName,
			ToolCallID: p.ToolCallID,
			Success:    p.Success,
			DurationMs: p.DurationMs,
			Iteration:  p.Iteration,
			Origin:     origin,
		})
	})
	logger.Info("skill telemetry subscriber registered")
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
		at := p.StartedAt
		if at.IsZero() {
			at = time.Now().UTC()
		}
		estimate := p.CostEstimate
		// Only legacy, unobserved events may use the calculator fallback. New
		// attempt events carry their frozen estimate, even when it is unknown.
		if estimate == nil && p.AttemptID == "" {
			if calc, ok := costCalc.(interface {
				Estimate(string, llm.Usage, time.Time) cost.Estimate
			}); ok {
				e := calc.Estimate(p.Model, llm.Usage{InputTokens: p.InputTokens, OutputTokens: p.OutputTokens,
					CacheReadInputTokens: p.CacheReadInputTokens, CacheCreationInputTokens: p.CacheCreationInputTokens}, at)
				estimate = &e
			}
		}
		if estimate == nil {
			estimate = &cost.Estimate{Status: "price_unknown", At: at}
		}
		var costUSD float64
		knownCost, unknownCost := int64(0), int64(1)
		if estimate.Known && estimate.USD != nil {
			costUSD, knownCost, unknownCost = *estimate.USD, 1, 0
		}
		unknownUsage := int64(0)
		if p.AttemptID != "" && !p.UsageAvailable {
			unknownUsage = 1
		}
		rec := storage.UsageUpsert{
			ChatID:              p.ChatID,
			UserID:              p.UserID,
			Model:               p.Model,
			Provider:            p.Provider,
			Hour:                at.UTC().Truncate(time.Hour),
			InputTokens:         p.InputTokens,
			OutputTokens:        p.OutputTokens,
			CacheReadTokens:     p.CacheReadInputTokens,
			CacheCreationTokens: p.CacheCreationInputTokens,
			Requests:            1,
			CostUSD:             costUSD,
			CostKnownRequests:   knownCost, CostUnknownRequests: unknownCost, UsageUnknownRequests: unknownUsage,
		}
		if p.AttemptID != "" {
			if ledger, ok := store.(interface {
				SaveUsageAttempt(context.Context, *domain.LLMUsageAttempt, storage.UsageUpsert) error
			}); ok {
				return ledger.SaveUsageAttempt(ctx, &domain.LLMUsageAttempt{
					AttemptID: p.AttemptID, ChatID: p.ChatID, UserID: p.UserID, Provider: p.Provider,
					RequestedModel: p.RequestedModel, Model: p.Model, ModelVerified: p.ModelVerified,
					ProfileID: p.ProfileID, Role: p.Role, Operation: p.Operation, PolicyRevision: p.PolicyRevision,
					StartedAt: at, CompletedAt: p.CompletedAt, Status: p.Status, UsageAvailable: p.UsageAvailable,
					ChargeUnknown: p.ChargeUnknown, Cached: p.Cached, InputTokens: p.InputTokens,
					OutputTokens: p.OutputTokens, CacheReadTokens: p.CacheReadInputTokens, CacheCreationTokens: p.CacheCreationInputTokens,
					EstimatedUSD: estimate.USD, CostStatus: estimate.Status, PriceVersion: estimate.PriceVersion, PriceSource: estimate.Source,
				}, rec)
			}
		}
		return store.UpsertUsage(ctx, rec)
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

// CredentialChangeAdapter wraps the event bus to satisfy credential.ChangePublisher.
type CredentialChangeAdapter struct {
	Bus *Bus
}

// PublishCredentialChanged publishes a CredentialChanged event.
func (a *CredentialChangeAdapter) PublishCredentialChanged(ctx context.Context, name string) {
	a.Bus.Publish(ctx, Event{
		Type:    CredentialChanged,
		Payload: CredentialChangedPayload{Name: name},
	})
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
