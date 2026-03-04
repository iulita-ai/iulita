package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) CreateChannelInstance(ctx context.Context, ci *domain.ChannelInstance) error {
	ci.CreatedAt = time.Now()
	ci.UpdatedAt = time.Now()
	_, err := s.db.NewInsert().Model(ci).Exec(ctx)
	if err != nil {
		return fmt.Errorf("creating channel instance: %w", err)
	}
	return nil
}

func (s *Store) GetChannelInstance(ctx context.Context, id string) (*domain.ChannelInstance, error) {
	ci := new(domain.ChannelInstance)
	err := s.db.NewSelect().Model(ci).Where("id = ?", id).Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting channel instance: %w", err)
	}
	return ci, nil
}

func (s *Store) ListChannelInstances(ctx context.Context) ([]domain.ChannelInstance, error) {
	var instances []domain.ChannelInstance
	err := s.db.NewSelect().Model(&instances).Order("created_at ASC").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing channel instances: %w", err)
	}
	return instances, nil
}

func (s *Store) UpdateChannelInstance(ctx context.Context, ci *domain.ChannelInstance) error {
	ci.UpdatedAt = time.Now()
	_, err := s.db.NewUpdate().Model(ci).WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating channel instance: %w", err)
	}
	return nil
}

func (s *Store) DeleteChannelInstance(ctx context.Context, id string) error {
	_, err := s.db.NewDelete().Model((*domain.ChannelInstance)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting channel instance: %w", err)
	}
	return nil
}
