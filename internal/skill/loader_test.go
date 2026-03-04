package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExternalManifests_FlatFile(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: greeting
description: Greeting style
---

Always greet the user warmly.`

	if err := os.WriteFile(filepath.Join(dir, "greeting.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	manifests, err := LoadExternalManifests(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("manifests len = %d, want 1", len(manifests))
	}

	m := manifests[0]
	if m.Name != "greeting" {
		t.Errorf("name = %q, want %q", m.Name, "greeting")
	}
	if m.Type != TextOnly {
		t.Errorf("type = %v, want TextOnly", m.Type)
	}
	if m.SystemPrompt != "Always greet the user warmly." {
		t.Errorf("prompt = %q", m.SystemPrompt)
	}
}

func TestLoadExternalManifests_DirectoryFormat(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "coding-style")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	skillMD := `---
name: coding-style
description: Coding conventions
---

Follow clean code principles.`

	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0644); err != nil {
		t.Fatal(err)
	}

	manifests, err := LoadExternalManifests(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("manifests len = %d, want 1", len(manifests))
	}

	m := manifests[0]
	if m.Name != "coding-style" {
		t.Errorf("name = %q, want %q", m.Name, "coding-style")
	}
}

func TestLoadExternalManifests_WithConfig(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	skillMD := `---
name: my-skill
description: Configurable skill
---

Do something configurable.`

	configYAML := `key: value
nested:
  setting: 42`

	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	manifests, err := LoadExternalManifests(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("manifests len = %d, want 1", len(manifests))
	}

	m := manifests[0]
	if m.Config == nil {
		t.Fatal("expected config, got nil")
	}
	if v, ok := m.Config["key"]; !ok || v != "value" {
		t.Errorf("config[key] = %v, want %q", v, "value")
	}
}

func TestLoadExternalManifests_NonExistentDir(t *testing.T) {
	manifests, err := LoadExternalManifests("/nonexistent/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if manifests != nil {
		t.Errorf("expected nil, got %v", manifests)
	}
}

func TestLoadExternalManifests_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	manifests, err := LoadExternalManifests(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(manifests) != 0 {
		t.Errorf("manifests len = %d, want 0", len(manifests))
	}
}

func TestLoadExternalManifests_DefaultName(t *testing.T) {
	dir := t.TempDir()
	// File without name in frontmatter — should use filename.
	content := `---
description: No name provided
---

Content here.`

	if err := os.WriteFile(filepath.Join(dir, "my-default.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	manifests, err := LoadExternalManifests(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("manifests len = %d, want 1", len(manifests))
	}
	if manifests[0].Name != "my-default" {
		t.Errorf("name = %q, want %q", manifests[0].Name, "my-default")
	}
}

func TestLoadManifestFromDir(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: internal_skill
description: An internal skill
capabilities:
  - memory
---

Internal skill system prompt.`

	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	m, err := LoadManifestFromDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected manifest, got nil")
	}
	if m.Name != "internal_skill" {
		t.Errorf("name = %q, want %q", m.Name, "internal_skill")
	}
	if m.Type != Internal {
		t.Errorf("type = %v, want Internal", m.Type)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0] != "memory" {
		t.Errorf("capabilities = %v, want [memory]", m.Capabilities)
	}
}

func TestLoadManifestFromDir_NoFile(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadManifestFromDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Errorf("expected nil for missing SKILL.md, got %+v", m)
	}
}
