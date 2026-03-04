package geolocation

import (
	"embed"

	"github.com/iulita-ai/iulita/internal/skill"
)

//go:embed SKILL.md
var skillFS embed.FS

// LoadManifest loads the geolocation skill manifest from embedded SKILL.md.
func LoadManifest() (*skill.Manifest, error) {
	return skill.LoadManifestFromFS(skillFS, "SKILL.md")
}
