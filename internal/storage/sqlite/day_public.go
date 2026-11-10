package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/iulita-ai/iulita/internal/dayview"
)

// SavePublicSnapshot saves a bounded publication while preserving immutable ownership.
func (s *Store) SavePublicSnapshot(ctx context.Context, p *dayview.PublicSnapshot) error {
	if _, e := s.db.NewCreateTable().Model((*dayview.PublicSnapshot)(nil)).IfNotExists().Exec(ctx); e != nil {
		return e
	}
	result, e := s.db.NewInsert().Model(p).On("CONFLICT (id) DO UPDATE").Set("widget_id = EXCLUDED.widget_id").Set("withdrawn = EXCLUDED.withdrawn").Where("owner = EXCLUDED.owner AND instance = EXCLUDED.instance").Exec(ctx)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return errors.New("public snapshot ownership conflict")
	}
	return nil
}

// GetPublicSnapshot resolves a publication only within the requested bot instance.
func (s *Store) GetPublicSnapshot(ctx context.Context, instance, id string) (*dayview.PublicSnapshot, error) {
	p := new(dayview.PublicSnapshot)
	e := s.db.NewSelect().Model(p).Where("instance = ? AND id = ?", instance, id).Scan(ctx)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return p, e
}
