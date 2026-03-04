package config

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzLoad(f *testing.F) {
	// Seed corpus with valid TOML.
	f.Add([]byte(`
[telegram]
token = "test-token"
[claude]
api_key = "sk-test"
`))
	f.Add([]byte(`
[app]
system_prompt = "hello"
default_timezone = "UTC"
[telegram]
token = "t"
[claude]
api_key = "k"
`))
	f.Add([]byte(`
[telegram]
token = "t"
allowed_ids = [123, 456]
[claude]
api_key = "k"
model = "claude-sonnet-4-5-20250929"
max_tokens = 4096
`))
	f.Add([]byte(``))
	f.Add([]byte(`invalid toml {{{{`))

	f.Fuzz(func(t *testing.T, data []byte) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "config.toml")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Skip()
		}
		paths := &Paths{
			ConfigDir: tmpDir,
			DataDir:   tmpDir,
			CacheDir:  tmpDir,
			StateDir:  tmpDir,
		}
		// Should not panic regardless of input.
		cfg, _, _, err := Load(path, paths)
		if err != nil {
			return
		}
		// If it loaded, Validate should not panic either.
		_ = cfg.Validate(ValidateConsole)
	})
}
