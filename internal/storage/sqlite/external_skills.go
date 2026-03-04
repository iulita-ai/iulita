package sqlite

import (
	"context"
	"fmt"

	"github.com/iulita-ai/iulita/internal/domain"
)

// SaveInstalledSkill inserts or updates (upsert by slug) an installed skill record.
func (s *Store) SaveInstalledSkill(ctx context.Context, sk *domain.InstalledSkill) error {
	_, err := s.db.NewInsert().
		Model(sk).
		On("CONFLICT (slug) DO UPDATE").
		Set("name = EXCLUDED.name").
		Set("version = EXCLUDED.version").
		Set("source = EXCLUDED.source").
		Set("source_ref = EXCLUDED.source_ref").
		Set("isolation = EXCLUDED.isolation").
		Set("install_dir = EXCLUDED.install_dir").
		Set("enabled = EXCLUDED.enabled").
		Set("checksum = EXCLUDED.checksum").
		Set("description = EXCLUDED.description").
		Set("author = EXCLUDED.author").
		Set("tags = EXCLUDED.tags").
		Set("capabilities = EXCLUDED.capabilities").
		Set("config_keys = EXCLUDED.config_keys").
		Set("secret_keys = EXCLUDED.secret_keys").
		Set("requires_bins = EXCLUDED.requires_bins").
		Set("requires_env = EXCLUDED.requires_env").
		Set("allowed_tools = EXCLUDED.allowed_tools").
		Set("has_code = EXCLUDED.has_code").
		Set("effective_mode = EXCLUDED.effective_mode").
		Set("install_warnings = EXCLUDED.install_warnings").
		Set("updated_at = CURRENT_TIMESTAMP").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("save installed skill %q: %w", sk.Slug, err)
	}
	return nil
}

// GetInstalledSkill returns a single installed skill by slug.
func (s *Store) GetInstalledSkill(ctx context.Context, slug string) (*domain.InstalledSkill, error) {
	sk := new(domain.InstalledSkill)
	err := s.db.NewSelect().
		Model(sk).
		Where("slug = ?", slug).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get installed skill %q: %w", slug, err)
	}
	return sk, nil
}

// ListInstalledSkills returns all installed skills, ordered by name.
func (s *Store) ListInstalledSkills(ctx context.Context) ([]domain.InstalledSkill, error) {
	var skills []domain.InstalledSkill
	err := s.db.NewSelect().
		Model(&skills).
		OrderExpr("name ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list installed skills: %w", err)
	}
	return skills, nil
}

// UpdateInstalledSkill updates an installed skill record by slug.
func (s *Store) UpdateInstalledSkill(ctx context.Context, sk *domain.InstalledSkill) error {
	_, err := s.db.NewUpdate().
		Model(sk).
		Where("slug = ?", sk.Slug).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("update installed skill %q: %w", sk.Slug, err)
	}
	return nil
}

// DeleteInstalledSkill removes an installed skill record by slug.
func (s *Store) DeleteInstalledSkill(ctx context.Context, slug string) error {
	_, err := s.db.NewDelete().
		Model((*domain.InstalledSkill)(nil)).
		Where("slug = ?", slug).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete installed skill %q: %w", slug, err)
	}
	return nil
}
