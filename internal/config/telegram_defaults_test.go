package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
)

func TestTelegramDayUpgradeDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, text       string
		managed          bool
		widgets, actions bool
	}{
		{"old_toml", "[telegram]\ntoken = \"fixture\"\n", false, true, true},
		{"explicit_opt_out", "[telegram]\nday_widgets = false\nday_actions = false\n", false, false, false},
		{"read_only", "[telegram]\nday_actions = false\n", false, true, false},
		{"cards_off", "[telegram]\nday_widgets = false\n", false, false, true},
		{"old_db_managed", "[telegram]\n", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := testPaths(t)
			if e := os.MkdirAll(paths.ConfigDir, 0o700); e != nil {
				t.Fatal(e)
			}
			p := filepath.Join(paths.ConfigDir, "config.toml")
			if e := os.WriteFile(p, []byte(tc.text), 0o600); e != nil {
				t.Fatal(e)
			}
			if tc.managed {
				if e := os.WriteFile(filepath.Join(paths.ConfigDir, "db_managed"), nil, 0o600); e != nil {
					t.Fatal(e)
				}
			}
			cfg, k, _, e := Load(p, paths)
			if e != nil {
				t.Fatal(e)
			}
			if cfg.Telegram.DayWidgets != tc.widgets || cfg.Telegram.DayActions != tc.actions {
				t.Fatalf("%+v", cfg.Telegram)
			}
			if k.Bool("telegram.day_widgets") != tc.widgets || k.Bool("telegram.day_actions") != tc.actions {
				t.Fatal("base values differ")
			}
		})
	}
	generated, e := GenerateDefaultConfig(testPaths(t))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(generated, "day_widgets = true") || !strings.Contains(generated, "day_actions = true") {
		t.Fatal("generated config disables features")
	}
}

func TestTelegramDayEnvironmentOptOut(t *testing.T) {
	t.Setenv("IULITA_TELEGRAM_DAY_WIDGETS", "false")
	t.Setenv("IULITA_TELEGRAM_DAY_ACTIONS", "false")
	cfg, _, _, e := Load("", testPaths(t))
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Telegram.DayWidgets || cfg.Telegram.DayActions {
		t.Fatal("env opt-out ignored")
	}
}

func TestTelegramDayPersistedOptOutAfterUpgrade(t *testing.T) {
	paths := testPaths(t)
	if e := os.MkdirAll(paths.ConfigDir, 0o700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(paths.ConfigDir, "db_managed"), nil, 0o600); e != nil {
		t.Fatal(e)
	}
	cfg, k, _, e := Load("", paths)
	if e != nil {
		t.Fatal(e)
	}
	repo := &overrideRepo{rows: []domain.ConfigOverride{{Key: "telegram.day_widgets", Value: "false"}, {Key: "telegram.day_actions", Value: "false"}}}
	store := NewStore(cfg, k, repo, nil, zap.NewNop())
	if e = store.LoadOverrides(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = ApplyTelegramDayOverrides(&cfg.Telegram, store.GetEffective); e != nil {
		t.Fatal(e)
	}
	if cfg.Telegram.DayWidgets || cfg.Telegram.DayActions {
		t.Fatal("persisted opt-out ignored at composition")
	}
	// Import/settings persistence for restart-only keys remains available.
	for _, key := range []string{"telegram.day_widgets", "telegram.day_actions"} {
		if e = store.SetForImport(context.Background(), key, "false", "owner", false); e != nil {
			t.Fatal(key, e)
		}
	}
}
func TestTelegramDayMissingAndInvalidOverrides(t *testing.T) {
	c := TelegramConfig{DayWidgets: true, DayActions: true}
	if e := ApplyTelegramDayOverrides(&c, func(string) (string, bool) { return "", false }); e != nil || !c.DayWidgets || !c.DayActions {
		t.Fatal(c, e)
	}
	if e := ApplyTelegramDayOverrides(&c, func(key string) (string, bool) {
		if key == "telegram.day_widgets" {
			return "false", true
		}
		return "invalid", true
	}); e == nil {
		t.Fatal("invalid override silently accepted")
	}
	if !c.DayWidgets || !c.DayActions {
		t.Fatal("partially applied invalid overrides")
	}
}
