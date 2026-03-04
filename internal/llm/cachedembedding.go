package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// EmbeddingCache interface for storage-backed embedding cache.
type EmbeddingCache interface {
	GetCachedEmbedding(ctx context.Context, hash string) ([]float32, error)
	SaveCachedEmbedding(ctx context.Context, hash string, embedding []float32) error
	EvictOldEmbeddings(ctx context.Context, maxEntries int) error
}

// CachedEmbeddingProvider wraps an EmbeddingProvider with SHA-256 content hash caching.
type CachedEmbeddingProvider struct {
	inner    EmbeddingProvider
	cache    EmbeddingCache
	maxItems int
}

// NewCachedEmbeddingProvider creates a cached embedding provider.
func NewCachedEmbeddingProvider(inner EmbeddingProvider, cache EmbeddingCache, maxItems int) *CachedEmbeddingProvider {
	if maxItems <= 0 {
		maxItems = 10000
	}
	return &CachedEmbeddingProvider{
		inner:    inner,
		cache:    cache,
		maxItems: maxItems,
	}
}

// Embed checks cache for each text (SHA-256 hash), only calls inner for cache misses.
func (p *CachedEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	results := make([][]float32, len(texts))
	hashes := make([]string, len(texts))

	// Compute hashes and check cache for each text.
	var missIndices []int
	var missTexts []string

	for i, text := range texts {
		h := sha256.Sum256([]byte(text))
		hashes[i] = hex.EncodeToString(h[:])

		cached, err := p.cache.GetCachedEmbedding(ctx, hashes[i])
		if err == nil && cached != nil {
			results[i] = cached
		} else {
			missIndices = append(missIndices, i)
			missTexts = append(missTexts, text)
		}
	}

	// If all were cached, return immediately.
	if len(missTexts) == 0 {
		return results, nil
	}

	// Call inner provider for cache misses only.
	embeddings, err := p.inner.Embed(ctx, missTexts)
	if err != nil {
		return nil, fmt.Errorf("embedding cache miss: %w", err)
	}

	if len(embeddings) != len(missTexts) {
		return nil, fmt.Errorf("embedding provider returned %d results for %d texts", len(embeddings), len(missTexts))
	}

	// Save results to cache and place in correct positions.
	for j, idx := range missIndices {
		results[idx] = embeddings[j]
		// Save to cache (best effort, don't fail on cache errors).
		_ = p.cache.SaveCachedEmbedding(ctx, hashes[idx], embeddings[j])
	}

	// Evict old entries if needed (best effort).
	_ = p.cache.EvictOldEmbeddings(ctx, p.maxItems)

	return results, nil
}

// Dimensions returns the embedding dimensionality from the inner provider.
func (p *CachedEmbeddingProvider) Dimensions() int {
	return p.inner.Dimensions()
}
