package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func (s *Store) CreateAgentJob(ctx context.Context, j *domain.AgentJob) error {
	j.CreatedAt = time.Now()
	j.UpdatedAt = time.Now()
	_, err := s.db.NewInsert().Model(j).Exec(ctx)
	if err != nil {
		return fmt.Errorf("inserting agent job: %w", err)
	}
	return nil
}

func (s *Store) GetAgentJob(ctx context.Context, id int64) (*domain.AgentJob, error) {
	j := new(domain.AgentJob)
	err := s.db.NewSelect().Model(j).Where("id = ?", id).Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting agent job: %w", err)
	}
	return j, nil
}

func (s *Store) ListAgentJobs(ctx context.Context) ([]domain.AgentJob, error) {
	var jobs []domain.AgentJob
	err := s.db.NewSelect().Model(&jobs).Order("id ASC").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing agent jobs: %w", err)
	}
	return jobs, nil
}

func (s *Store) UpdateAgentJob(ctx context.Context, j *domain.AgentJob) error {
	j.UpdatedAt = time.Now()
	_, err := s.db.NewUpdate().Model(j).WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating agent job: %w", err)
	}
	return nil
}

func (s *Store) DeleteAgentJob(ctx context.Context, id int64) error {
	_, err := s.db.NewDelete().Model((*domain.AgentJob)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting agent job: %w", err)
	}
	return nil
}

func (s *Store) GetDueAgentJobs(ctx context.Context, now time.Time) ([]domain.AgentJob, error) {
	var jobs []domain.AgentJob
	err := s.db.NewSelect().Model(&jobs).
		Where("enabled = ?", true).
		Where("(next_run IS NULL OR next_run <= ?)", now).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting due agent jobs: %w", err)
	}
	return jobs, nil
}

func (s *Store) UpdateAgentJobSchedule(ctx context.Context, id int64, lastRun, nextRun time.Time) error {
	_, err := s.db.NewUpdate().
		Model((*domain.AgentJob)(nil)).
		Set("last_run = ?", lastRun).
		Set("next_run = ?", nextRun).
		Set("updated_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("updating agent job schedule: %w", err)
	}
	return nil
}
