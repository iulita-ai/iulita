package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/storage"
)

// GetCachedResponse retrieves a cached LLM response by prompt hash.
// Returns nil if not found or older than maxAge.
func (s *Store) GetCachedResponse(ctx context.Context, hash string, maxAge time.Duration) (*storage.CachedResponse, error) {
	var resp storage.CachedResponse
	var createdAt time.Time

	err := s.db.QueryRowContext(ctx,
		`SELECT response, usage_json, hit_count, created_at FROM response_cache WHERE prompt_hash = ?`,
		hash).Scan(&resp.Response, &resp.UsageJSON, &resp.HitCount, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying response cache: %w", err)
	}

	// Check age.
	if maxAge > 0 && time.Since(createdAt) > maxAge {
		return nil, nil
	}

	// Update hit_count and accessed_at.
	s.db.ExecContext(ctx,
		`UPDATE response_cache SET hit_count = hit_count + 1, accessed_at = ? WHERE prompt_hash = ?`,
		time.Now(), hash)

	resp.HitCount++ // reflect the current hit
	return &resp, nil
}

// SaveCachedResponse stores an LLM response in the cache.
func (s *Store) SaveCachedResponse(ctx context.Context, hash, model, response, usageJSON string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO response_cache (prompt_hash, model, response, usage_json, created_at, accessed_at, hit_count)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`,
		hash, model, response, usageJSON, now, now)
	if err != nil {
		return fmt.Errorf("saving cached response: %w", err)
	}
	return nil
}

// EvictResponseCache removes the least recently accessed entries beyond maxEntries.
func (s *Store) EvictResponseCache(ctx context.Context, maxEntries int) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM response_cache WHERE prompt_hash NOT IN (
			SELECT prompt_hash FROM response_cache ORDER BY accessed_at DESC LIMIT ?
		)`, maxEntries)
	if err != nil {
		return fmt.Errorf("evicting response cache: %w", err)
	}
	return nil
}

// GetResponseCacheStats returns the total number of cached entries and total hit count.
func (s *Store) GetResponseCacheStats(ctx context.Context) (entries int, totalHits int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(hit_count), 0) FROM response_cache`).
		Scan(&entries, &totalHits)
	if err != nil {
		return 0, 0, fmt.Errorf("querying response cache stats: %w", err)
	}
	return entries, totalHits, nil
}
