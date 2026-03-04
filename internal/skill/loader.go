package skill

import (
	"bufio"
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// skillFrontmatter is the YAML header parsed from SKILL.md files.
type skillFrontmatter struct {
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description"`
	Capabilities  []string `yaml:"capabilities"`
	ConfigKeys    []string `yaml:"config_keys"`
	SecretKeys    []string `yaml:"secret_keys"`
	ForceTriggers []string `yaml:"force_triggers"`
}

// LoadManifestFromFS reads a SKILL.md file from an embed.FS at the given path.
// Returns nil (no error) if the file doesn't exist.
func LoadManifestFromFS(fsys embed.FS, path string) (*Manifest, error) {
	data, err := fsys.ReadFile(path)
	if err != nil {
		return nil, nil // file not found — not an error
	}
	return parseManifest(data, Internal)
}

// LoadManifestFromDir reads a SKILL.md from a filesystem directory.
// Returns nil (no error) if the file doesn't exist.
func LoadManifestFromDir(dir string) (*Manifest, error) {
	path := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parseManifest(data, Internal)
}

// LoadExternalManifests discovers text-only skill manifests from an external directory.
// Supports two layouts:
//   - Flat files: dir/tone.md
//   - Directory format: dir/coding-style/SKILL.md
//
// Returns empty slice (not error) if the directory doesn't exist.
func LoadExternalManifests(dir string) ([]*Manifest, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat skills dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skills path is not a directory: %s", dir)
	}

	var manifests []*Manifest

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}

	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())

		if entry.IsDir() {
			// Directory format: look for SKILL.md inside
			skillFile := filepath.Join(path, "SKILL.md")
			data, err := os.ReadFile(skillFile)
			if err != nil {
				continue // no SKILL.md, skip
			}
			m, err := parseManifest(data, TextOnly)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", skillFile, err)
			}
			if m != nil {
				if m.Name == "" {
					m.Name = entry.Name()
				}
				// Load optional config.yaml
				cfgFile := filepath.Join(path, "config.yaml")
				if cfg, err := loadYAMLConfig(cfgFile); err == nil && cfg != nil {
					m.Config = cfg
				}
				manifests = append(manifests, m)
			}
			continue
		}

		// Flat format: *.md files
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		defaultName := strings.TrimSuffix(entry.Name(), ".md")
		m, err := parseManifest(data, TextOnly)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if m != nil {
			if m.Name == "" {
				m.Name = defaultName
			}
			manifests = append(manifests, m)
		}
	}

	return manifests, nil
}

// LoadEmbeddedManifest reads a SKILL.md from an embedded filesystem.
func LoadEmbeddedManifest(fsys fs.FS, path string) (*Manifest, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, nil
	}
	return parseManifest(data, Internal)
}

func parseManifest(data []byte, skillType SkillType) (*Manifest, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}

	fm, body, err := parseFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("invalid frontmatter: %w", err)
	}

	content := strings.TrimSpace(string(body))

	m := &Manifest{
		Type:         skillType,
		SystemPrompt: content,
	}

	if fm != nil {
		m.Name = fm.Name
		m.Description = fm.Description
		m.Capabilities = fm.Capabilities
		m.ConfigKeys = fm.ConfigKeys
		m.SecretKeys = fm.SecretKeys
		m.ForceTriggers = fm.ForceTriggers
	}

	return m, nil
}

// parseFrontmatter splits YAML frontmatter (delimited by ---) from the body.
func parseFrontmatter(data []byte) (*skillFrontmatter, []byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))

	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return nil, data, nil
	}

	var fmLines []string
	closed := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		fmLines = append(fmLines, line)
	}

	if !closed {
		return nil, data, nil
	}

	var fm skillFrontmatter
	fmData := []byte(strings.Join(fmLines, "\n"))
	if err := yaml.Unmarshal(fmData, &fm); err != nil {
		return nil, nil, fmt.Errorf("yaml: %w", err)
	}

	var bodyLines []string
	for scanner.Scan() {
		bodyLines = append(bodyLines, scanner.Text())
	}

	body := []byte(strings.Join(bodyLines, "\n"))
	return &fm, body, nil
}

// loadYAMLConfig reads a YAML config file and returns it as a map.
func loadYAMLConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil // not found is OK
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}
