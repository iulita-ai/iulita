package skill

// SkillType distinguishes between text-only and internal (code-based) skills.
type SkillType int

const (
	// TextOnly skills are loaded from external markdown files.
	// They inject instructions into the system prompt but don't execute code.
	TextOnly SkillType = iota
	// Internal skills have Go code and optionally a SKILL.md system prompt.
	Internal
)

// Manifest describes a skill's metadata, system prompt, and configuration.
// Both text-only and internal skills use manifests for unified discovery.
type Manifest struct {
	Name          string
	Description   string
	Type          SkillType
	SystemPrompt  string               // markdown content injected into system prompt
	Capabilities  []string             // required capabilities (e.g. "memory", "web")
	Config        map[string]any       // merged config (defaults + overrides)
	ConfigKeys    []string             // runtime-overridable config keys (e.g. "skills.craft.secret_link_id")
	SecretKeys    []string             // subset of ConfigKeys that must be encrypted and hidden from UI
	External      *ExternalManifestExt // non-nil for external skills
	ForceTriggers []string             // lowercase keywords that force this tool via tool_choice (e.g. "погода", "weather")
}

// ExternalManifestExt holds metadata specific to external (marketplace) skills.
type ExternalManifestExt struct {
	Slug       string   // unique skill identifier (e.g. "github-pr-review")
	Source     string   // "clawhub", "url", "git", "local"
	SourceRef  string   // original URL or repo path
	Version    string   // semver or commit hash
	Author     string   // skill author
	Tags       []string // categorization tags
	Isolation  string   // "text_only", "shell", "docker", "wasm"
	InstallDir string   // absolute path to installed files
	HasCode    bool     // true if skill contains executable files (.py, .js, .go)
	Entrypoint string   // main executable (e.g. "main.py") — only for docker/wasm
}
