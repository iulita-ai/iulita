package domain

import "time"

// IsolationLevel defines how an external skill executes.
type IsolationLevel string

const (
	IsolationTextOnly IsolationLevel = "text_only" // System prompt injection only
	IsolationShell    IsolationLevel = "shell"     // Via existing shellexec with allowlist
	IsolationDocker   IsolationLevel = "docker"    // Docker container sandbox
	IsolationWASM     IsolationLevel = "wasm"      // WASM sandbox (wazero)
)

// InstalledSkill represents an external skill installed from a marketplace or URL.
type InstalledSkill struct {
	ID        int64  `bun:",pk,autoincrement" json:"id"`
	Slug      string `bun:",notnull,unique" json:"slug"`           // unique identifier (e.g. "github-pr-review")
	Name      string `bun:",notnull" json:"name"`                  // human-readable name
	Version   string `bun:",notnull,default:''" json:"version"`    // semver or commit hash
	Source    string `bun:",notnull" json:"source"`                // "clawhub", "url", "git", "local"
	SourceRef string `bun:",notnull,default:''" json:"source_ref"` // original URL or repo path

	Isolation  IsolationLevel `bun:",notnull,default:'text_only'" json:"isolation"`
	InstallDir string         `bun:",notnull" json:"install_dir"` // absolute path to extracted skill directory

	Enabled     bool   `bun:",notnull,default:true" json:"enabled"`
	Pinned      bool   `bun:",notnull,default:false" json:"pinned"` // prevent auto-update
	Checksum    string `bun:",notnull,default:''" json:"checksum"`  // SHA256 of archive
	Description string `bun:",notnull,default:''" json:"description"`
	Author      string `bun:",notnull,default:''" json:"author"`
	Tags        string `bun:",notnull,default:''" json:"tags"` // comma-separated

	// Rich metadata (JSON-encoded arrays persisted from SKILL.md manifest).
	Capabilities    string `bun:",notnull,default:''" json:"capabilities"`  // JSON array
	ConfigKeys      string `bun:",notnull,default:''" json:"config_keys"`   // JSON array
	SecretKeys      string `bun:",notnull,default:''" json:"secret_keys"`   // JSON array
	RequiresBins    string `bun:",notnull,default:''" json:"requires_bins"` // JSON array
	RequiresEnv     string `bun:",notnull,default:''" json:"requires_env"`  // JSON array
	AllowedTools    string `bun:",notnull,default:''" json:"allowed_tools"` // JSON array
	HasCode         bool   `bun:",notnull,default:false" json:"has_code"`
	EffectiveMode   string `bun:",notnull,default:''" json:"effective_mode"`   // actual runtime mode after fallback
	InstallWarnings string `bun:",notnull,default:''" json:"install_warnings"` // JSON array of warning strings

	InstalledAt time.Time  `bun:",notnull,default:current_timestamp" json:"installed_at"`
	UpdatedAt   *time.Time `bun:",nullzero" json:"updated_at,omitempty"`
}
