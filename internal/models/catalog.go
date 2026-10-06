// Package models defines the bounded, nonsecret model settings contract shared
// by configuration, routing and the dashboard. It performs no network requests.
package models

// CatalogVersion identifies the vendor documentation snapshot.
const CatalogVersion = "2026-10-05"

// Definition describes documented model capabilities and limits.
type Definition struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Name             string `json:"name"`
	ContextTokens    int    `json:"context_tokens"`
	MaxOutputTokens  int    `json:"max_output_tokens"`
	Images           bool   `json:"images"`
	Tools            bool   `json:"tools"`
	Streaming        bool   `json:"streaming"`
	ThinkingRequired bool   `json:"thinking_required"`
	SourceURL        string `json:"source_url"`
	CatalogVersion   string `json:"catalog_version"`
}

// Catalog is documentation evidence, not account availability or a live probe.
func Catalog() []Definition {
	return []Definition{
		{"deepseek", "deepseek-flash", "DeepSeek V4.1 Flash", 1_000_000, 384_000, true, true, true, false, "https://api-docs.deepseek.com/quick_start/pricing/", CatalogVersion},
		{"deepseek", "deepseek-v4-pro", "DeepSeek V4 Pro", 1_000_000, 384_000, false, true, true, false, "https://api-docs.deepseek.com/quick_start/pricing/", CatalogVersion},
		{"zai", "glm-5.3-flash", "GLM 5.3 Flash", 1_000_000, 128_000, true, true, true, true, "https://docs.z.ai/guides/vlm/glm-5.3-flash", CatalogVersion},
		{"zai", "glm-5.3", "GLM 5.3", 1_000_000, 128_000, false, true, true, true, "https://docs.z.ai/guides/llm/glm-5.3", CatalogVersion},
	}
}

// Lookup finds a documented model by provider and identifier.
func Lookup(provider, model string) (Definition, bool) {
	for _, d := range Catalog() {
		if d.Provider == provider && d.Model == model {
			return d, true
		}
	}
	return Definition{}, false
}

// CandidateProfiles are opt-in presets. Saving them does not assign any role or
// establish production eligibility. DeepSeek explicitly starts non-thinking.
func CandidateProfiles() []Profile {
	return []Profile{
		{ID: "ds-flash", Name: "DeepSeek Flash", Connection: "deepseek", Model: "deepseek-flash", MaxOutputTokens: 8192, Thinking: "disabled"},
		{ID: "ds-pro", Name: "DeepSeek Pro", Connection: "deepseek", Model: "deepseek-v4-pro", MaxOutputTokens: 16384, Thinking: "disabled"},
		{ID: "glm-flash", Name: "GLM Flash", Connection: "zai", Model: "glm-5.3-flash", MaxOutputTokens: 8192, Thinking: "enabled", ReasoningEffort: "low", ClearThinking: true},
		{ID: "glm-main", Name: "GLM 5.3", Connection: "zai", Model: "glm-5.3", MaxOutputTokens: 16384, Thinking: "enabled", ReasoningEffort: "high", ClearThinking: true},
	}
}

// CompatibilityVersion changes only when this adapter contract changes. An
// unrelated catalog refresh must not invalidate working model identities.
func CompatibilityVersion(provider, model string) string {
	switch provider {
	case "deepseek":
		return "deepseek-explicit-thinking-v1"
	case "zai":
		return "glm-clear-thinking-v1"
	default:
		return provider + "-legacy-protocol-v1"
	}
}
