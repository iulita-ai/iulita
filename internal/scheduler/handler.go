package scheduler

import "context"

// TaskHandler processes a specific task type.
type TaskHandler interface {
	// Type returns the task type this handler processes (e.g. "insight.generate").
	Type() string
	// Handle executes the task with the given JSON payload.
	// Returns a result string (may be empty) or an error.
	Handle(ctx context.Context, payload string) (string, error)
}
