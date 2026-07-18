package scheduler

import "context"

type ctxKey int

const taskIDKey ctxKey = iota

// WithTaskID returns a context carrying the numeric task ID. The worker sets this
// before invoking a handler so long-running handlers can heartbeat via TouchTask.
func WithTaskID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, taskIDKey, id)
}

// TaskIDFrom returns the task ID stored in ctx, or 0 if absent.
func TaskIDFrom(ctx context.Context) int64 {
	if v, ok := ctx.Value(taskIDKey).(int64); ok {
		return v
	}
	return 0
}
