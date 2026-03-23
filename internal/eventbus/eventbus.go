package eventbus

import (
	"context"
	"sync"

	"go.uber.org/zap"
)

// Event represents a typed event with arbitrary payload.
type Event struct {
	Type    string
	Payload any
}

// Common event types.
const (
	MessageReceived = "message.received"
	ResponseSent    = "response.sent"
	SkillExecuted   = "skill.executed"
	LLMUsage        = "llm.usage"
	TaskCompleted   = "task.completed"
	TaskFailed      = "task.failed"
	InsightCreated  = "insight.created"
	FactSaved       = "fact.saved"
	FactDeleted     = "fact.deleted"
	ConfigChanged   = "config.changed"

	// Credential management events.
	CredentialChanged = "credential.changed"

	// Multi-agent orchestration events.
	AgentOrchestrationStarted = "agent.orchestration.started"
	AgentOrchestrationDone    = "agent.orchestration.done"
)

// Handler processes an event. Returning an error logs a warning but does not
// affect other subscribers.
type Handler func(ctx context.Context, evt Event) error

type subscription struct {
	handler Handler
	async   bool
}

// Bus is an in-process publish/subscribe event bus.
type Bus struct {
	mu     sync.RWMutex
	subs   map[string][]subscription
	wg     sync.WaitGroup
	logger *zap.Logger
}

// New creates a new event bus.
func New(logger *zap.Logger) *Bus {
	return &Bus{
		subs:   make(map[string][]subscription),
		logger: logger,
	}
}

// Subscribe registers a synchronous handler for the given event type.
// Sync handlers block Publish until they return.
func (b *Bus) Subscribe(eventType string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[eventType] = append(b.subs[eventType], subscription{handler: h, async: false})
}

// SubscribeAsync registers an asynchronous handler that runs in a goroutine.
// The handler must not depend on the caller's context deadline.
func (b *Bus) SubscribeAsync(eventType string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[eventType] = append(b.subs[eventType], subscription{handler: h, async: true})
}

// Publish sends an event to all registered handlers.
// Sync handlers run in order; async handlers are spawned as goroutines.
func (b *Bus) Publish(ctx context.Context, evt Event) {
	b.mu.RLock()
	subs := b.subs[evt.Type]
	b.mu.RUnlock()

	for _, s := range subs {
		if s.async {
			b.wg.Add(1)
			go func(h Handler) {
				defer b.wg.Done()
				if err := h(context.WithoutCancel(ctx), evt); err != nil {
					b.logger.Warn("async event handler error",
						zap.String("event", evt.Type),
						zap.Error(err))
				}
			}(s.handler)
		} else {
			if err := s.handler(ctx, evt); err != nil {
				b.logger.Warn("sync event handler error",
					zap.String("event", evt.Type),
					zap.Error(err))
			}
		}
	}
}

// Shutdown waits for all in-flight async handlers to complete.
func (b *Bus) Shutdown() {
	b.wg.Wait()
}
