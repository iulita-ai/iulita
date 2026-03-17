package agent

import "context"

type depthKey struct{}
type currentTimeKey struct{}

// MaxDepth is the maximum allowed nesting depth for sub-agents.
// Depth 0 = parent assistant, depth 1 = sub-agent (maximum).
const MaxDepth = 1

// WithDepth returns a context with the agent depth set.
func WithDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, depthKey{}, depth)
}

// DepthFrom extracts the current agent depth from the context.
// Returns 0 if not set (parent assistant level).
func DepthFrom(ctx context.Context) int {
	v, _ := ctx.Value(depthKey{}).(int)
	return v
}

// WithCurrentTime returns a context with the formatted current time string.
// This is used to inject the parent assistant's resolved time/timezone into sub-agents.
func WithCurrentTime(ctx context.Context, currentTime string) context.Context {
	return context.WithValue(ctx, currentTimeKey{}, currentTime)
}

// CurrentTimeFrom extracts the current time string from the context.
// Returns empty string if not set.
func CurrentTimeFrom(ctx context.Context) string {
	v, _ := ctx.Value(currentTimeKey{}).(string)
	return v
}
