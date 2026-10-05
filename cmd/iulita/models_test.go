package main

import (
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/llm"
	"testing"
)

func TestLegacyMigrationPreservesDistinctCustomTargets(t *testing.T) {
	cfg := &config.Config{}
	cfg.Claude.APIKey = "fixture"
	cfg.Claude.Model = "claude-sonnet-4-6"
	cfg.DeepSeek.APIKey = "fixture"
	cfg.DeepSeek.Model = "deepseek-v4-flash"
	cfg.Routing.DefaultProvider = "deepseek"
	cfg.Routing.LightEnabled = true
	cfg.Routing.LightProvider = "claude-haiku"
	cfg.Routing.Routes = []config.RouteConfig{{Hint: "custom_task", Provider: "claude"}, {Hint: "complex", Provider: "deepseek"}}
	s := legacyModelSettings(cfg)
	if s == nil || len(s.Validate()) != 0 {
		t.Fatalf("invalid migration draft: %+v", s)
	}
	if s.Policy.Everyday != "legacy-deepseek" || s.Policy.Background != "legacy-claude-haiku" || s.Policy.LegacyProfileHints["custom_task"] != "legacy-claude" || s.Policy.LegacyHints[llm.RouteHintCheap] != "background" || s.Policy.Classifier.Enabled {
		t.Fatalf("changed routing: %+v", s.Policy)
	}
}

func TestEnvironmentSourceUsesEffectiveKeyAndLegacySourceIsRefreshable(t *testing.T) {
	for _, env := range []string{"IULITA_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY", "IULITA_ZAI_API_KEY", "ZAI_API_KEY", "IULITA_CLAUDE_API_KEY", "ANTHROPIC_API_KEY", "IULITA_OPENAI_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(env, "")
	}
	cfg := &config.Config{}
	cfg.DeepSeek.APIKey = "file-key" // Synthetic test credential. gitleaks:allow
	if source := legacyModelConnections(cfg)[0].Source; source != "legacy_effective" {
		t.Fatalf("unrefreshable startup source: %s", source)
	}
	t.Setenv("DEEPSEEK_API_KEY", "environment-key")
	applyModelEnvironmentKeys(cfg)
	c := legacyModelConnections(cfg)[0]
	if c.Source != "environment" || c.APIKey != "environment-key" {
		t.Fatalf("ownership/key mismatch: %s", c.Source)
	}
	t.Setenv("IULITA_DEEPSEEK_API_KEY", "iulita-key")
	applyModelEnvironmentKeys(cfg)
	if cfg.DeepSeek.APIKey != "iulita-key" {
		t.Fatal("documented IULITA precedence changed")
	}
}
func TestUnsupportedLegacyModelDoesNotSilentlyDropCustomRoutes(t *testing.T) {
	cfg := &config.Config{}
	cfg.Claude.APIKey = "fixture"
	cfg.Claude.Model = "claude-sonnet-4-6"
	cfg.DeepSeek.APIKey = "fixture"
	cfg.DeepSeek.Model = "unknown-legacy-model"
	cfg.Routing.Routes = []config.RouteConfig{{Hint: "custom", Provider: "deepseek"}}
	if legacyModelSettings(cfg) != nil {
		t.Fatal("unsupported custom target silently removed from migration draft")
	}
}

func TestLegacyMigrationDoesNotGrantChangedThinkingOrOutputLimit(t *testing.T) {
	cfg := &config.Config{}
	cfg.Claude.MaxTokens = 4096
	cfg.OpenAI.MaxTokens = 4096
	if got := changedLegacyModelProviders(cfg, "off"); len(got) != 0 {
		t.Fatal(got)
	}
	for _, mode := range []string{"low", "medium", "high"} {
		got := changedLegacyModelProviders(cfg, mode)
		if len(got) != 1 || got[0] != "claude" {
			t.Fatal(got)
		}
	}
	cfg.Claude.MaxTokens = 100000
	cfg.OpenAI.MaxTokens = 100000
	got := changedLegacyModelProviders(cfg, "off")
	if len(got) != 2 || got[0] != "claude" || got[1] != "openai" {
		t.Fatal(got)
	}
}
