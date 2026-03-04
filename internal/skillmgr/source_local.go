package skillmgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// LocalSource implements Source for local directory-based skills.
type LocalSource struct{}

// NewLocalSource creates a local filesystem source.
func NewLocalSource() *LocalSource { return &LocalSource{} }

func (s *LocalSource) Name() string { return "local" }

func (s *LocalSource) Resolve(_ context.Context, ref string) (*SkillRef, error) {
	abs, err := filepath.Abs(ref)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", abs)
	}

	// Check SKILL.md exists.
	if _, err := os.Stat(filepath.Join(abs, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("no SKILL.md found in %q", abs)
	}

	slug := filepath.Base(abs)

	return &SkillRef{
		Slug:      slug,
		Name:      slug,
		Source:    "local",
		SourceRef: abs,
	}, nil
}

// Download for local source is a no-op — the skill is already on disk.
// It copies the directory instead of downloading an archive.
func (s *LocalSource) Download(_ context.Context, ref *SkillRef, destDir string) (string, string, error) {
	// For local source, we don't download — we signal the installer to copy directly.
	// Return the source path as "archive" with empty checksum.
	return ref.SourceRef, "", nil
}

func (s *LocalSource) Search(_ context.Context, _ string, _ int) ([]SkillRef, error) {
	return nil, fmt.Errorf("local source does not support search")
}
