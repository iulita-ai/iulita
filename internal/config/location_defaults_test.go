package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLocationTimezoneDefaultSurvivesConfigLoading(t *testing.T) {
	paths := testPaths(t)
	cfg, _, _, err := Load(filepath.Join(paths.ConfigDir, "absent.toml"), paths)
	if err != nil || cfg.App.DefaultTimezone != "Asia/Irkutsk" {
		t.Fatalf("compiled timezone default lost during load: %v, %v", cfg, err)
	}
	generated, err := GenerateDefaultConfig(paths)
	if err != nil || !strings.Contains(generated, `# default_timezone = "Asia/Irkutsk"`) {
		t.Fatalf("generated config differs from runtime default: %v", err)
	}
}
