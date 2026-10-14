package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// SaveHTTPArtifact inserts new artifact metadata.
func (s *Store) SaveHTTPArtifact(ctx context.Context, a *domain.HTTPArtifact) error {
	_, err := s.db.NewInsert().Model(a).Exec(ctx)
	return err
}

// GetHTTPArtifact fetches one row by ID.
func (s *Store) GetHTTPArtifact(ctx context.Context, id string) (*domain.HTTPArtifact, error) {
	a := new(domain.HTTPArtifact)
	err := s.db.NewSelect().Model(a).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("artifact %s: %w", id, storage.ErrNotFound)
	}
	return a, err
}

// ListHTTPArtifactsByOwner returns an owner's artifacts, newest first.
func (s *Store) ListHTTPArtifactsByOwner(ctx context.Context, ownerUserID string) ([]domain.HTTPArtifact, error) {
	var out []domain.HTTPArtifact
	err := s.db.NewSelect().Model(&out).
		Where("owner_user_id = ?", ownerUserID).
		Order("created_at DESC").
		Limit(200).
		Scan(ctx)
	return out, err
}

// ListAllHTTPArtifacts returns every row (sweeper reconciliation).
func (s *Store) ListAllHTTPArtifacts(ctx context.Context) ([]domain.HTTPArtifact, error) {
	var out []domain.HTTPArtifact
	err := s.db.NewSelect().Model(&out).Scan(ctx)
	return out, err
}

// UpdateHTTPArtifact persists metadata changes.
func (s *Store) UpdateHTTPArtifact(ctx context.Context, a *domain.HTTPArtifact) error {
	_, err := s.db.NewUpdate().Model(a).WherePK().Exec(ctx)
	return err
}

// DeleteHTTPArtifact removes a row.
func (s *Store) DeleteHTTPArtifact(ctx context.Context, id string) error {
	_, err := s.db.NewDelete().Model((*domain.HTTPArtifact)(nil)).Where("id = ?", id).Exec(ctx)
	return err
}

// DeleteExpiredHTTPArtifacts removes rows past their TTL and returns them so
// the caller can delete the files.
func (s *Store) DeleteExpiredHTTPArtifacts(ctx context.Context, before time.Time) ([]domain.HTTPArtifact, error) {
	var expired []domain.HTTPArtifact
	err := s.db.NewSelect().Model(&expired).
		Where("expires_at < ?", before).
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	for i := range expired {
		if _, derr := s.db.NewDelete().Model((*domain.HTTPArtifact)(nil)).Where("id = ?", expired[i].ID).Exec(ctx); derr != nil {
			return expired, derr
		}
	}
	return expired, nil
}

// SumHTTPArtifactBytes sums stored ready+partial bytes (quota truth).
func (s *Store) SumHTTPArtifactBytes(ctx context.Context) (int64, error) {
	var sum sql.NullInt64
	err := s.db.NewSelect().Model((*domain.HTTPArtifact)(nil)).
		ColumnExpr("COALESCE(SUM(size), 0)").
		Where("state IN ('partial', 'ready')").
		Scan(ctx, &sum)
	return sum.Int64, err
}

// SaveHTTPStandingGrant persists a standing mutation approval.
func (s *Store) SaveHTTPStandingGrant(ctx context.Context, g *domain.HTTPStandingGrant) error {
	_, err := s.db.NewInsert().Model(g).Exec(ctx)
	return err
}

// HasHTTPStandingGrant reports whether an unrevoked, unexpired grant covers
// the exact origin (or globally) for the owner.
func (s *Store) HasHTTPStandingGrant(ctx context.Context, owner, scheme, host, port string) (bool, error) {
	count, err := s.db.NewSelect().Model((*domain.HTTPStandingGrant)(nil)).
		Where("owner_user_id = ?", owner).
		Where("revoked_at IS NULL").
		Where("expires_at > ?", time.Now()).
		Where("(scope = ? OR (scope = ? AND scheme = ? AND LOWER(host) = ? AND port = ?))",
			domain.HTTPStandingScopeGlobal,
			domain.HTTPStandingScopeOrigin, scheme, strings.ToLower(host), port).
		Count(ctx)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListHTTPStandingGrants returns an owner's grants, newest first.
func (s *Store) ListHTTPStandingGrants(ctx context.Context, owner string) ([]domain.HTTPStandingGrant, error) {
	var out []domain.HTTPStandingGrant
	err := s.db.NewSelect().Model(&out).
		Where("owner_user_id = ?", owner).
		Order("id DESC").
		Limit(200).
		Scan(ctx)
	return out, err
}

// RevokeHTTPStandingGrant marks a grant revoked (idempotent).
func (s *Store) RevokeHTTPStandingGrant(ctx context.Context, id int64) error {
	_, err := s.db.NewUpdate().Model((*domain.HTTPStandingGrant)(nil)).
		Set("revoked_at = ?", time.Now()).
		Where("id = ? AND revoked_at IS NULL", id).
		Exec(ctx)
	return err
}

// DeleteExpiredHTTPStandingGrants removes dead rows (housekeeping).
func (s *Store) DeleteExpiredHTTPStandingGrants(ctx context.Context) error {
	_, err := s.db.NewDelete().Model((*domain.HTTPStandingGrant)(nil)).
		Where("expires_at < ? OR revoked_at IS NOT NULL", time.Now().Add(-24*time.Hour)).
		Exec(ctx)
	return err
}
