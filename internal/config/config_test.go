package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestValidateSetupMode verifies that ValidateSetup mode always returns nil,
// regardless of the config state (no LLM, no channels configured).
func TestValidateSetupMode(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "empty config",
			cfg:  Config{},
		},
		{
			name: "no LLM provider",
			cfg: Config{
				Claude: ClaudeConfig{APIKey: ""},
				OpenAI: OpenAIConfig{APIKey: "", Model: ""},
				Ollama: OllamaConfig{URL: "", Model: ""},
			},
		},
		{
			name: "no channel configured",
			cfg: Config{
				Telegram: TelegramConfig{Token: ""},
				Server:   ServerConfig{Enabled: false},
			},
		},
		{
			name: "invalid timezone still passes in setup mode",
			cfg: Config{
				App: AppConfig{DefaultTimezone: "Not/AReal/Timezone"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate(ValidateSetup)
			if err != nil {
				t.Errorf("Validate(ValidateSetup) = %v, want nil", err)
			}
		})
	}
}

// TestValidateModesRequireLLM verifies that non-setup modes require at least one LLM.
func TestValidateModesRequireLLM(t *testing.T) {
	cfg := &Config{}

	if err := cfg.Validate(ValidateConsole); err == nil {
		t.Error("Validate(ValidateConsole) with no LLM should return an error, got nil")
	}

	if err := cfg.Validate(ValidateServer); err == nil {
		t.Error("Validate(ValidateServer) with no LLM should return an error, got nil")
	}
}

// TestValidateServerRequiresChannel verifies that server mode requires a channel
// (Telegram token or server.enabled) in addition to an LLM.
func TestValidateServerRequiresChannel(t *testing.T) {
	cfg := &Config{
		Claude: ClaudeConfig{APIKey: "sk-test"},
	}

	// Server mode with no channel should fail.
	if err := cfg.Validate(ValidateServer); err == nil {
		t.Error("Validate(ValidateServer) with no channel should return an error, got nil")
	}

	// Console mode with just an LLM should pass.
	if err := cfg.Validate(ValidateConsole); err != nil {
		t.Errorf("Validate(ValidateConsole) with Claude key = %v, want nil", err)
	}

	// Server mode with server.enabled=true should pass.
	cfg.Server.Enabled = true
	if err := cfg.Validate(ValidateServer); err != nil {
		t.Errorf("Validate(ValidateServer) with server.enabled = %v, want nil", err)
	}
}

// TestValidateModeConstants verifies the ValidateMode constants are distinct.
func TestValidateModeConstants(t *testing.T) {
	modes := []ValidateMode{ValidateConsole, ValidateServer, ValidateSetup}
	seen := make(map[ValidateMode]bool)
	for _, m := range modes {
		if seen[m] {
			t.Errorf("ValidateMode value %d is duplicated", m)
		}
		seen[m] = true
	}
}

// TestWriteSentinelFile verifies the sentinel file is created with the expected content.
func TestWriteSentinelFile(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "db_managed")

	if err := WriteSentinelFile(sentinel); err != nil {
		t.Fatalf("WriteSentinelFile: %v", err)
	}

	// File must exist.
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel file does not exist after WriteSentinelFile: %v", err)
	}

	// File must have correct content.
	content, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("reading sentinel file: %v", err)
	}
	if string(content) != "db-managed\n" {
		t.Errorf("sentinel content = %q, want %q", string(content), "db-managed\n")
	}
}

// TestWriteSentinelFile_Idempotent verifies that calling WriteSentinelFile twice
// overwrites the file without error.
func TestWriteSentinelFile_Idempotent(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "db_managed")

	if err := WriteSentinelFile(sentinel); err != nil {
		t.Fatalf("first WriteSentinelFile: %v", err)
	}
	if err := WriteSentinelFile(sentinel); err != nil {
		t.Fatalf("second WriteSentinelFile: %v", err)
	}

	content, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("reading sentinel file: %v", err)
	}
	if string(content) != "db-managed\n" {
		t.Errorf("sentinel content after double write = %q, want %q", string(content), "db-managed\n")
	}
}

// TestLoadSkipsTOMLWhenSentinel verifies that when a db_managed sentinel file exists
// in the config dir, Load() skips TOML file loading (configLoaded = false).
func TestLoadSkipsTOMLWhenSentinel(t *testing.T) {
	dir := t.TempDir()

	// Write a real TOML config file that would normally be loaded.
	tomlPath := filepath.Join(dir, "config.toml")
	tomlContent := `[claude]
model = "claude-haiku-4-5-20251001"
max_tokens = 8192
`
	if err := os.WriteFile(tomlPath, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}

	paths := &Paths{
		ConfigDir: dir,
		DataDir:   dir,
		CacheDir:  dir,
		StateDir:  dir,
	}

	// Without sentinel: TOML should be loaded.
	cfg, _, configLoaded, err := Load(tomlPath, paths)
	if err != nil {
		t.Fatalf("Load (no sentinel): %v", err)
	}
	if !configLoaded {
		t.Error("expected configLoaded=true when no sentinel file exists")
	}
	// Verify the TOML values were applied.
	if cfg.Claude.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("expected claude.model=claude-haiku-4-5-20251001 from TOML, got %q", cfg.Claude.Model)
	}
	if cfg.Claude.MaxTokens != 8192 {
		t.Errorf("expected claude.max_tokens=8192 from TOML, got %d", cfg.Claude.MaxTokens)
	}

	// Create the sentinel file.
	sentinel := filepath.Join(dir, "db_managed")
	if err := WriteSentinelFile(sentinel); err != nil {
		t.Fatalf("WriteSentinelFile: %v", err)
	}

	// With sentinel: TOML should be skipped.
	cfg2, _, configLoaded2, err := Load(tomlPath, paths)
	if err != nil {
		t.Fatalf("Load (with sentinel): %v", err)
	}
	if configLoaded2 {
		t.Error("expected configLoaded=false when db_managed sentinel file exists")
	}
	// The TOML override should NOT have been applied — values should be defaults.
	if cfg2.Claude.Model == "claude-haiku-4-5-20251001" {
		t.Error("claude.model should not be overridden from TOML when sentinel exists")
	}
}

// TestLoadSkipsTOMLWhenSentinel_MissingTOML verifies that when both sentinel and TOML
// are absent, Load() still succeeds (returns defaults, configLoaded=false).
func TestLoadSkipsTOMLWhenSentinel_MissingTOML(t *testing.T) {
	dir := t.TempDir()

	// Write only the sentinel, no TOML.
	sentinel := filepath.Join(dir, "db_managed")
	if err := WriteSentinelFile(sentinel); err != nil {
		t.Fatalf("WriteSentinelFile: %v", err)
	}

	paths := &Paths{
		ConfigDir: dir,
		DataDir:   dir,
		CacheDir:  dir,
		StateDir:  dir,
	}

	tomlPath := filepath.Join(dir, "config.toml") // does not exist
	cfg, _, configLoaded, err := Load(tomlPath, paths)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if configLoaded {
		t.Error("expected configLoaded=false when sentinel exists and TOML is absent")
	}
	if cfg == nil {
		t.Error("expected non-nil cfg")
	}
}

// TestHasAnyLLMProvider verifies the LLM provider detection helper.
func TestHasAnyLLMProvider(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"empty", Config{}, false},
		{"claude key only", Config{Claude: ClaudeConfig{APIKey: "sk-test"}}, true},
		{"openai key + model", Config{OpenAI: OpenAIConfig{APIKey: "sk-test", Model: "gpt-4o"}}, true},
		{"openai key no model", Config{OpenAI: OpenAIConfig{APIKey: "sk-test"}}, false},
		{"ollama url + model", Config{Ollama: OllamaConfig{URL: "http://localhost:11434", Model: "llama3"}}, true},
		{"ollama url no model", Config{Ollama: OllamaConfig{URL: "http://localhost:11434"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.HasAnyLLMProvider()
			if got != tt.want {
				t.Errorf("HasAnyLLMProvider() = %v, want %v", got, tt.want)
			}
		})
	}
}
