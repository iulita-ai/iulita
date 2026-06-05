package config

import (
	"strings"
	"testing"
)

func testPaths(t *testing.T) *Paths {
	t.Helper()
	dir := t.TempDir()
	return &Paths{ConfigDir: dir, DataDir: dir, CacheDir: dir, StateDir: dir}
}

func TestHasAnyLLMProvider_DeepSeekOnly(t *testing.T) {
	c := &Config{}
	c.DeepSeek.APIKey = "sk-test"
	c.DeepSeek.Model = "deepseek-v4-flash"
	if !c.HasAnyLLMProvider() {
		t.Error("DeepSeek-only config should satisfy HasAnyLLMProvider")
	}

	// API key without model is not enough (mirrors OpenAI semantics).
	c2 := &Config{}
	c2.DeepSeek.APIKey = "sk-test"
	if c2.HasAnyLLMProvider() {
		t.Error("DeepSeek api_key without model should not satisfy HasAnyLLMProvider")
	}
}

func TestValidate_DeepSeekOnlyConsole(t *testing.T) {
	c := DefaultConfig(testPaths(t))
	c.DeepSeek.APIKey = "sk-test"
	c.DeepSeek.Model = "deepseek-v4-flash"
	if err := c.Validate(ValidateConsole); err != nil {
		t.Errorf("DeepSeek-only console config should validate, got: %v", err)
	}
}

func TestStructToMap_DeepSeekKeysMirrorOpenAI(t *testing.T) {
	m := structToMap(DefaultConfig(testPaths(t)))
	if _, ok := m["deepseek.max_tokens"]; !ok {
		t.Error("structToMap must include deepseek.max_tokens")
	}
	// model/base_url must NOT be in structToMap (would shadow keyring/env with
	// empty strings during koanf merge — model default comes from DefaultConfig).
	if _, ok := m["deepseek.model"]; ok {
		t.Error("structToMap must NOT include deepseek.model")
	}
	if _, ok := m["deepseek.base_url"]; ok {
		t.Error("structToMap must NOT include deepseek.base_url")
	}
}

func TestSchema_DeepSeekSection(t *testing.T) {
	var found bool
	for _, s := range CoreConfigSchema() {
		if s.Name == "deepseek" {
			found = true
			var hasModelSource bool
			for _, f := range s.Fields {
				if f.Key == "deepseek.model" && f.ModelSource == ModelSourceDeepSeek {
					hasModelSource = true
				}
			}
			if !hasModelSource {
				t.Error("deepseek.model must use ModelSourceDeepSeek for the dynamic dropdown")
			}
		}
	}
	if !found {
		t.Fatal("CoreConfigSchema must include a deepseek section")
	}
}

func TestSchemaSecretKeys_IncludesDeepSeekAPIKey(t *testing.T) {
	if !SchemaSecretKeys()["deepseek.api_key"] {
		t.Error("deepseek.api_key must be a secret key (encrypted at rest)")
	}
}

func TestDefaultModelPrices_DeepSeek(t *testing.T) {
	prices := defaultModelPrices()
	for _, model := range []string{"deepseek-v4-flash", "deepseek-chat", "deepseek-reasoner"} {
		p, ok := prices[model]
		if !ok {
			t.Errorf("missing price entry for %s", model)
			continue
		}
		if p.InputPerMillion <= 0 || p.OutputPerMillion <= 0 {
			t.Errorf("%s has non-positive price: %+v", model, p)
		}
	}
}

func TestGenerateDefaultConfig_IncludesDeepSeek(t *testing.T) {
	out, err := GenerateDefaultConfig(testPaths(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[deepseek]") {
		t.Error("generated config must include a [deepseek] section")
	}
	if !strings.Contains(out, "deepseek-v4-flash") {
		t.Error("generated [deepseek] section must reference the default model")
	}
}

func TestCoreKeys_IncludeDeepSeek(t *testing.T) {
	// Dashboard / DB config overrides are gated by coreKeys; without these
	// entries Store.Set rejects deepseek.* with "unknown config key".
	for _, k := range []string{
		"deepseek.api_key", "deepseek.model", "deepseek.max_tokens",
		"deepseek.base_url", "deepseek.fallback",
	} {
		if !coreKeys[k] {
			t.Errorf("coreKeys missing %q (DB/dashboard override would be rejected)", k)
		}
	}
}

func TestDefaultConfig_DeepSeekModel(t *testing.T) {
	if got := DefaultConfig(testPaths(t)).DeepSeek.Model; got != "deepseek-v4-flash" {
		t.Errorf("default DeepSeek model = %q, want deepseek-v4-flash", got)
	}
}
