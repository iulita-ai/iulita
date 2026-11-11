package sqlite

import (
	"context"
	"fmt"

	"github.com/iulita-ai/iulita/internal/domain"
)

// GetLocationFactsByUser reads a bounded set of relevant user-source assertions
// by creation time, independent of popularity, decay and memory reinforcement.
func (s *Store) GetLocationFactsByUser(ctx context.Context, owner string, limit int) ([]domain.Fact, error) {
	if owner == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	const match = `live* OR reside* OR based OR located OR current* OR home OR city OR moved OR living OR жив* OR жил* OR прожива* OR нахожусь OR находится OR местоположение OR город OR дом OR переехал* OR сейчас OR in OR no OR not OR longer OR "не" OR уже OR "в"`
	var facts []domain.Fact
	err := s.db.NewSelect().Model(&facts).
		Where(legacyChatPredicate).Where("user_id = ?", owner).
		Where("source_type = ?", "user").
		Where("id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)", match).
		OrderExpr("created_at DESC, id DESC").Limit(limit).Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading location assertions: %w", err)
	}
	return facts, nil
}
