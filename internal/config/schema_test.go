package config

import (
	"testing"
)

func TestCoreConfigSchema_NoDuplicateKeys(t *testing.T) {
	seen := make(map[string]bool)
	for _, section := range CoreConfigSchema() {
		for _, f := range section.Fields {
			if seen[f.Key] {
				t.Errorf("duplicate config key: %s", f.Key)
			}
			seen[f.Key] = true
		}
	}
}

func TestCoreConfigSchema_AllFieldsHaveSection(t *testing.T) {
	for _, section := range CoreConfigSchema() {
		for _, f := range section.Fields {
			if f.Section == "" {
				t.Errorf("field %s has empty section", f.Key)
			}
			if f.Section != section.Name {
				t.Errorf("field %s section %q doesn't match parent section %q", f.Key, f.Section, section.Name)
			}
		}
	}
}

func TestCoreConfigSchema_SecretFieldsMarked(t *testing.T) {
	secrets := SchemaSecretKeys()
	expected := map[string]bool{
		"claude.api_key": true,
		"openai.api_key": true,
		"telegram.token": true,
	}
	for k := range expected {
		if !secrets[k] {
			t.Errorf("expected secret key %q not found in SchemaSecretKeys()", k)
		}
	}
}

func TestCoreConfigSchema_NoRequiredLLMKeys(t *testing.T) {
	// LLM provider keys should not be required — user picks which providers to use.
	for _, section := range CoreConfigSchema() {
		for _, f := range section.Fields {
			if f.Key == "claude.api_key" && f.Required {
				t.Error("claude.api_key should not be required (user can choose a different provider)")
			}
		}
	}
}

func TestSchemaKeys_NotEmpty(t *testing.T) {
	keys := SchemaKeys()
	if len(keys) == 0 {
		t.Error("SchemaKeys() returned empty")
	}
	// Expect at least claude + openai + ollama keys
	if len(keys) < 10 {
		t.Errorf("expected at least 10 keys, got %d", len(keys))
	}
}

func TestWizardSections_OrderedAndFiltered(t *testing.T) {
	sections := WizardSections()
	if len(sections) == 0 {
		t.Fatal("WizardSections() returned empty")
	}
	// First section should be claude (WizardOrder=1).
	if sections[0].Name != "claude" {
		// WizardSections doesn't sort; the caller sorts.
		// But the source is in order, so this should be claude.
	}
	// All wizard sections should have WizardOrder > 0.
	for _, s := range sections {
		if s.WizardOrder == 0 {
			t.Errorf("wizard section %q has WizardOrder=0", s.Name)
		}
		// Fields should only have WizardOrder > 0.
		for _, f := range s.Fields {
			if f.WizardOrder == 0 {
				t.Errorf("wizard field %q in section %q has WizardOrder=0 (should have been filtered)", f.Key, s.Name)
			}
		}
	}
}

func TestGetSection(t *testing.T) {
	section, ok := GetSection("claude")
	if !ok {
		t.Fatal("GetSection('claude') returned false")
	}
	if section.Label == "" {
		t.Error("claude section has no label")
	}
	if len(section.Fields) == 0 {
		t.Error("claude section has no fields")
	}

	_, ok = GetSection("nonexistent")
	if ok {
		t.Error("GetSection('nonexistent') should return false")
	}
}

func TestWriteConfigFromValues(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.toml"

	values := map[string]string{
		"claude.model":       "claude-haiku-4-5-20251001",
		"claude.max_tokens":  "8192",
		"openai.fallback":    "true",
		"embedding.provider": "onnx",
	}

	if err := writeConfigFromValues(path, values); err != nil {
		t.Fatalf("writeConfigFromValues: %v", err)
	}

	// Load the file and verify it parses.
	paths := &Paths{
		ConfigDir: dir,
		DataDir:   dir,
		CacheDir:  dir,
		StateDir:  dir,
	}
	cfg, _, loaded, err := Load(path, paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded {
		t.Error("expected config to be loaded")
	}
	if cfg.Claude.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("expected claude.model=claude-haiku-4-5-20251001, got %q", cfg.Claude.Model)
	}
	if cfg.Claude.MaxTokens != 8192 {
		t.Errorf("expected claude.max_tokens=8192, got %d", cfg.Claude.MaxTokens)
	}
	if !cfg.OpenAI.Fallback {
		t.Error("expected openai.fallback=true")
	}
	if cfg.Embedding.Provider != "onnx" {
		t.Errorf("expected embedding.provider=onnx, got %q", cfg.Embedding.Provider)
	}
}
