package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) SaveDirective(ctx context.Context, d *domain.Directive) error {
	d.UpdatedAt = time.Now()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = d.UpdatedAt
	}

	_, err := s.db.NewInsert().
		Model(d).
		On("CONFLICT (chat_id) DO UPDATE").
		Set("content = EXCLUDED.content").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("upserting directive: %w", err)
	}
	return nil
}

func (s *Store) GetDirective(ctx context.Context, chatID string) (*domain.Directive, error) {
	d := new(domain.Directive)
	err := s.db.NewSelect().
		Model(d).
		Where("chat_id = ?", chatID).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting directive: %w", err)
	}
	return d, nil
}

func (s *Store) DeleteDirective(ctx context.Context, chatID string) error {
	_, err := s.db.NewDelete().
		Model((*domain.Directive)(nil)).
		Where("chat_id = ?", chatID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting directive: %w", err)
	}
	return nil
}
