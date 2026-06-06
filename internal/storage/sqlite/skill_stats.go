package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// SaveSkillExecution appends a per-call skill telemetry record.
func (s *Store) SaveSkillExecution(ctx context.Context, e *domain.SkillExecution) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	if e.Origin == "" {
		e.Origin = domain.SkillOriginMain
	}
	if _, err := s.db.NewInsert().Model(e).Exec(ctx); err != nil {
		return fmt.Errorf("inserting skill execution: %w", err)
	}
	return nil
}

// GetSkillStats returns per-skill aggregated telemetry, ordered by call count.
// Uses the bun query builder so DATETIME columns (last_used) decode into time.Time
// regardless of how the driver represents them.
func (s *Store) GetSkillStats(ctx context.Context, filter storage.SkillStatsFilter) ([]storage.SkillStat, error) {
	q := s.db.NewSelect().
		Model((*domain.SkillExecution)(nil)).
		ColumnExpr("skill_name").
		ColumnExpr("COUNT(*) AS total_calls").
		ColumnExpr("COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0) AS success_calls").
		ColumnExpr("COALESCE(SUM(CASE WHEN success THEN 0 ELSE 1 END), 0) AS failure_calls").
		ColumnExpr("COALESCE(AVG(duration_ms), 0) AS avg_duration_ms").
		ColumnExpr("COALESCE(MAX(duration_ms), 0) AS max_duration_ms").
		ColumnExpr("MAX(created_at) AS last_used")

	if filter.UserID != "" {
		q = q.Where("user_id = ?", filter.UserID)
	}
	if filter.Origin != "" {
		q = q.Where("origin = ?", filter.Origin)
	}
	if !filter.From.IsZero() {
		q = q.Where("created_at >= ?", filter.From)
	}
	if !filter.To.IsZero() {
		q = q.Where("created_at < ?", filter.To)
	}

	q = q.GroupExpr("skill_name").OrderExpr("total_calls DESC, skill_name ASC")

	var result []storage.SkillStat
	if err := q.Scan(ctx, &result); err != nil {
		return nil, fmt.Errorf("querying skill stats: %w", err)
	}
	return result, nil
}
