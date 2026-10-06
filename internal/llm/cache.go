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
	// Versioned, unambiguous serialization includes both complete prompts and
	// the resolved profile/revision. Old, truncated keys are never reused.
	raw, err := json.Marshal(struct {
		Version                                                 int
		Scope, Identity, Profile, Hint, Static, System, Message string
		Thinking                                                int64
	}{2, req.CacheScope, req.CacheIdentity, req.ProfileID, req.RouteHint,
		req.StaticSystemPrompt, req.SystemPrompt, req.Message, req.ThinkingBudget})
	if err != nil {
		return "" // a serialization failure must never create a shared cache key
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// Complete checks cache first, calls inner on miss, and saves the result.
func (p *CachingProvider) Complete(ctx context.Context, req Request) (Response, error) {
	// Explicit opt-in only. Callers must bind the identity after resolving the
	// model; a global wrapper cannot safely infer a changing default model.
	if req.CacheScope == "" || req.CacheIdentity == "" || len(req.History) > 0 ||
		len(req.Images) > 0 || len(req.Documents) > 0 || len(req.Tools) > 0 ||
		len(req.ToolExchanges) > 0 || req.ForceTool != "" {
		return p.inner.Complete(ctx, req)
	}

	key := responseCacheKey(req)
	if key == "" {
		return p.inner.Complete(ctx, req)
	}

	// Check cache.
	entry, err := p.cache.GetCachedResponse(ctx, key, p.ttl)
	if err == nil && entry != nil {
		var meta cachedResponseMetadata
		if json.Unmarshal([]byte(entry.UsageJSON), &meta) == nil && meta.Version == 2 {
			return Response{Content: entry.Response, Model: meta.Model,
				RequestedModel: meta.RequestedModel, Provider: meta.Provider,
				ModelVerified: meta.ModelVerified, FinishReason: meta.FinishReason, Cached: true}, nil
		}
	}

	// Cache miss — call inner provider.
	resp, err := p.inner.Complete(ctx, req)
	if err != nil {
		return resp, err
	}

	// Save to cache (best effort).
	if len(resp.ToolCalls) != 0 || resp.Content == "" || resp.FinishReason == "length" || resp.Cached {
		return resp, nil
	}
	p.saveResponse(ctx, key, resp)
	return resp, nil
}

// saveResponse treats cache persistence as an optional side effect. Failures
// never invalidate an already completed, potentially paid provider response.
func (p *CachingProvider) saveResponse(ctx context.Context, key string, resp Response) {
	metaJSON, err := json.Marshal(cachedResponseMetadata{Version: 2, Model: resp.Model,
		RequestedModel: resp.RequestedModel, ModelVerified: resp.ModelVerified, Provider: resp.Provider, FinishReason: resp.FinishReason})
	if err != nil {
		return
	}
	if err := p.cache.SaveCachedResponse(ctx, key, resp.Model, resp.Content, string(metaJSON)); err != nil {
		return // do not evict when no new entry was saved
	}
	if err := p.cache.EvictResponseCache(ctx, p.maxItems); err != nil {
		return
	}
}

type cachedResponseMetadata struct {
	Version                                       int
	Model, RequestedModel, Provider, FinishReason string
	ModelVerified                                 bool
}

// CompleteStream delegates to inner provider without caching (streaming is not cached).
func (p *CachingProvider) CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error) {
	if sp, ok := p.inner.(StreamingProvider); ok {
		return sp.CompleteStream(ctx, req, callback)
	}
	return p.Complete(ctx, req)
}
