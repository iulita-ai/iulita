package config

// FieldType defines the type of a config field for UI rendering.
type FieldType string

const (
	FieldString FieldType = "string"
	FieldInt    FieldType = "int"
	FieldBool   FieldType = "bool"
	FieldSelect FieldType = "select"
	FieldSecret FieldType = "secret"
	FieldURL    FieldType = "url"
)

// ModelSource indicates where to get the options list for a select field.
type ModelSource string

const (
	ModelSourceStatic ModelSource = ""       // use Options as-is
	ModelSourceOpenAI ModelSource = "openai" // fetch from OpenAI /v1/models
	ModelSourceOllama ModelSource = "ollama" // fetch from Ollama /api/tags
)

// ConfigField describes a single configurable key.
type ConfigField struct {
	Key         string      `json:"key"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Type        FieldType   `json:"type"`
	Default     string      `json:"default,omitempty"`
	Options     []string    `json:"options,omitempty"`
	Secret      bool        `json:"secret"`
	Required    bool        `json:"required"`
	Section     string      `json:"section"`
	WizardOrder int         `json:"-"`                      // 0 = not shown in wizard
	ModelSource ModelSource `json:"model_source,omitempty"` // dynamic model fetching
}

// ConfigSection describes a group of related config fields.
type ConfigSection struct {
	Name        string        `json:"name"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	Fields      []ConfigField `json:"fields"`
	WizardOrder int           `json:"-"` // 0 = not shown in wizard; lower = earlier
	Optional    bool          `json:"-"` // if true, wizard asks "Configure X? [y/N]" gate
}

// CoreConfigSchema returns the typed schema for all core (non-skill) config sections.
// This is the single source of truth for the init wizard, dashboard UI, and chat config.
func CoreConfigSchema() []ConfigSection {
	return []ConfigSection{
		{
			Name:        "claude",
			Label:       "Claude (Anthropic)",
			Description: "Primary LLM provider",
			WizardOrder: 1,
			Fields: []ConfigField{
				{Key: "claude.api_key", Label: "API Key", Description: "Anthropic API key", Type: FieldSecret, Secret: true, Section: "claude", WizardOrder: 1},
				{Key: "claude.model", Label: "Model", Description: "Claude model ID", Type: FieldSelect, Default: "claude-sonnet-4-6", Options: []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5-20251001", "claude-sonnet-4-5-20250929", "claude-opus-4-5-20251101"}, Section: "claude", WizardOrder: 2},
				{Key: "claude.max_tokens", Label: "Max Tokens", Description: "Maximum output tokens per response", Type: FieldInt, Default: "4096", Section: "claude", WizardOrder: 3},
				{Key: "claude.base_url", Label: "Base URL", Description: "Custom API base URL (for proxies/gateways)", Type: FieldURL, Section: "claude", WizardOrder: 4},
				{Key: "claude.thinking", Label: "Thinking", Description: "Extended thinking budget", Type: FieldSelect, Default: "off", Options: []string{"off", "low", "medium", "high"}, Section: "claude"},
				{Key: "claude.streaming", Label: "Streaming", Description: "Stream responses (Telegram live edits)", Type: FieldBool, Default: "false", Section: "claude"},
				{Key: "claude.request_timeout", Label: "Request Timeout", Description: "Per-request timeout (Go duration)", Type: FieldString, Default: "120s", Section: "claude"},
				{Key: "claude.context_window", Label: "Context Window", Description: "Model context window size", Type: FieldInt, Default: "200000", Section: "claude"},
			},
		},
		{
			Name:        "openai",
			Label:       "OpenAI-Compatible",
			Description: "OpenAI or compatible provider (optional fallback)",
			WizardOrder: 2,
			Optional:    true,
			Fields: []ConfigField{
				{Key: "openai.api_key", Label: "API Key", Description: "OpenAI API key", Type: FieldSecret, Secret: true, Section: "openai", WizardOrder: 1},
				{Key: "openai.model", Label: "Model", Description: "Model name (e.g. gpt-4o, gpt-4o-mini)", Type: FieldString, Section: "openai", WizardOrder: 2, ModelSource: ModelSourceOpenAI},
				{Key: "openai.max_tokens", Label: "Max Tokens", Description: "Maximum output tokens", Type: FieldInt, Default: "4096", Section: "openai"},
				{Key: "openai.base_url", Label: "Base URL", Description: "Custom base URL (for compatible APIs like Azure, Together, etc.)", Type: FieldURL, Section: "openai", WizardOrder: 3},
				{Key: "openai.fallback", Label: "Use as Fallback", Description: "Use as fallback when Claude fails", Type: FieldBool, Default: "false", Section: "openai", WizardOrder: 4},
			},
		},
		{
			Name:        "ollama",
			Label:       "Ollama (Local)",
			Description: "Local LLM via Ollama",
			WizardOrder: 3,
			Optional:    true,
			Fields: []ConfigField{
				{Key: "ollama.url", Label: "URL", Description: "Ollama server URL", Type: FieldURL, Default: "http://localhost:11434", Section: "ollama", WizardOrder: 1},
				{Key: "ollama.model", Label: "Model", Description: "Model name (e.g. llama3, mistral, gemma2)", Type: FieldString, Section: "ollama", WizardOrder: 2, ModelSource: ModelSourceOllama},
			},
		},
		{
			Name:        "proxy",
			Label:       "HTTP Proxy",
			Description: "Global HTTP proxy for all providers",
			WizardOrder: 4,
			Optional:    true,
			Fields: []ConfigField{
				{Key: "proxy.url", Label: "Proxy URL", Description: "HTTP/HTTPS/SOCKS5 proxy URL", Type: FieldURL, Section: "proxy", WizardOrder: 1},
			},
		},
		{
			Name:        "embedding",
			Label:       "Embedding",
			Description: "Vector embeddings for hybrid search (ONNX, ~30MB download)",
			WizardOrder: 5,
			Fields: []ConfigField{
				{Key: "embedding.provider", Label: "Provider", Description: "Embedding provider (onnx = local, empty = disabled)", Type: FieldSelect, Default: "onnx", Options: []string{"onnx", ""}, Section: "embedding", WizardOrder: 1},
				{Key: "embedding.model_dir", Label: "Model Directory", Description: "Directory for ONNX model files", Type: FieldString, Section: "embedding"},
			},
		},
		{
			Name:        "telegram",
			Label:       "Telegram",
			Description: "Telegram bot channel",
			WizardOrder: 6,
			Optional:    true,
			Fields: []ConfigField{
				{Key: "telegram.token", Label: "Bot Token", Description: "Telegram bot token from @BotFather", Type: FieldSecret, Secret: true, Section: "telegram", WizardOrder: 1},
			},
		},
		{
			Name:        "routing",
			Label:       "Model Routing",
			Description: "Route queries to different providers",
			Fields: []ConfigField{
				{Key: "routing.enabled", Label: "Enabled", Description: "Enable hint-based model routing", Type: FieldBool, Default: "false", Section: "routing"},
				{Key: "routing.default_provider", Label: "Default Provider", Description: "Default provider for unclassified queries", Type: FieldSelect, Default: "claude", Options: []string{"claude", "openai", "ollama"}, Section: "routing"},
				{Key: "routing.classification_enabled", Label: "Auto-Classification", Description: "Automatically classify queries (requires Ollama)", Type: FieldBool, Default: "false", Section: "routing"},
				{Key: "routing.classification_provider", Label: "Classification Provider", Description: "Provider for query classification", Type: FieldSelect, Options: []string{"claude", "ollama"}, Section: "routing"},
				{Key: "routing.max_actions_per_hour", Label: "Max Actions/Hour", Description: "Global action rate limit (0 = unlimited)", Type: FieldInt, Default: "0", Section: "routing"},
			},
		},
		{
			Name:        "cache",
			Label:       "Cache",
			Description: "Response and embedding caching",
			Fields: []ConfigField{
				{Key: "cache.response_enabled", Label: "Response Cache", Description: "Cache LLM responses", Type: FieldBool, Default: "false", Section: "cache"},
				{Key: "cache.response_ttl", Label: "Response TTL", Description: "Cache TTL (Go duration)", Type: FieldString, Default: "60m", Section: "cache"},
				{Key: "cache.response_max_items", Label: "Max Responses", Description: "Maximum cached responses", Type: FieldInt, Default: "1000", Section: "cache"},
				{Key: "cache.embedding_enabled", Label: "Embedding Cache", Description: "Cache embedding computations", Type: FieldBool, Default: "true", Section: "cache"},
				{Key: "cache.embedding_max_items", Label: "Max Embeddings", Description: "Maximum cached embeddings", Type: FieldInt, Default: "10000", Section: "cache"},
			},
		},
		{
			Name:        "app",
			Label:       "Application",
			Description: "General application settings",
			Fields: []ConfigField{
				{Key: "app.system_prompt", Label: "System Prompt", Description: "Base system prompt for the assistant", Type: FieldString, Default: "You are Iulita, a helpful personal AI assistant. Be concise and helpful.", Section: "app"},
				{Key: "app.auto_link_summary", Label: "Auto Link Summary", Description: "Automatically fetch and summarize URLs", Type: FieldBool, Default: "false", Section: "app"},
				{Key: "app.max_links", Label: "Max Links", Description: "Max URLs to fetch per message", Type: FieldInt, Default: "3", Section: "app"},
			},
		},
		{
			Name:        "log",
			Label:       "Logging",
			Description: "Logging configuration",
			Fields: []ConfigField{
				{Key: "log.level", Label: "Level", Description: "Log level", Type: FieldSelect, Default: "info", Options: []string{"debug", "info", "warn", "error"}, Section: "log"},
				{Key: "log.encoding", Label: "Encoding", Description: "Log output format", Type: FieldSelect, Default: "console", Options: []string{"console", "json"}, Section: "log"},
			},
		},
		{
			Name:        "server",
			Label:       "Server",
			Description: "Dashboard web server",
			Fields: []ConfigField{
				{Key: "server.enabled", Label: "Enabled", Description: "Enable web dashboard server", Type: FieldBool, Default: "false", Section: "server"},
				{Key: "server.address", Label: "Address", Description: "Listen address", Type: FieldString, Default: ":8080", Section: "server"},
			},
		},
		{
			Name:        "cost",
			Label:       "Cost Tracking",
			Description: "LLM usage cost tracking",
			Fields: []ConfigField{
				{Key: "cost.enabled", Label: "Enabled", Description: "Enable cost tracking", Type: FieldBool, Default: "true", Section: "cost"},
				{Key: "cost.daily_limit_usd", Label: "Daily Limit (USD)", Description: "Max daily spend (0 = unlimited)", Type: FieldString, Default: "0", Section: "cost"},
				{Key: "cost.alert_threshold", Label: "Alert Threshold", Description: "Warning threshold (0-1 fraction of daily limit)", Type: FieldString, Default: "0.8", Section: "cost"},
			},
		},
	}
}

// SchemaKeys returns all config keys defined in the schema.
func SchemaKeys() []string {
	var keys []string
	for _, s := range CoreConfigSchema() {
		for _, f := range s.Fields {
			keys = append(keys, f.Key)
		}
	}
	return keys
}

// SchemaSecretKeys returns config keys that are marked as secret.
func SchemaSecretKeys() map[string]bool {
	m := make(map[string]bool)
	for _, s := range CoreConfigSchema() {
		for _, f := range s.Fields {
			if f.Secret {
				m[f.Key] = true
			}
		}
	}
	return m
}

// WizardSections returns only sections that should appear in the init wizard,
// sorted by WizardOrder.
func WizardSections() []ConfigSection {
	var sections []ConfigSection
	for _, s := range CoreConfigSchema() {
		if s.WizardOrder > 0 {
			// Filter to wizard-visible fields only.
			var fields []ConfigField
			for _, f := range s.Fields {
				if f.WizardOrder > 0 {
					fields = append(fields, f)
				}
			}
			cs := s
			cs.Fields = fields
			sections = append(sections, cs)
		}
	}
	return sections
}

// GetSection returns a section by name.
func GetSection(name string) (ConfigSection, bool) {
	for _, s := range CoreConfigSchema() {
		if s.Name == name {
			return s, true
		}
	}
	return ConfigSection{}, false
}
