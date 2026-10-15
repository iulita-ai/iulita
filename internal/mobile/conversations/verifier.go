package conversations

import "context"

type verifierKey struct{}

// WithVerifier attaches a server-built authorization predicate to an account DB
// critical section. It must perform only bounded local OAuth reads, never take
// another account reservation or call a provider. Public OAuth mutations follow
// the same account -> OAuth lock order.
func WithVerifier(ctx context.Context, fn func(context.Context) error) context.Context {
	return context.WithValue(ctx, verifierKey{}, fn)
}
func verify(ctx context.Context) error {
	if fn, ok := ctx.Value(verifierKey{}).(func(context.Context) error); ok && fn != nil {
		return fn(ctx)
	}
	return nil
}
