package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// ResponseCache interface for storage-backed response cache.
type ResponseCache interface {
	GetCachedResponse(ctx context.Context, hash string, maxAge time.Duration) (*CachedResponseEntry, error)
	SaveCachedResponse(ctx context.Context, hash, model, response, usageJSON string) error
	EvictResponseCache(ctx context.Context, maxEntries int) error
}

// CachedResponseEntry holds a cached LLM response.
type CachedResponseEntry struct {
	Response  string
	UsageJSON string
	HitCount  int
}

// CachingProvider wraps a Provider with response caching.
type CachingProvider struct {
	inner    Provider
	cache    ResponseCache
	ttl      time.Duration
	maxItems int
}

// NewCachingProvider creates a caching provider wrapper.
func NewCachingProvider(inner Provider, cache ResponseCache, ttl time.Duration, maxItems int) *CachingProvider {
	if ttl <= 0 {
		ttl = 60 * time.Minute
	}
	if maxItems <= 0 {
		maxItems = 1000
	}
	return &CachingProvider{
		inner:    inner,
		cache:    cache,
		ttl:      ttl,
		maxItems: maxItems,
	}
}

// responseCacheKey computes a SHA-256 cache key from the request.
func responseCacheKey(req Request) string {
	systemPrefix := req.SystemPrompt
	if len(systemPrefix) > 200 {
		systemPrefix = systemPrefix[:200]
	}
	raw := "||" + systemPrefix + "|" + req.Message
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// Complete checks cache first, calls inner on miss, and saves the result.
func (p *CachingProvider) Complete(ctx context.Context, req Request) (Response, error) {
	// Skip cache for requests with tools (non-deterministic).
	if len(req.Tools) > 0 || len(req.ToolExchanges) > 0 {
		return p.inner.Complete(ctx, req)
	}

	key := responseCacheKey(req)

	// Check cache.
	entry, err := p.cache.GetCachedResponse(ctx, key, p.ttl)
	if err == nil && entry != nil {
		var usage Usage
		if entry.UsageJSON != "" {
			_ = json.Unmarshal([]byte(entry.UsageJSON), &usage)
		}
		return Response{
			Content: entry.Response,
			Usage:   usage,
		}, nil
	}

	// Cache miss — call inner provider.
	resp, err := p.inner.Complete(ctx, req)
	if err != nil {
		return resp, err
	}

	// Save to cache (best effort).
	usageJSON, _ := json.Marshal(resp.Usage)
	_ = p.cache.SaveCachedResponse(ctx, key, "", resp.Content, string(usageJSON))

	// Evict old entries (best effort).
	_ = p.cache.EvictResponseCache(ctx, p.maxItems)

	return resp, nil
}

// CompleteStream delegates to inner provider without caching (streaming is not cached).
func (p *CachingProvider) CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error) {
	if sp, ok := p.inner.(StreamingProvider); ok {
		return sp.CompleteStream(ctx, req, callback)
	}
	return p.Complete(ctx, req)
}
