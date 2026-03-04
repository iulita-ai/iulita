package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) SaveInsight(ctx context.Context, d *domain.Insight) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	_, err := s.db.NewInsert().Model(d).Exec(ctx)
	if err != nil {
		return fmt.Errorf("inserting insight: %w", err)
	}

	// Auto-embed in background if embedding is configured.
	if s.embedFunc != nil && d.ID > 0 {
		go func(id int64, content string) {
			bgCtx := context.Background()
			vecs, err := s.embedFunc(bgCtx, []string{content})
			if err == nil && len(vecs) > 0 {
				s.SaveInsightVector(bgCtx, id, vecs[0])
			}
		}(d.ID, d.Content)
	}

	return nil
}

func (s *Store) GetRecentInsights(ctx context.Context, chatID string, limit int) ([]domain.Insight, error) {
	var insights []domain.Insight
	// Fetch extra candidates for temporal decay re-ranking.
	fetchLimit := limit * 3
	if fetchLimit < 20 {
		fetchLimit = 20
	}
	err := s.db.NewSelect().
		Model(&insights).
		Where("chat_id = ?", chatID).
		Where("expires_at IS NULL OR expires_at > ?", time.Now()).
		OrderExpr("quality DESC, access_count DESC, created_at DESC").
		Limit(fetchLimit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting recent insights: %w", err)
	}
	if s.halfLifeDays > 0 && len(insights) > 0 {
		if s.mmrLambda > 0 {
			sorted, scores := insightDecayScores(insights, s.halfLifeDays)
			return applyMMRInsights(sorted, scores, s.mmrLambda, limit), nil
		}
		return rankInsightsByDecay(insights, s.halfLifeDays, limit), nil
	}
	if len(insights) > limit {
		insights = insights[:limit]
	}
	return insights, nil
}

func (s *Store) DeleteExpiredInsights(ctx context.Context) error {
	_, err := s.db.NewDelete().
		Model((*domain.Insight)(nil)).
		Where("expires_at IS NOT NULL AND expires_at < ?", time.Now()).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting expired insights: %w", err)
	}
	return nil
}

func (s *Store) DeleteInsight(ctx context.Context, id int64, chatID string) error {
	res, err := s.db.NewDelete().
		Model((*domain.Insight)(nil)).
		Where("id = ? AND chat_id = ?", id, chatID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting insight: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("insight #%d not found", id)
	}
	return nil
}

func (s *Store) ReinforceInsight(ctx context.Context, id int64) error {
	_, err := s.db.NewUpdate().
		Model((*domain.Insight)(nil)).
		Set("access_count = access_count + 1").
		Set("last_accessed_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("reinforcing insight: %w", err)
	}
	return nil
}

func (s *Store) SearchInsights(ctx context.Context, chatID, query string, limit int) ([]domain.Insight, error) {
	var insights []domain.Insight
	fetchLimit := limit * 3
	if fetchLimit < 20 {
		fetchLimit = 20
	}
	err := s.db.NewSelect().
		Model(&insights).
		Where("chat_id = ?", chatID).
		Where("id IN (SELECT rowid FROM insights_fts WHERE insights_fts MATCH ?)", query).
		Where("expires_at IS NULL OR expires_at > ?", time.Now()).
		OrderExpr("quality DESC, access_count DESC, created_at DESC").
		Limit(fetchLimit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("searching insights: %w", err)
	}
	if s.halfLifeDays > 0 && len(insights) > 0 {
		if s.mmrLambda > 0 {
			sorted, scores := insightDecayScores(insights, s.halfLifeDays)
			return applyMMRInsights(sorted, scores, s.mmrLambda, limit), nil
		}
		return rankInsightsByDecay(insights, s.halfLifeDays, limit), nil
	}
	if len(insights) > limit {
		insights = insights[:limit]
	}
	return insights, nil
}

func (s *Store) CountInsights(ctx context.Context, chatID string) (int, error) {
	q := s.db.NewSelect().
		Model((*domain.Insight)(nil)).
		Where("expires_at IS NULL OR expires_at > ?", time.Now())
	if chatID != "" {
		q = q.Where("chat_id = ?", chatID)
	}
	count, err := q.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting insights: %w", err)
	}
	return count, nil
}
