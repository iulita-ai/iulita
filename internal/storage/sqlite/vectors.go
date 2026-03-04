package sqlite

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// encodeVector encodes a []float32 as a binary BLOB using LittleEndian encoding.
func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVector decodes a vector from binary BLOB format, falling back to JSON for legacy data.
func decodeVector(data []byte) ([]float32, error) {
	// Try binary first: must be a multiple of 4 bytes and not start with '[' (JSON array).
	if len(data) > 0 && len(data)%4 == 0 && data[0] != '[' {
		n := len(data) / 4
		v := make([]float32, n)
		for i := 0; i < n; i++ {
			v[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
		}
		return v, nil
	}

	// Fall back to JSON.
	var v []float32
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decoding vector: %w", err)
	}
	return v, nil
}

// CreateVectorTables creates tables for storing embeddings.
func (s *Store) CreateVectorTables(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS fact_vectors (
			fact_id INTEGER PRIMARY KEY REFERENCES facts(id) ON DELETE CASCADE,
			embedding TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS insight_vectors (
			insight_id INTEGER PRIMARY KEY REFERENCES insights(id) ON DELETE CASCADE,
			embedding TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("creating vector table: %w", err)
		}
	}
	return nil
}

// SaveFactVector stores the embedding for a fact as a binary BLOB.
func (s *Store) SaveFactVector(ctx context.Context, factID int64, embedding []float32) error {
	data := encodeVector(embedding)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO fact_vectors (fact_id, embedding, created_at) VALUES (?, ?, ?)`,
		factID, data, time.Now())
	return err
}

// SaveInsightVector stores the embedding for an insight as a binary BLOB.
func (s *Store) SaveInsightVector(ctx context.Context, insightID int64, embedding []float32) error {
	data := encodeVector(embedding)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO insight_vectors (insight_id, embedding, created_at) VALUES (?, ?, ?)`,
		insightID, data, time.Now())
	return err
}

type vectorRow struct {
	ID        int64
	Embedding []float32
}

// SearchFactsHybrid combines FTS and vector search, merging results with weighted scoring.
// vectorWeight controls the blend: 0 = pure FTS, 1 = pure vector.
func (s *Store) SearchFactsHybrid(ctx context.Context, chatID string, query string, queryVec []float32, limit int, vectorWeight float64) ([]domain.Fact, error) {
	// FTS results.
	ftsResults, err := s.SearchFacts(ctx, chatID, query, limit*2)
	if err != nil {
		return nil, err
	}

	if queryVec == nil || vectorWeight <= 0 {
		if len(ftsResults) > limit {
			return ftsResults[:limit], nil
		}
		return ftsResults, nil
	}

	// Load all fact vectors for this chat.
	var rows []struct {
		FactID    int64  `bun:"fact_id"`
		Embedding []byte `bun:"embedding"`
	}
	err = s.db.NewSelect().
		TableExpr("fact_vectors fv").
		Join("JOIN facts f ON f.id = fv.fact_id").
		Where("f.chat_id = ?", chatID).
		ColumnExpr("fv.fact_id, fv.embedding").
		Scan(ctx, &rows)
	if err != nil {
		// Fall back to FTS-only.
		if len(ftsResults) > limit {
			return ftsResults[:limit], nil
		}
		return ftsResults, nil
	}

	// Compute cosine similarities.
	vecScores := make(map[int64]float64)
	for _, row := range rows {
		emb, err := decodeVector(row.Embedding)
		if err != nil {
			continue
		}
		vecScores[row.FactID] = cosineSimilarity(queryVec, emb)
	}

	// Build FTS score map (rank by position).
	ftsScores := make(map[int64]float64)
	for i, f := range ftsResults {
		ftsScores[f.ID] = 1.0 - float64(i)/float64(len(ftsResults)+1)
	}

	// Merge all candidate IDs.
	allIDs := make(map[int64]struct{})
	for id := range ftsScores {
		allIDs[id] = struct{}{}
	}
	for id := range vecScores {
		allIDs[id] = struct{}{}
	}

	type scored struct {
		id    int64
		score float64
	}
	var candidates []scored
	for id := range allIDs {
		fts := ftsScores[id]
		vec := vecScores[id]
		combined := (1-vectorWeight)*fts + vectorWeight*vec
		candidates = append(candidates, scored{id, combined})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// Fetch facts by IDs.
	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
	}

	var facts []domain.Fact
	err = s.db.NewSelect().
		Model(&facts).
		Where("id IN (?)", s.db.NewSelect().TableExpr("unnest(?::bigint[]) AS id", ids)).
		Scan(ctx)
	if err != nil {
		// Fallback: use bun's IN operator.
		err = s.db.NewSelect().
			Model(&facts).
			Where("id IN (?)", ids).
			Scan(ctx)
		if err != nil {
			return ftsResults, nil
		}
	}

	// Reorder by score.
	idOrder := make(map[int64]int)
	for i, c := range candidates {
		idOrder[c.id] = i
	}
	sort.Slice(facts, func(i, j int) bool {
		return idOrder[facts[i].ID] < idOrder[facts[j].ID]
	})

	return facts, nil
}

// SearchInsightsHybrid combines FTS and vector search for insights.
func (s *Store) SearchInsightsHybrid(ctx context.Context, chatID string, query string, queryVec []float32, limit int, vectorWeight float64) ([]domain.Insight, error) {
	// FTS results.
	ftsResults, err := s.SearchInsights(ctx, chatID, query, limit*2)
	if err != nil {
		return nil, err
	}

	if queryVec == nil || vectorWeight <= 0 {
		if len(ftsResults) > limit {
			return ftsResults[:limit], nil
		}
		return ftsResults, nil
	}

	// Load all insight vectors for this chat.
	var rows []struct {
		InsightID int64  `bun:"insight_id"`
		Embedding []byte `bun:"embedding"`
	}
	err = s.db.NewSelect().
		TableExpr("insight_vectors iv").
		Join("JOIN insights i ON i.id = iv.insight_id").
		Where("i.chat_id = ?", chatID).
		ColumnExpr("iv.insight_id, iv.embedding").
		Scan(ctx, &rows)
	if err != nil {
		if len(ftsResults) > limit {
			return ftsResults[:limit], nil
		}
		return ftsResults, nil
	}

	// Compute cosine similarities.
	vecScores := make(map[int64]float64)
	for _, row := range rows {
		emb, err := decodeVector(row.Embedding)
		if err != nil {
			continue
		}
		vecScores[row.InsightID] = cosineSimilarity(queryVec, emb)
	}

	// Build FTS score map.
	ftsScores := make(map[int64]float64)
	for i, ins := range ftsResults {
		ftsScores[ins.ID] = 1.0 - float64(i)/float64(len(ftsResults)+1)
	}

	// Merge all candidate IDs.
	allIDs := make(map[int64]struct{})
	for id := range ftsScores {
		allIDs[id] = struct{}{}
	}
	for id := range vecScores {
		allIDs[id] = struct{}{}
	}

	type scored struct {
		id    int64
		score float64
	}
	var candidates []scored
	for id := range allIDs {
		fts := ftsScores[id]
		vec := vecScores[id]
		combined := (1-vectorWeight)*fts + vectorWeight*vec
		candidates = append(candidates, scored{id, combined})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
	}

	var insights []domain.Insight
	err = s.db.NewSelect().
		Model(&insights).
		Where("id IN (?)", ids).
		Scan(ctx)
	if err != nil {
		return ftsResults, nil
	}

	// Reorder by score.
	idOrder := make(map[int64]int)
	for i, c := range candidates {
		idOrder[c.id] = i
	}
	sort.Slice(insights, func(i, j int) bool {
		return idOrder[insights[i].ID] < idOrder[insights[j].ID]
	})

	return insights, nil
}

// FactsWithoutEmbeddings returns facts that don't have a vector embedding yet.
func (s *Store) FactsWithoutEmbeddings(ctx context.Context, limit int) ([]domain.Fact, error) {
	var facts []domain.Fact
	err := s.db.NewSelect().
		Model(&facts).
		Where("id NOT IN (SELECT fact_id FROM fact_vectors)").
		Order("created_at DESC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying facts without embeddings: %w", err)
	}
	return facts, nil
}

// InsightsWithoutEmbeddings returns insights that don't have a vector embedding yet.
func (s *Store) InsightsWithoutEmbeddings(ctx context.Context, limit int) ([]domain.Insight, error) {
	var insights []domain.Insight
	err := s.db.NewSelect().
		Model(&insights).
		Where("id NOT IN (SELECT insight_id FROM insight_vectors)").
		Order("created_at DESC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying insights without embeddings: %w", err)
	}
	return insights, nil
}

// GetCachedEmbedding retrieves a cached embedding by content hash.
func (s *Store) GetCachedEmbedding(ctx context.Context, contentHash string) ([]float32, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT embedding FROM embedding_cache WHERE content_hash = ?`, contentHash).Scan(&data)
	if err != nil {
		return nil, err
	}

	// Update accessed_at timestamp.
	s.db.ExecContext(ctx,
		`UPDATE embedding_cache SET accessed_at = ? WHERE content_hash = ?`,
		time.Now(), contentHash)

	return decodeVector(data)
}

// SaveCachedEmbedding stores an embedding in the cache keyed by content hash.
func (s *Store) SaveCachedEmbedding(ctx context.Context, contentHash string, embedding []float32) error {
	data := encodeVector(embedding)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO embedding_cache (content_hash, embedding, dimensions, created_at, accessed_at)
		 VALUES (?, ?, ?, ?, ?)`,
		contentHash, data, len(embedding), time.Now(), time.Now())
	if err != nil {
		return fmt.Errorf("saving cached embedding: %w", err)
	}
	return nil
}

// EvictOldEmbeddings removes the least recently accessed embeddings beyond maxEntries.
func (s *Store) EvictOldEmbeddings(ctx context.Context, maxEntries int) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM embedding_cache WHERE content_hash NOT IN (
			SELECT content_hash FROM embedding_cache ORDER BY accessed_at DESC LIMIT ?
		)`, maxEntries)
	if err != nil {
		return fmt.Errorf("evicting old embeddings: %w", err)
	}
	return nil
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
