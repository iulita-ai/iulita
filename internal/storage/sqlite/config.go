package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) GetConfigOverride(ctx context.Context, key string) (*domain.ConfigOverride, error) {
	o := new(domain.ConfigOverride)
	err := s.db.NewSelect().Model(o).Where("\"key\" = ?", key).Scan(ctx)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting config override %q: %w", key, err)
	}
	return o, nil
}

func (s *Store) ListConfigOverrides(ctx context.Context) ([]domain.ConfigOverride, error) {
	var overrides []domain.ConfigOverride
	err := s.db.NewSelect().Model(&overrides).Order("\"key\" ASC").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing config overrides: %w", err)
	}
	return overrides, nil
}

func (s *Store) SaveConfigOverride(ctx context.Context, o *domain.ConfigOverride) error {
	_, err := s.db.NewInsert().Model(o).
		On("CONFLICT (\"key\") DO UPDATE").
		Set("value = EXCLUDED.value").
		Set("encrypted = EXCLUDED.encrypted").
		Set("updated_at = EXCLUDED.updated_at").
		Set("updated_by = EXCLUDED.updated_by").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("saving config override %q: %w", o.Key, err)
	}
	return nil
}

func (s *Store) DeleteConfigOverride(ctx context.Context, key string) error {
	_, err := s.db.NewDelete().
		Model((*domain.ConfigOverride)(nil)).
		Where("\"key\" = ?", key).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting config override %q: %w", key, err)
	}
	return nil
}
