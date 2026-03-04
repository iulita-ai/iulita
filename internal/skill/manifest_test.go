package skill

import (
	"testing"
)

func TestParseManifest_WithFrontmatter(t *testing.T) {
	data := []byte(`---
name: test_skill
description: A test skill
capabilities:
  - memory
  - web
---

This is the system prompt content.
It can span multiple lines.`)

	m, err := parseManifest(data, Internal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected manifest, got nil")
	}
	if m.Name != "test_skill" {
		t.Errorf("name = %q, want %q", m.Name, "test_skill")
	}
	if m.Description != "A test skill" {
		t.Errorf("description = %q, want %q", m.Description, "A test skill")
	}
	if m.Type != Internal {
		t.Errorf("type = %v, want Internal", m.Type)
	}
	if len(m.Capabilities) != 2 {
		t.Errorf("capabilities len = %d, want 2", len(m.Capabilities))
	}
	if m.SystemPrompt == "" {
		t.Error("system prompt is empty")
	}
	if m.SystemPrompt != "This is the system prompt content.\nIt can span multiple lines." {
		t.Errorf("system prompt = %q", m.SystemPrompt)
	}
}

func TestParseManifest_NoFrontmatter(t *testing.T) {
	data := []byte(`Just plain text content without frontmatter.`)

	m, err := parseManifest(data, TextOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected manifest, got nil")
	}
	if m.Name != "" {
		t.Errorf("name = %q, want empty", m.Name)
	}
	if m.Type != TextOnly {
		t.Errorf("type = %v, want TextOnly", m.Type)
	}
	if m.SystemPrompt != "Just plain text content without frontmatter." {
		t.Errorf("system prompt = %q", m.SystemPrompt)
	}
}

func TestParseManifest_EmptyContent(t *testing.T) {
	m, err := parseManifest([]byte(""), Internal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Errorf("expected nil manifest for empty content, got %+v", m)
	}
}

func TestParseManifest_FrontmatterOnly(t *testing.T) {
	data := []byte(`---
name: empty_body
description: No body content
---`)

	m, err := parseManifest(data, Internal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected manifest, got nil")
	}
	if m.Name != "empty_body" {
		t.Errorf("name = %q, want %q", m.Name, "empty_body")
	}
	if m.SystemPrompt != "" {
		t.Errorf("system prompt should be empty, got %q", m.SystemPrompt)
	}
}

func TestParseManifest_EmptyCapabilities(t *testing.T) {
	data := []byte(`---
name: no_caps
capabilities: []
---

Some content.`)

	m, err := parseManifest(data, Internal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected manifest, got nil")
	}
	if len(m.Capabilities) != 0 {
		t.Errorf("capabilities len = %d, want 0", len(m.Capabilities))
	}
}

func TestParseManifest_WithConfigKeys(t *testing.T) {
	data := []byte(`---
name: craft
description: Craft integration
capabilities:
  - craft
config_keys:
  - skills.craft.secret_link_id
  - skills.craft.system_prompt
---

Use craft tools to access documents.`)

	m, err := parseManifest(data, Internal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected manifest, got nil")
	}
	if len(m.ConfigKeys) != 2 {
		t.Fatalf("ConfigKeys len = %d, want 2", len(m.ConfigKeys))
	}
	if m.ConfigKeys[0] != "skills.craft.secret_link_id" {
		t.Errorf("ConfigKeys[0] = %q", m.ConfigKeys[0])
	}
	if m.ConfigKeys[1] != "skills.craft.system_prompt" {
		t.Errorf("ConfigKeys[1] = %q", m.ConfigKeys[1])
	}
}

func TestParseManifest_WithSecretKeys(t *testing.T) {
	data := []byte(`---
name: craft
description: Craft integration
config_keys:
  - skills.craft.api_url
  - skills.craft.api_key
  - skills.craft.system_prompt
secret_keys:
  - skills.craft.api_key
---

Use craft tools.`)

	m, err := parseManifest(data, Internal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected manifest, got nil")
	}
	if len(m.SecretKeys) != 1 {
		t.Fatalf("SecretKeys len = %d, want 1", len(m.SecretKeys))
	}
	if m.SecretKeys[0] != "skills.craft.api_key" {
		t.Errorf("SecretKeys[0] = %q", m.SecretKeys[0])
	}
}

func TestParseManifest_NoConfigKeys(t *testing.T) {
	data := []byte(`---
name: simple
---

Content.`)

	m, err := parseManifest(data, Internal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected manifest, got nil")
	}
	if len(m.ConfigKeys) != 0 {
		t.Errorf("ConfigKeys len = %d, want 0", len(m.ConfigKeys))
	}
}
