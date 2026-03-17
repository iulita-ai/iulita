package skill

import (
	"context"
	"time"
)

// DeadlineExtender replaces the context deadline with a new one of duration d.
// Implementations should use context.WithoutCancel to break the parent deadline
// chain while preserving context values (user ID, chat ID, locale, etc.).
type DeadlineExtender func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc)

type ctxDeadlineExtenderKey struct{}

// WithDeadlineExtender injects a DeadlineExtender into the context.
func WithDeadlineExtender(ctx context.Context, fn DeadlineExtender) context.Context {
	return context.WithValue(ctx, ctxDeadlineExtenderKey{}, fn)
}

// DeadlineExtenderFrom retrieves the DeadlineExtender from context.
// Returns nil if not set.
func DeadlineExtenderFrom(ctx context.Context) DeadlineExtender {
	fn, ok := ctx.Value(ctxDeadlineExtenderKey{}).(DeadlineExtender)
	if !ok {
		return nil
	}
	return fn
}

// DefaultDeadlineExtender replaces the deadline by detaching from the parent
// timeout chain while preserving all context values (user ID, chat ID, locale,
// depth, channel caps, etc.). Uses context.WithoutCancel as the base so the
// parent's deadline/cancellation no longer applies.
func DefaultDeadlineExtender(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), d)
}

// TimeoutDeclarer is an optional interface for skills that require a longer
// execution deadline than the default request timeout. When a skill implements
// this and a DeadlineExtender is available in the context, the assistant will
// replace the execution context with a new one using the declared duration.
type TimeoutDeclarer interface {
	RequestTimeout() time.Duration
}
