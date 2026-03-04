package llm

import (
	"context"
	"fmt"
)

// FallbackProvider tries providers in order, falling back on error.
type FallbackProvider struct {
	providers []Provider
}

// NewFallbackProvider creates a provider that tries each provider in order.
func NewFallbackProvider(providers ...Provider) *FallbackProvider {
	return &FallbackProvider{providers: providers}
}

func (f *FallbackProvider) Complete(ctx context.Context, req Request) (Response, error) {
	var lastErr error
	for _, p := range f.providers {
		resp, err := p.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	return Response{}, fmt.Errorf("all providers failed, last error: %w", lastErr)
}
