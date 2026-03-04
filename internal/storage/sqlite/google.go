package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) SaveGoogleAccount(ctx context.Context, a *domain.GoogleAccount) error {
	_, err := s.db.NewInsert().Model(a).Exec(ctx)
	if err != nil {
		return fmt.Errorf("inserting google account: %w", err)
	}
	return nil
}

func (s *Store) GetGoogleAccount(ctx context.Context, id int64) (*domain.GoogleAccount, error) {
	a := new(domain.GoogleAccount)
	err := s.db.NewSelect().Model(a).Where("id = ?", id).Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting google account %d: %w", id, err)
	}
	return a, nil
}

func (s *Store) GetGoogleAccountByEmail(ctx context.Context, userID, email string) (*domain.GoogleAccount, error) {
	a := new(domain.GoogleAccount)
	err := s.db.NewSelect().Model(a).
		Where("user_id = ?", userID).
		Where("account_email = ?", email).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting google account by email: %w", err)
	}
	return a, nil
}

func (s *Store) GetDefaultGoogleAccount(ctx context.Context, userID string) (*domain.GoogleAccount, error) {
	a := new(domain.GoogleAccount)
	// Try default first, then fall back to first account.
	err := s.db.NewSelect().Model(a).
		Where("user_id = ?", userID).
		OrderExpr("is_default DESC, id ASC").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting default google account: %w", err)
	}
	return a, nil
}

func (s *Store) ListGoogleAccounts(ctx context.Context, userID string) ([]domain.GoogleAccount, error) {
	var accounts []domain.GoogleAccount
	err := s.db.NewSelect().Model(&accounts).
		Where("user_id = ?", userID).
		Order("id ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing google accounts: %w", err)
	}
	return accounts, nil
}

func (s *Store) DeleteGoogleAccount(ctx context.Context, id int64) error {
	_, err := s.db.NewDelete().Model((*domain.GoogleAccount)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting google account %d: %w", id, err)
	}
	return nil
}

func (s *Store) UpdateGoogleAccountMeta(ctx context.Context, id int64, alias string, isDefault bool) error {
	_, err := s.db.NewUpdate().Model((*domain.GoogleAccount)(nil)).
		Set("account_alias = ?", alias).
		Set("is_default = ?", isDefault).
		Set("updated_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating google account meta: %w", err)
	}
	return nil
}

func (s *Store) UpdateGoogleTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiry time.Time) error {
	_, err := s.db.NewUpdate().Model((*domain.GoogleAccount)(nil)).
		Set("encrypted_access_token = ?", accessToken).
		Set("encrypted_refresh_token = ?", refreshToken).
		Set("token_expiry = ?", expiry).
		Set("updated_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating google tokens: %w", err)
	}
	return nil
}
