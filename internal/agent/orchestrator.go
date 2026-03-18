package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
)

// Orchestrator runs multiple sub-agents in parallel and collects results.
type Orchestrator struct {
	provider llm.Provider
	registry *skill.Registry
	notifier channel.StatusNotifier
	bus      *eventbus.Bus
	logger   *zap.Logger
}

// NewOrchestrator constructs an Orchestrator.
func NewOrchestrator(
	provider llm.Provider,
	registry *skill.Registry,
	notifier channel.StatusNotifier,
	bus *eventbus.Bus,
	logger *zap.Logger,
) *Orchestrator {
	return &Orchestrator{
		provider: provider,
		registry: registry,
		notifier: notifier,
		bus:      bus,
		logger:   logger,
	}
}

// Run launches all specs in parallel (bounded by budget.MaxAgents).
// It returns all results regardless of individual agent errors.
// The returned error is non-nil only if the context was canceled before any agent could run.
func (o *Orchestrator) Run(ctx context.Context, chatID string, specs []AgentSpec, budget Budget) ([]AgentResult, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	maxAgents := budget.EffectiveMaxAgents()
	if len(specs) > maxAgents {
		o.logger.Warn("clamping agent count to max",
			zap.Int("requested", len(specs)),
			zap.Int("max", maxAgents))
		specs = specs[:maxAgents]
	}

	start := time.Now()

	// Emit orchestration started.
	o.emitStatus(ctx, chatID, EventOrchestrationStarted, map[string]string{
		"agent_count": fmt.Sprintf("%d", len(specs)),
	})
	if o.bus != nil {
		o.bus.Publish(ctx, eventbus.Event{
			Type: eventbus.AgentOrchestrationStarted,
			Payload: eventbus.OrchestrationStartedPayload{
				ChatID:     chatID,
				AgentCount: len(specs),
			},
		})
	}

	// Initialize shared token budget.
	var sharedTokens *atomic.Int64
	if budget.MaxTokens > 0 {
		sharedTokens = &atomic.Int64{}
		sharedTokens.Store(budget.MaxTokens)
	}

	// Pre-allocate results — each goroutine writes to its own index.
	results := make([]AgentResult, len(specs))

	// Semaphore for parallelism control.
	sem := make(chan struct{}, maxAgents)

	eg, egCtx := errgroup.WithContext(ctx)

	for i, spec := range specs {
		i, spec := i, spec // capture loop variables
		eg.Go(func() error {
			// Acquire semaphore.
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-egCtx.Done():
				results[i] = AgentResult{
					ID:   spec.ID,
					Type: spec.Type,
					Err:  egCtx.Err(),
				}
				return nil
			}

			// Emit agent started.
			o.emitStatus(egCtx, chatID, EventAgentStarted, map[string]string{
				"agent_id":   spec.ID,
				"agent_type": string(spec.Type),
			})

			// Per-agent timeout and depth injection.
			agentCtx, cancel := context.WithTimeout(egCtx, budget.EffectiveTimeout())
			defer cancel()
			agentCtx = WithDepth(agentCtx, DepthFrom(ctx)+1)

			// Run the sub-agent.
			runner := NewRunner(o.provider, o.registry, o.notifier, o.bus, chatID, o.logger)
			runner.SetUserID(skill.UserIDFrom(ctx))
			results[i] = runner.Run(agentCtx, spec, budget, sharedTokens)

			// Emit completion or failure using a fresh context so events
			// are delivered even if the parent context was canceled or expired.
			statusCtx := context.Background()
			if results[i].Err != nil {
				o.emitStatus(statusCtx, chatID, EventAgentFailed, map[string]string{
					"agent_id": spec.ID,
					"error":    results[i].Err.Error(),
				})
			} else {
				o.emitStatus(statusCtx, chatID, EventAgentCompleted, map[string]string{
					"agent_id":    spec.ID,
					"tokens":      fmt.Sprintf("%d", results[i].Tokens),
					"duration_ms": fmt.Sprintf("%d", results[i].Duration.Milliseconds()),
				})
			}

			return nil // individual errors are stored in AgentResult.Err
		})
	}

	_ = eg.Wait() //nolint:errcheck // individual errors stored in AgentResult.Err, errgroup error is always nil

	totalDuration := time.Since(start)

	// Compute summary stats.
	var successCount int
	var totalTokens int64
	for _, r := range results {
		if r.Err == nil {
			successCount++
		}
		totalTokens += r.Tokens
	}

	// Emit orchestration done using a fresh context with a short timeout.
	// context.WithoutCancel still inherits the parent's deadline, which may have
	// expired. Use a fresh background context to guarantee delivery.
	doneCtx, doneCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer doneCancel()
	o.emitStatus(doneCtx, chatID, EventOrchestrationDone, map[string]string{
		"success_count": fmt.Sprintf("%d", successCount),
		"total_tokens":  fmt.Sprintf("%d", totalTokens),
		"duration_ms":   fmt.Sprintf("%d", totalDuration.Milliseconds()),
	})
	if o.bus != nil {
		o.bus.Publish(doneCtx, eventbus.Event{
			Type: eventbus.AgentOrchestrationDone,
			Payload: eventbus.OrchestrationDonePayload{
				ChatID:       chatID,
				AgentCount:   len(specs),
				SuccessCount: successCount,
				TotalTokens:  totalTokens,
				DurationMs:   totalDuration.Milliseconds(),
			},
		})
	}

	return results, nil
}

// emitStatus sends a status event if a notifier is available.
func (o *Orchestrator) emitStatus(ctx context.Context, chatID, eventType string, data map[string]string) {
	if o.notifier == nil {
		return
	}
	_ = o.notifier.NotifyStatus(ctx, chatID, channel.StatusEvent{ //nolint:errcheck // status notifications are best-effort
		Type: eventType,
		Data: data,
	})
}
