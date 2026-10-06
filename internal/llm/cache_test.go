package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

type memoryResponseCache struct {
	entries       map[string]*CachedResponseEntry
	reads, writes int
}

func (c *memoryResponseCache) GetCachedResponse(_ context.Context, hash string, _ time.Duration) (*CachedResponseEntry, error) {
	c.reads++
	return c.entries[hash], nil
}
func (c *memoryResponseCache) SaveCachedResponse(_ context.Context, hash, model, content, meta string) error {
	c.writes++
	c.entries[hash] = &CachedResponseEntry{Response: content, UsageJSON: meta}
	return nil
}
func (c *memoryResponseCache) EvictResponseCache(context.Context, int) error { return nil }

type countedProvider struct{ calls int }

func (p *countedProvider) Complete(context.Context, Request) (Response, error) {
	p.calls++
	return Response{Content: "answer", Model: "served", RequestedModel: "requested", Provider: "deepseek", FinishReason: "stop", Usage: Usage{InputTokens: 100, OutputTokens: 10}}, nil
}

func TestResponseCachePrivateRequestsNeverReadOrWrite(t *testing.T) {
	for _, req := range []Request{
		{Message: "private"},
		{History: []domain.ChatMessage{{Content: "private"}}},
		{Images: []ImageAttachment{{Data: []byte("private")}}},
		{Documents: []DocumentAttachment{{Data: []byte("private")}}},
		{Tools: []ToolDefinition{{Name: "tool"}}},
		{ToolExchanges: []ToolExchange{{AssistantText: "private"}}},
		{ForceTool: "remember"},
	} {
		if req.Message != "private" {
			req.CacheScope = "stateless"
			req.CacheIdentity = "profile:revision"
		}
		cache := &memoryResponseCache{entries: map[string]*CachedResponseEntry{}}
		inner := &countedProvider{}
		p := NewCachingProvider(inner, cache, time.Minute, 10)
		for i := 0; i < 2; i++ {
			if _, err := p.Complete(context.Background(), req); err != nil {
				t.Fatal(err)
			}
		}
		if inner.calls != 2 || cache.reads != 0 || cache.writes != 0 {
			t.Fatalf("private request reached cache: inner=%d reads=%d writes=%d", inner.calls, cache.reads, cache.writes)
		}
	}
}

func TestResponseCacheHitHasProvenanceWithoutNewUsage(t *testing.T) {
	inner := &countedProvider{}
	cache := &memoryResponseCache{entries: map[string]*CachedResponseEntry{}}
	p := NewCachingProvider(inner, cache, time.Minute, 10)
	req := Request{CacheScope: "public-stateless", CacheIdentity: "profile:rev1", Message: "question"}
	first, err := p.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	hit, err := p.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached || !hit.Cached || hit.Usage != (Usage{}) || hit.Model != "served" || hit.RequestedModel != "requested" || hit.Provider != "deepseek" || inner.calls != 1 {
		t.Fatalf("invalid cached response: %+v calls=%d", hit, inner.calls)
	}
	req.CacheIdentity = "profile:rev2"
	_, _ = p.Complete(context.Background(), req)
	if inner.calls != 2 {
		t.Fatal("revision change reused cached answer")
	}
}

func TestResponseCacheFullPromptsAndUnambiguousFields(t *testing.T) {
	base := Request{CacheScope: "scope", CacheIdentity: "identity", SystemPrompt: string(make([]byte, 200)) + "first", Message: "a|b"}
	for _, change := range []func(*Request){
		func(r *Request) { r.SystemPrompt = string(make([]byte, 200)) + "second" },
		func(r *Request) { r.StaticSystemPrompt = "private" },
		func(r *Request) { r.CacheScope = "other" },
		func(r *Request) { r.ProfileID = "other" },
		func(r *Request) { r.Message = "a"; r.SystemPrompt += "|b" },
	} {
		other := base
		change(&other)
		if responseCacheKey(base) == responseCacheKey(other) {
			t.Fatal("distinct request collided")
		}
	}
}

type failingResponseCache struct {
	memoryResponseCache
	writeError    error
	evictionError error
	evictions     int
}

func (c *failingResponseCache) SaveCachedResponse(ctx context.Context, hash, model, content, meta string) error {
	if c.writeError != nil {
		c.writes++
		return c.writeError
	}
	return c.memoryResponseCache.SaveCachedResponse(ctx, hash, model, content, meta)
}
func (c *failingResponseCache) EvictResponseCache(context.Context, int) error {
	c.evictions++
	return c.evictionError
}

func TestResponseCacheWriteFailurePreservesPaidResultAndSkipsEviction(t *testing.T) {
	cache := &failingResponseCache{memoryResponseCache: memoryResponseCache{entries: map[string]*CachedResponseEntry{}}, writeError: errors.New("cache unavailable")}
	inner := &countedProvider{}
	provider := NewCachingProvider(inner, cache, time.Minute, 10)
	req := Request{CacheScope: "stateless", CacheIdentity: "profile:rev1", Message: "question"}
	for i := 0; i < 2; i++ {
		response, err := provider.Complete(context.Background(), req)
		if err != nil || response.Cached || response.Content != "answer" || response.Usage.InputTokens != 100 || response.Usage.OutputTokens != 10 {
			t.Fatalf("cache failure corrupted paid result: %+v %v", response, err)
		}
	}
	if cache.evictions != 0 || cache.writes != 2 || inner.calls != 2 {
		t.Fatal("failed cache write triggered eviction or reused an unsaved result")
	}
}

func TestResponseCacheEvictionFailureDoesNotInvalidateSavedResult(t *testing.T) {
	cache := &failingResponseCache{memoryResponseCache: memoryResponseCache{entries: map[string]*CachedResponseEntry{}}, evictionError: errors.New("eviction unavailable")}
	inner := &countedProvider{}
	provider := NewCachingProvider(inner, cache, time.Minute, 10)
	req := Request{CacheScope: "stateless", CacheIdentity: "profile:rev1", Message: "question"}
	if response, err := provider.Complete(context.Background(), req); err != nil || response.Cached || response.Usage.InputTokens != 100 {
		t.Fatal("eviction failure invalidated paid result")
	}
	response, err := provider.Complete(context.Background(), req)
	if err != nil || !response.Cached || response.Usage != (Usage{}) || inner.calls != 1 || cache.evictions != 1 {
		t.Fatal("eviction failure prevented a valid saved cache hit")
	}
}
