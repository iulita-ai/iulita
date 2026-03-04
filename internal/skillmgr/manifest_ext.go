package skillmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iulita-ai/iulita/internal/skill"
	"gopkg.in/yaml.v3"
)

// codeExtensions are file extensions that indicate executable code.
var codeExtensions = map[string]bool{
	".py": true, ".js": true, ".ts": true, ".go": true,
	".rb": true, ".sh": true, ".bash": true, ".pl": true,
}

// extendedFrontmatter represents the YAML frontmatter from an external SKILL.md.
type extendedFrontmatter struct {
	Name         string     `yaml:"name"`
	Description  string     `yaml:"description"`
	Version      string     `yaml:"version"`
	Isolation    string     `yaml:"isolation"`
	Capabilities []string   `yaml:"capabilities"`
	ConfigKeys   []string   `yaml:"config_keys"`
	SecretKeys   []string   `yaml:"secret_keys"`
	EntryPoint   string     `yaml:"entry_point"`
	DockerImage  string     `yaml:"docker_image"`
	AllowedTools []string   `yaml:"allowed_tools"`
	Metadata     yamlAnyStr `yaml:"metadata"` // ClawhHub: JSON or YAML map with clawdbot metadata
	Requires     struct {
		Bins    []string     `yaml:"bins"`
		Env     []string     `yaml:"env"`
		Skills  []string     `yaml:"skills"`
		Install []installDep `yaml:"install"`
	} `yaml:"requires"`
}

type installDep struct {
	Kind     string   `yaml:"kind"`
	Packages []string `yaml:"packages"`
}

// yamlAnyStr accepts both a YAML string and a YAML map/object,
// converting either to a JSON string for uniform processing.
type yamlAnyStr string

func (s *yamlAnyStr) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		*s = yamlAnyStr(value.Value)
		return nil
	}
	// For map/sequence nodes, decode to interface{} then marshal to JSON.
	var raw interface{}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	*s = yamlAnyStr(data)
	return nil
}

// ParsedManifest is the result of parsing an external SKILL.md.
type ParsedManifest struct {
	Manifest     *skill.Manifest
	HasCode      bool     // true if code files found in the skill directory
	Warnings     []string // non-fatal warnings (prompt injection patterns, etc.)
	Entrypoint   string
	DockerImage  string
	Requires     ExternalRequires
	AllowedTools []string
}

// ExternalRequires describes dependencies declared in the skill manifest.
type ExternalRequires struct {
	Bins    []string
	Env     []string
	Skills  []string
	Install []InstallDep
}

// InstallDep describes a package manager dependency.
type InstallDep struct {
	Kind     string
	Packages []string
}

// ParseExternalManifest reads a SKILL.md from a directory and returns a ParsedManifest.
// It also detects code files and auto-sets isolation level if not declared.
func ParseExternalManifest(dir string, slug, source, sourceRef string) (*ParsedManifest, error) {
	skillMDPath := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(skillMDPath)
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md: %w", err)
	}

	frontmatter, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	var fm extendedFrontmatter
	if err := yaml.Unmarshal(frontmatter, &fm); err != nil {
		return nil, fmt.Errorf("unmarshal frontmatter: %w", err)
	}

	if fm.Name == "" {
		fm.Name = slug
	}

	// Build warnings.
	var warnings []string

	// Merge clawdbot metadata requires into frontmatter requires.
	if w := mergeClawdbotRequires(&fm); w != "" {
		warnings = append(warnings, w)
	}

	// Detect code files in the skill directory.
	hasCode := detectCodeFiles(dir)

	// Auto-detect isolation level if not declared.
	isolation := fm.Isolation
	if isolation == "" {
		if hasCode {
			isolation = "docker"
		} else if len(fm.Requires.Bins) > 0 {
			isolation = "shell"
		} else {
			isolation = "text_only"
		}
	}
	warnings = append(warnings, scanForInjection(body)...)

	if hasCode && (isolation == "text_only" || isolation == "shell") {
		return nil, fmt.Errorf("skill %q contains code files but declares isolation=%q; must use 'docker' or 'wasm'", slug, isolation)
	}

	// Convert requires.
	var reqs ExternalRequires
	reqs.Bins = fm.Requires.Bins
	reqs.Env = fm.Requires.Env
	reqs.Skills = fm.Requires.Skills
	for _, d := range fm.Requires.Install {
		reqs.Install = append(reqs.Install, InstallDep{Kind: d.Kind, Packages: d.Packages})
	}

	// Build skill type.
	skillType := skill.TextOnly
	if hasCode || isolation == "docker" || isolation == "wasm" || isolation == "shell" {
		skillType = skill.Internal
	}

	manifest := &skill.Manifest{
		Name:         fm.Name,
		Description:  fm.Description,
		Type:         skillType,
		SystemPrompt: strings.TrimSpace(body),
		Capabilities: fm.Capabilities,
		ConfigKeys:   fm.ConfigKeys,
		SecretKeys:   fm.SecretKeys,
		External: &skill.ExternalManifestExt{
			Slug:       slug,
			Source:     source,
			SourceRef:  sourceRef,
			Version:    fm.Version,
			Isolation:  isolation,
			InstallDir: dir,
			HasCode:    hasCode,
			Entrypoint: fm.EntryPoint,
		},
	}

	return &ParsedManifest{
		Manifest:     manifest,
		HasCode:      hasCode,
		Warnings:     warnings,
		Entrypoint:   fm.EntryPoint,
		DockerImage:  fm.DockerImage,
		Requires:     reqs,
		AllowedTools: fm.AllowedTools,
	}, nil
}

// splitFrontmatter separates YAML frontmatter from markdown body.
func splitFrontmatter(data []byte) (frontmatter []byte, body string, err error) {
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return nil, content, nil
	}

	end := strings.Index(content[3:], "\n---")
	if end == -1 {
		return nil, content, nil
	}

	fm := content[3 : end+3]
	rest := content[end+7:] // skip "\n---"
	return []byte(fm), rest, nil
}

// mergeClawdbotRequires extracts requires from ClawhHub's clawdbot metadata JSON
// and merges them into the frontmatter requires (if not already present).
// ClawhHub format: metadata: {"clawdbot":{"requires":{"bins":["curl"]}}}
func mergeClawdbotRequires(fm *extendedFrontmatter) string {
	if string(fm.Metadata) == "" {
		return ""
	}
	var meta struct {
		Clawdbot struct {
			Requires struct {
				Bins []string `json:"bins"`
			} `json:"requires"`
		} `json:"clawdbot"`
	}
	if err := json.Unmarshal([]byte(string(fm.Metadata)), &meta); err != nil {
		return fmt.Sprintf("metadata field is not valid JSON, clawdbot requires ignored: %v", err)
	}
	// Merge bins that aren't already declared.
	if len(meta.Clawdbot.Requires.Bins) > 0 {
		existing := make(map[string]bool, len(fm.Requires.Bins))
		for _, b := range fm.Requires.Bins {
			existing[b] = true
		}
		for _, b := range meta.Clawdbot.Requires.Bins {
			if !existing[b] {
				fm.Requires.Bins = append(fm.Requires.Bins, b)
			}
		}
	}
	return ""
}

// detectCodeFiles walks the skill directory and returns true if any code files exist.
func detectCodeFiles(dir string) bool {
	found := false
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if codeExtensions[ext] {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
