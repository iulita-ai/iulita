package sqlite

import (
	"context"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
)

// PendingDayCards lists bounded undelivered, unexpired projections for one bot instance.
func (s *Store) PendingDayCards(ctx context.Context, instance string) ([]dayview.Card, error) {
	var cards []dayview.Card
	e := s.db.NewSelect().Model(&cards).Where("instance = ? AND revision > delivered_revision AND expires > ?", instance, time.Now()).Order("expires ASC").Limit(20).Scan(ctx)
	return cards, e
}

// MarkDayDelivered advances a card delivery receipt only for the expected revision.
func (s *Store) MarkDayDelivered(ctx context.Context, id string, revision int64) error {
	_, e := s.db.NewUpdate().Model((*dayview.Card)(nil)).Set("delivered_revision = ?", revision).Where("id = ? AND revision = ?", id, revision).Exec(ctx)
	return e
}
