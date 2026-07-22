package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// SaveSlackAccount inserts the owner's Slack account row.
func (s *Store) SaveSlackAccount(ctx context.Context, a *domain.SlackAccount) error {
	if _, err := s.db.NewInsert().Model(a).Exec(ctx); err != nil {
		return fmt.Errorf("inserting slack account: %w", err)
	}
	return nil
}

// GetSlackAccountByUserID returns the owner's Slack account, or (nil, nil) when
// none is connected so callers can treat "not connected" cleanly.
func (s *Store) GetSlackAccountByUserID(ctx context.Context, userID string) (*domain.SlackAccount, error) {
	a := new(domain.SlackAccount)
	err := s.db.NewSelect().Model(a).Where("user_id = ?", userID).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting slack account: %w", err)
	}
	return a, nil
}

// GetAnySlackAccount returns the single connected Slack account regardless of
// owner, or (nil, nil) when none exists. Slack is single-owner (one row total),
// so this is the canonical way to resolve "the connected account".
func (s *Store) GetAnySlackAccount(ctx context.Context) (*domain.SlackAccount, error) {
	a := new(domain.SlackAccount)
	err := s.db.NewSelect().Model(a).Order("id ASC").Limit(1).Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting any slack account: %w", err)
	}
	return a, nil
}

// DeleteSlackAccount removes the owner's Slack account (disconnect).
func (s *Store) DeleteSlackAccount(ctx context.Context, userID string) error {
	if _, err := s.db.NewDelete().
		Model((*domain.SlackAccount)(nil)).
		Where("user_id = ?", userID).
		Exec(ctx); err != nil {
		return fmt.Errorf("deleting slack account: %w", err)
	}
	return nil
}

// UpdateSlackTokens refreshes the stored (encrypted) tokens and expiry after a
// token rotation.
func (s *Store) UpdateSlackTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiry time.Time) error {
	_, err := s.db.NewUpdate().
		Model((*domain.SlackAccount)(nil)).
		Set("encrypted_access_token = ?", accessToken).
		Set("encrypted_refresh_token = ?", refreshToken).
		Set("token_expiry = ?", expiry).
		Set("updated_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating slack tokens: %w", err)
	}
	return nil
}
