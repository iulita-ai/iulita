package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestMobileDefaultsAndLayeredConfiguration(t *testing.T) {
	for _, key := range []string{"CHAT", "DEVICE", "UI", "VISION"} {
		t.Setenv("IULITA_MOBILE_"+key+"_ENABLED", "false")
	}
	paths := testPaths(t)
	if got := DefaultConfig(paths).Mobile; got != (MobileConfig{}) {
		t.Fatalf("mobile defaults active: %+v", got)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[mobile]\nchat_enabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Environment layer disables a TOML request; it does not expose an endpoint.
	cfg, _, _, err := Load(path, paths)
	if err != nil || cfg.Mobile.ChatEnabled {
		t.Fatalf("env precedence: %v", err)
	}
	t.Setenv("IULITA_MOBILE_CHAT_ENABLED", "true")
	cfg, _, _, err = Load(path, paths)
	if err != nil || !cfg.Mobile.ChatEnabled {
		t.Fatalf("requested env flag not represented: %v", err)
	}
}

func TestMobileFlagsAreRestartOnly(t *testing.T) {
	store := NewStore(nil, nil, &overrideRepo{}, nil, zap.NewNop())
	section, ok := GetSection("mobile")
	if !ok || len(section.Fields) != 4 {
		t.Fatal("mobile schema missing")
	}
	for _, field := range section.Fields {
		if !field.RestartRequired || !IsRestartOnlyKey(field.Key) || field.Default != "false" {
			t.Fatalf("unsafe mobile config field: %+v", field)
		}
		if err := store.Set(context.Background(), field.Key, "true", "test", false); err == nil {
			t.Fatal("runtime activation accepted")
		}
		if err := store.SetForImport(context.Background(), field.Key, "false", "test", false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSetupDoesNotSkipMobileDependencies(t *testing.T) {
	for _, flags := range []MobileConfig{{UIEnabled: true}, {VisionEnabled: true}, {DeviceEnabled: true, VisionEnabled: true}} {
		cfg := Config{Mobile: flags}
		if err := cfg.Validate(ValidateSetup); err == nil {
			t.Fatalf("invalid dependency accepted in setup: %+v", flags)
		}
	}
	text, err := GenerateDefaultConfig(testPaths(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "defaults.toml")
	if stepErr1 := os.WriteFile(path, []byte(text), 0o600); stepErr1 != nil {
		t.Fatal(stepErr1)
	}
	for _, key := range []string{"CHAT", "DEVICE", "UI", "VISION"} {
		t.Setenv("IULITA_MOBILE_"+key+"_ENABLED", "false")
	}
	cfg, _, _, err := Load(path, testPaths(t))
	if err != nil || cfg.Mobile != (MobileConfig{}) {
		t.Fatalf("generated defaults not disabled: %v", err)
	}
}
