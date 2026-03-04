package llm

import (
	"context"
	"time"

	"github.com/iulita-ai/iulita/internal/storage"
)

// StorageResponseCacheAdapter adapts storage.Repository to llm.ResponseCache interface.
type StorageResponseCacheAdapter struct {
	store storage.Repository
}

// NewStorageResponseCacheAdapter creates an adapter.
func NewStorageResponseCacheAdapter(store storage.Repository) *StorageResponseCacheAdapter {
	return &StorageResponseCacheAdapter{store: store}
}

func (a *StorageResponseCacheAdapter) GetCachedResponse(ctx context.Context, hash string, maxAge time.Duration) (*CachedResponseEntry, error) {
	resp, err := a.store.GetCachedResponse(ctx, hash, maxAge)
	if err != nil || resp == nil {
		return nil, err
	}
	return &CachedResponseEntry{
		Response:  resp.Response,
		UsageJSON: resp.UsageJSON,
		HitCount:  resp.HitCount,
	}, nil
}

func (a *StorageResponseCacheAdapter) SaveCachedResponse(ctx context.Context, hash, model, response, usageJSON string) error {
	return a.store.SaveCachedResponse(ctx, hash, model, response, usageJSON)
}

func (a *StorageResponseCacheAdapter) EvictResponseCache(ctx context.Context, maxEntries int) error {
	return a.store.EvictResponseCache(ctx, maxEntries)
}

// StorageEmbeddingCacheAdapter adapts storage.Repository to llm.EmbeddingCache interface.
type StorageEmbeddingCacheAdapter struct {
	store storage.Repository
}

// NewStorageEmbeddingCacheAdapter creates an adapter.
func NewStorageEmbeddingCacheAdapter(store storage.Repository) *StorageEmbeddingCacheAdapter {
	return &StorageEmbeddingCacheAdapter{store: store}
}

func (a *StorageEmbeddingCacheAdapter) GetCachedEmbedding(ctx context.Context, hash string) ([]float32, error) {
	return a.store.GetCachedEmbedding(ctx, hash)
}

func (a *StorageEmbeddingCacheAdapter) SaveCachedEmbedding(ctx context.Context, hash string, embedding []float32) error {
	return a.store.SaveCachedEmbedding(ctx, hash, embedding)
}

func (a *StorageEmbeddingCacheAdapter) EvictOldEmbeddings(ctx context.Context, maxEntries int) error {
	return a.store.EvictOldEmbeddings(ctx, maxEntries)
}
