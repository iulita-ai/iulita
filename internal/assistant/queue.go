package assistant

import (
	"github.com/iulita-ai/iulita/internal/channel"
)

// MessagePriority defines the processing order for injected messages.
type MessagePriority int

const (
	// PrioritySteer is highest priority — processed before normal messages.
	// Use for admin commands, urgent corrections, cancellation signals.
	PrioritySteer MessagePriority = iota

	// PriorityFollowUp is lowest priority — processed when agent is idle.
	// Use for cron triggers, heartbeat prompts, non-urgent notifications.
	PriorityFollowUp
)

const (
	steerBufferSize    = 16
	followUpBufferSize = 64
)

// InjectedMessage wraps a channel message with priority and optional callback.
type InjectedMessage struct {
	channel.IncomingMessage
	Priority   MessagePriority
	ResponseFn func(resp string, err error) // optional callback with result
}

// Injector allows external components (scheduler, cron, admin) to inject
// messages into the assistant's processing queues.
type Injector interface {
	// Steer injects a high-priority message that is processed before the
	// next normal message. Non-blocking — drops if queue is full.
	Steer(msg channel.IncomingMessage, cb func(string, error))

	// FollowUp injects a low-priority message that is processed when idle.
	// Non-blocking — drops if queue is full.
	FollowUp(msg channel.IncomingMessage, cb func(string, error))
}
