package web

import "context"

// Searcher is the common interface for all search providers.
type Searcher interface {
	Search(ctx context.Context, query string, count int) ([]SearchResult, error)
}

// FallbackSearcher tries providers in order, returning the first success.
type FallbackSearcher struct {
	providers []Searcher
}

// NewFallbackSearcher creates a searcher that tries each provider in order.
func NewFallbackSearcher(providers ...Searcher) *FallbackSearcher {
	return &FallbackSearcher{providers: providers}
}

// Search tries each provider until one returns results.
func (f *FallbackSearcher) Search(ctx context.Context, query string, count int) ([]SearchResult, error) {
	var lastErr error
	for _, p := range f.providers {
		results, err := p.Search(ctx, query, count)
		if err == nil && len(results) > 0 {
			return results, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}
