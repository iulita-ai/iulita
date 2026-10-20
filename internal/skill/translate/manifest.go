package translate

import (
	"embed"

	"github.com/iulita-ai/iulita/internal/skill"
)

//go:embed SKILL.md
var skillFS embed.FS

// LoadManifest loads translation instructions and its nonsecret opt-in setting.
func LoadManifest() (*skill.Manifest, error) { return skill.LoadManifestFromFS(skillFS, "SKILL.md") }
