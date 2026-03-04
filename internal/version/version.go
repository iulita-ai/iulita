package version

import (
	"fmt"
	"runtime/debug"
)

// Set via ldflags at build time:
//
//	-ldflags "-X github.com/iulita-ai/iulita/internal/version.Version=v0.1.0
//	          -X github.com/iulita-ai/iulita/internal/version.Commit=abc1234
//	          -X github.com/iulita-ai/iulita/internal/version.Date=2026-03-06"
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

func init() {
	if Version != "" {
		return
	}
	// Fallback: extract VCS info from Go build metadata (go 1.18+).
	info, ok := debug.ReadBuildInfo()
	if !ok {
		Version = "dev"
		return
	}
	Version = "dev"
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) > 8 {
				Commit = s.Value[:8]
			} else {
				Commit = s.Value
			}
		case "vcs.time":
			Date = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if dirty && Commit != "" {
		Commit += "-dirty"
	}
}

// String returns a human-readable version string.
func String() string {
	v := Version
	if Commit != "" {
		v += fmt.Sprintf(" (%s", Commit)
		if Date != "" {
			v += ", " + Date
		}
		v += ")"
	}
	return v
}

// Short returns just the version tag or "dev".
func Short() string {
	return Version
}
