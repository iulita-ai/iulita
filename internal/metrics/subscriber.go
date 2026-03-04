package metrics

import (
	"context"

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
		// We don't have provider info in the payload, use "default" as label.
		m.LLMTokensInput.WithLabelValues("default").Add(float64(p.InputTokens))
		m.LLMTokensOutput.WithLabelValues("default").Add(float64(p.OutputTokens))
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
}
