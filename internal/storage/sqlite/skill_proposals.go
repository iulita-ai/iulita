package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// SaveSkillProposal inserts a new self-authored skill proposal.
func (s *Store) SaveSkillProposal(ctx context.Context, p *domain.SkillProposal) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if p.Status == "" {
		p.Status = domain.SkillProposalPending
	}
	if _, err := s.db.NewInsert().Model(p).Exec(ctx); err != nil {
		return fmt.Errorf("inserting skill proposal: %w", err)
	}
	return nil
}

// ListSkillProposals returns proposals (newest first), optionally filtered by status.
func (s *Store) ListSkillProposals(ctx context.Context, filter storage.SkillProposalFilter) ([]domain.SkillProposal, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []domain.SkillProposal
	q := s.db.NewSelect().Model(&rows).Order("created_at DESC").Limit(limit)
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("listing skill proposals: %w", err)
	}
	return rows, nil
}

// GetSkillProposal fetches a single proposal by id.
func (s *Store) GetSkillProposal(ctx context.Context, id int64) (*domain.SkillProposal, error) {
	p := new(domain.SkillProposal)
	err := s.db.NewSelect().Model(p).Where("id = ?", id).Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting skill proposal %d: %w", id, err)
	}
	return p, nil
}

// UpdateSkillProposalStatus sets the lifecycle status of a proposal.
func (s *Store) UpdateSkillProposalStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.NewUpdate().
		Model((*domain.SkillProposal)(nil)).
		Set("status = ?", status).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating skill proposal %d status: %w", id, err)
	}
	return nil
}
