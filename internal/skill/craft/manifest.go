package craft

import (
	"embed"

	"github.com/iulita-ai/iulita/internal/skill"
)

//go:embed SKILL.md
var skillFS embed.FS

// LoadManifest loads the embedded SKILL.md manifest.
func LoadManifest() (*skill.Manifest, error) {
	return skill.LoadManifestFromFS(skillFS, "SKILL.md")
}
