package skillmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iulita-ai/iulita/internal/domain"
)

// InstallAuthored installs a vetted, machine-authored text-only skill draft.
// It re-runs the security scan as defense-in-depth, writes a SKILL.md into a
// staging directory on the same filesystem as the install dir (so the atomic
// rename inside Install succeeds), and installs it via the local source.
//
// Authored skills are always text_only: the body becomes standing system-prompt
// guidance. No code and no custom force-triggers are emitted from this path.
func (m *Manager) InstallAuthored(ctx context.Context, slug, name, description, body string, triggers []string) (*domain.InstalledSkill, []string, error) {
	if warnings, blocked := ScanAuthoredSkill(slug, name, body, triggers); blocked {
		return nil, nil, fmt.Errorf("authored skill failed security scan: %s", strings.Join(warnings, "; "))
	}

	// Stage inside cfg.Dir so Install's final os.Rename stays on one filesystem.
	staging, err := os.MkdirTemp(m.cfg.Dir, "authored-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging) //nolint:errcheck // best-effort cleanup of leftover staging

	skillDir := filepath.Join(staging, slug)
	if err := os.MkdirAll(skillDir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("create skill dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(renderAuthoredSkillMD(name, description, body)), 0o600); err != nil {
		return nil, nil, fmt.Errorf("write SKILL.md: %w", err)
	}

	return m.Install(ctx, "local", skillDir)
}

// renderAuthoredSkillMD builds a text-only SKILL.md. Frontmatter scalars are
// JSON-encoded (valid YAML double-quoted scalars) so arbitrary names/descriptions
// can't break the frontmatter or inject extra keys.
func renderAuthoredSkillMD(name, description, body string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", yamlScalar(name))
	fmt.Fprintf(&b, "description: %s\n", yamlScalar(description))
	b.WriteString("isolation: text_only\n")
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String()
}

// yamlScalar returns a safely-quoted YAML scalar for an arbitrary string.
func yamlScalar(s string) string {
	encoded, err := json.Marshal(s) // JSON string == valid YAML flow scalar
	if err != nil {
		return `""`
	}
	return string(encoded)
}
