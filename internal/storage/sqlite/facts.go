package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) SaveFact(ctx context.Context, f *domain.Fact) error {
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now()
	}
	if f.LastAccessedAt.IsZero() {
		f.LastAccessedAt = f.CreatedAt
	}
	_, err := s.db.NewInsert().Model(f).Exec(ctx)
	if err != nil {
		return fmt.Errorf("inserting fact: %w", err)
	}

	// Auto-embed in background if embedding is configured.
	if s.embedFunc != nil && f.ID > 0 {
		go func(id int64, content string) {
			bgCtx := context.Background()
			vecs, err := s.embedFunc(bgCtx, []string{content})
			if err == nil && len(vecs) > 0 {
				s.SaveFactVector(bgCtx, id, vecs[0])
			}
		}(f.ID, f.Content)
	}

	return nil
}

func (s *Store) SearchFacts(ctx context.Context, chatID, query string, limit int) ([]domain.Fact, error) {
	var facts []domain.Fact
	fetchLimit := limit * 3
	if fetchLimit < 20 {
		fetchLimit = 20
	}
	err := s.db.NewSelect().
		Model(&facts).
		Where("chat_id = ?", chatID).
		Where("id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)", query).
		OrderExpr("access_count DESC, last_accessed_at DESC").
		Limit(fetchLimit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("searching facts: %w", err)
	}
	if s.halfLifeDays > 0 && len(facts) > 0 {
		if s.mmrLambda > 0 {
			sorted, scores := factDecayScores(facts, s.halfLifeDays)
			return applyMMRFacts(sorted, scores, s.mmrLambda, limit), nil
		}
		return rankFactsByDecay(facts, s.halfLifeDays, limit), nil
	}
	if len(facts) > limit {
		facts = facts[:limit]
	}
	return facts, nil
}

func (s *Store) DeleteFact(ctx context.Context, id int64, chatID string) error {
	res, err := s.db.NewDelete().
		Model((*domain.Fact)(nil)).
		Where("id = ? AND chat_id = ?", id, chatID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting fact: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("fact #%d not found", id)
	}
	return nil
}

func (s *Store) DeleteFactByID(ctx context.Context, id int64) error {
	res, err := s.db.NewDelete().
		Model((*domain.Fact)(nil)).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting fact: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("fact #%d not found", id)
	}
	return nil
}

func (s *Store) UpdateFactContent(ctx context.Context, id int64, content string) error {
	res, err := s.db.NewUpdate().
		Model((*domain.Fact)(nil)).
		Set("content = ?", content).
		Set("last_accessed_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating fact content: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("fact #%d not found", id)
	}

	// Re-embed if embedding is configured.
	if s.embedFunc != nil {
		go func(factID int64, text string) {
			bgCtx := context.Background()
			vecs, err := s.embedFunc(bgCtx, []string{text})
			if err == nil && len(vecs) > 0 {
				s.SaveFactVector(bgCtx, factID, vecs[0])
			}
		}(id, content)
	}

	return nil
}

func (s *Store) DeleteFactsByQuery(ctx context.Context, chatID, query string) (int, error) {
	res, err := s.db.NewDelete().
		Model((*domain.Fact)(nil)).
		Where("chat_id = ?", chatID).
		Where("id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)", query).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("deleting facts by query: %w", err)
	}
	rows, _ := res.RowsAffected()
	return int(rows), nil
}

func (s *Store) ReinforceFact(ctx context.Context, id int64) error {
	_, err := s.db.NewUpdate().
		Model((*domain.Fact)(nil)).
		Set("access_count = access_count + 1").
		Set("last_accessed_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("reinforcing fact: %w", err)
	}
	return nil
}

func (s *Store) GetRecentFacts(ctx context.Context, chatID string, limit int) ([]domain.Fact, error) {
	var facts []domain.Fact
	fetchLimit := limit * 3
	if fetchLimit < 30 {
		fetchLimit = 30
	}
	err := s.db.NewSelect().
		Model(&facts).
		Where("chat_id = ?", chatID).
		Order("last_accessed_at DESC").
		Limit(fetchLimit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting recent facts: %w", err)
	}
	if s.halfLifeDays > 0 && len(facts) > 0 {
		if s.mmrLambda > 0 {
			sorted, scores := factDecayScores(facts, s.halfLifeDays)
			return applyMMRFacts(sorted, scores, s.mmrLambda, limit), nil
		}
		return rankFactsByDecay(facts, s.halfLifeDays, limit), nil
	}
	if len(facts) > limit {
		facts = facts[:limit]
	}
	return facts, nil
}

func (s *Store) GetAllFacts(ctx context.Context, chatID string) ([]domain.Fact, error) {
	var facts []domain.Fact
	q := s.db.NewSelect().
		Model(&facts).
		Order("created_at ASC")
	if chatID != "" {
		q = q.Where("chat_id = ?", chatID)
	}
	err := q.Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting all facts: %w", err)
	}
	return facts, nil
}
