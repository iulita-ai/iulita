package skillmgr

import (
	"context"

	"github.com/iulita-ai/iulita/internal/domain"
)

// SkillStore abstracts persistence for installed external skills.
// Implemented by storage.Repository (subset).
type SkillStore interface {
	SaveInstalledSkill(ctx context.Context, s *domain.InstalledSkill) error
	GetInstalledSkill(ctx context.Context, slug string) (*domain.InstalledSkill, error)
	ListInstalledSkills(ctx context.Context) ([]domain.InstalledSkill, error)
	UpdateInstalledSkill(ctx context.Context, s *domain.InstalledSkill) error
	DeleteInstalledSkill(ctx context.Context, slug string) error
}
