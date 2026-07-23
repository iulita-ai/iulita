package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// UpsertSlackRoute persists (or refreshes) the Slack coordinates for a composite
// chatID so proactive delivery can route to it after the in-memory cache is gone.
func (s *Store) UpsertSlackRoute(ctx context.Context, r *domain.SlackChatRoute) error {
	r.UpdatedAt = time.Now()
	_, err := s.db.NewInsert().
		Model(r).
		On("CONFLICT (instance_id, chat_id) DO UPDATE").
		Set("channel_id = EXCLUDED.channel_id").
		Set("slack_user_id = EXCLUDED.slack_user_id").
		Set("thread_ts = EXCLUDED.thread_ts").
		Set("locale = EXCLUDED.locale").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("upserting slack route: %w", err)
	}
	return nil
}

// GetSlackRoute returns the persisted route for an (instanceID, chatID), or
// (nil, nil) when none exists so the caller can fall back to parsing the chatID.
// Scoping by instanceID isolates routes between Slack instances that may share a
// workspace's channel IDs.
func (s *Store) GetSlackRoute(ctx context.Context, instanceID, chatID string) (*domain.SlackChatRoute, error) {
	r := new(domain.SlackChatRoute)
	err := s.db.NewSelect().Model(r).
		Where("instance_id = ?", instanceID).
		Where("chat_id = ?", chatID).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting slack route: %w", err)
	}
	return r, nil
}

// DeleteSlackRoutesOlderThan prunes stale routes for an instance whose last
// update predates olderThan, bounding table growth (thread-scoped chatIDs mint a
// row per thread). Returns the number of rows deleted.
func (s *Store) DeleteSlackRoutesOlderThan(ctx context.Context, instanceID string, olderThan time.Time) (int64, error) {
	res, err := s.db.NewDelete().
		Model((*domain.SlackChatRoute)(nil)).
		Where("instance_id = ?", instanceID).
		Where("updated_at < ?", olderThan).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("deleting stale slack routes: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting deleted slack routes: %w", err)
	}
	return n, nil
}
