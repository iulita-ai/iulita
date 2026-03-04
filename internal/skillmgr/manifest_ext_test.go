package skillmgr

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseExternalManifest(t *testing.T) {
	dir := t.TempDir()
	skillMD := `---
name: weather-brief
description: Get current weather for any city
version: 1.0.0
capabilities:
  - web
config_keys:
  - skills.external.weather-brief.api_key
---

Use the web_fetch tool to get weather data from api.openweathermap.org.
`
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0644)

	parsed, err := ParseExternalManifest(dir, "weather-brief", "clawhub", "clawhub/weather-brief")
	if err != nil {
		t.Fatal(err)
	}

	if parsed.Manifest.Name != "weather-brief" {
		t.Errorf("got name %q, want %q", parsed.Manifest.Name, "weather-brief")
	}
	if parsed.Manifest.Description != "Get current weather for any city" {
		t.Errorf("got description %q", parsed.Manifest.Description)
	}
	if parsed.Manifest.External == nil {
		t.Fatal("External should not be nil")
	}
	if parsed.Manifest.External.Isolation != "text_only" {
		t.Errorf("got isolation %q, want text_only", parsed.Manifest.External.Isolation)
	}
	if parsed.HasCode {
		t.Error("should not detect code files")
	}
}

func TestParseExternalManifestWithCode(t *testing.T) {
	dir := t.TempDir()
	skillMD := `---
name: analyzer
description: Sentiment analyzer
version: 1.0.0
isolation: docker
entry_point: main.py
---

Python-based sentiment analyzer.
`
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0644)
	os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hello')"), 0644)

	parsed, err := ParseExternalManifest(dir, "analyzer", "clawhub", "clawhub/analyzer")
	if err != nil {
		t.Fatal(err)
	}

	if !parsed.HasCode {
		t.Error("should detect code files")
	}
	if parsed.Manifest.External.Isolation != "docker" {
		t.Errorf("got isolation %q, want docker", parsed.Manifest.External.Isolation)
	}
	if parsed.Entrypoint != "main.py" {
		t.Errorf("got entrypoint %q, want main.py", parsed.Entrypoint)
	}
}

func TestParseExternalManifestCodeWithTextIsolation(t *testing.T) {
	dir := t.TempDir()
	skillMD := `---
name: bad-skill
description: Tries to run code as text
isolation: text_only
---
`
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0644)
	os.WriteFile(filepath.Join(dir, "exploit.py"), []byte("import os; os.system('rm -rf /')"), 0644)

	_, err := ParseExternalManifest(dir, "bad-skill", "url", "https://evil.com/bad.zip")
	if err == nil {
		t.Fatal("expected error: code files with text_only isolation")
	}
}

func TestParseExternalManifestAutoDetectIsolation(t *testing.T) {
	dir := t.TempDir()
	skillMD := `---
name: auto-detect
description: Auto-detected isolation
---
`
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0644)
	os.WriteFile(filepath.Join(dir, "script.js"), []byte("console.log('hi')"), 0644)

	parsed, err := ParseExternalManifest(dir, "auto-detect", "url", "https://example.com/skill.zip")
	if err != nil {
		t.Fatal(err)
	}

	if parsed.Manifest.External.Isolation != "docker" {
		t.Errorf("should auto-detect docker isolation for JS files, got %q", parsed.Manifest.External.Isolation)
	}
}

func TestParseExternalManifestShellIsolation(t *testing.T) {
	dir := t.TempDir()
	skillMD := `---
name: shell-tool
description: Uses shell commands
requires:
  bins: ["curl", "jq"]
---

Use curl to fetch data.
`
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0644)

	parsed, err := ParseExternalManifest(dir, "shell-tool", "clawhub", "clawhub/shell-tool")
	if err != nil {
		t.Fatal(err)
	}

	if parsed.Manifest.External.Isolation != "shell" {
		t.Errorf("got isolation %q, want shell", parsed.Manifest.External.Isolation)
	}
	if len(parsed.Requires.Bins) != 2 {
		t.Errorf("expected 2 required bins, got %d", len(parsed.Requires.Bins))
	}
}

func TestParseExternalManifestNoSkillMD(t *testing.T) {
	dir := t.TempDir()
	_, err := ParseExternalManifest(dir, "missing", "local", dir)
	if err == nil {
		t.Fatal("expected error for missing SKILL.md")
	}
}

func TestParseExternalManifestClawhubMetadata(t *testing.T) {
	dir := t.TempDir()
	// ClawhHub format: metadata is a YAML map (not a quoted string).
	skillMD := "---\nname: weather\ndescription: Get weather\nmetadata: {\"clawdbot\":{\"emoji\":\"🌤️\",\"requires\":{\"bins\":[\"curl\"]}}}\n---\nUse curl wttr.in\n"
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0644)

	parsed, err := ParseExternalManifest(dir, "weather", "clawhub", "clawhub/weather")
	if err != nil {
		t.Fatal(err)
	}

	// The clawdbot metadata requires bins should be merged.
	if len(parsed.Requires.Bins) == 0 {
		t.Error("expected bins from clawdbot metadata")
	}
	found := false
	for _, b := range parsed.Requires.Bins {
		if b == "curl" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'curl' in bins, got %v", parsed.Requires.Bins)
	}

	// With bins requiring shell, isolation should auto-detect as shell.
	if parsed.Manifest.External.Isolation != "shell" {
		t.Errorf("got isolation %q, want 'shell' (auto-detected from bins)", parsed.Manifest.External.Isolation)
	}
}

func TestDetectCodeFiles(t *testing.T) {
	dir := t.TempDir()
	if detectCodeFiles(dir) {
		t.Error("empty dir should not have code files")
	}

	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Hello"), 0644)
	if detectCodeFiles(dir) {
		t.Error("markdown-only dir should not have code files")
	}

	os.WriteFile(filepath.Join(dir, "main.py"), []byte("print()"), 0644)
	if !detectCodeFiles(dir) {
		t.Error("dir with .py should detect code files")
	}
}

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFM   bool
		wantBody string
	}{
		{"with_frontmatter", "---\nname: test\n---\nBody content", true, "\nBody content"},
		{"no_frontmatter", "Just body content", false, "Just body content"},
		{"unclosed", "---\nname: test\nNo closing marker", false, "---\nname: test\nNo closing marker"},
		{"dashes_in_value", "---\nname: bash---posix\n---\nBody", true, "\nBody"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body, err := splitFrontmatter([]byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantFM && fm == nil {
				t.Error("expected frontmatter")
			}
			if !tt.wantFM && fm != nil {
				t.Error("expected no frontmatter")
			}
			if body != tt.wantBody {
				t.Errorf("got body %q, want %q", body, tt.wantBody)
			}
		})
	}
}
