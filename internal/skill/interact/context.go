package interact

import "context"

type ctxAskerKey struct{}

// WithPrompter returns a context enriched with a PromptAsker.
func WithPrompter(ctx context.Context, asker PromptAsker) context.Context {
	return context.WithValue(ctx, ctxAskerKey{}, asker)
}

// PrompterFrom extracts the PromptAsker from context.
// Returns NoopAsker if none is set.
func PrompterFrom(ctx context.Context) PromptAsker {
	if v, ok := ctx.Value(ctxAskerKey{}).(PromptAsker); ok && v != nil {
		return v
	}
	return NoopAsker{}
}
