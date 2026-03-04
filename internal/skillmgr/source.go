package skillmgr

import "context"

// SkillRef describes a resolved skill from a marketplace or URL.
type SkillRef struct {
	Slug             string // unique identifier (e.g. "weather-brief")
	Name             string // human-readable name
	Version          string // semver or commit hash
	Description      string
	Author           string
	OwnerDisplayName string // human-readable owner name (e.g. "Peter Steinberger")
	Tags             []string
	DownloadURL      string // URL to fetch the archive
	Checksum         string // expected SHA256 (empty = skip verification)
	Source           string // "clawhub", "url", "git", "local"
	SourceRef        string // original reference (URL, repo path, etc.)

	// Marketplace metadata (optional, from ClawhHub).
	Downloads int   // total downloads
	Stars     int   // star count
	UpdatedAt int64 // Unix timestamp of last update
}

// Source resolves and downloads skills from a specific origin.
type Source interface {
	// Name returns the source identifier (e.g. "clawhub", "url").
	Name() string

	// Resolve looks up a skill reference and returns metadata.
	Resolve(ctx context.Context, ref string) (*SkillRef, error)

	// Download fetches the skill archive to a local path.
	// Returns the path to the downloaded file and its SHA256 checksum.
	Download(ctx context.Context, ref *SkillRef, destDir string) (archivePath string, checksum string, err error)

	// Search finds skills matching a query (marketplace sources only).
	Search(ctx context.Context, query string, limit int) ([]SkillRef, error)
}
