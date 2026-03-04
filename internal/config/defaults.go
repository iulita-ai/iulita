package config

// DefaultConfig returns a Config with sensible defaults for local installation.
// No config file needed — the app works out of the box with just an API key.
func DefaultConfig(paths *Paths) *Config {
	return &Config{
		App: AppConfig{
			SystemPrompt:    "You are Iulita, a helpful personal AI assistant. Be concise and helpful.",
			AutoLinkSummary: false,
			MaxLinks:        3,
		},
		Log: LogConfig{
			Level:    "info",
			Encoding: "console",
		},
		Claude: ClaudeConfig{
			Model:          "claude-sonnet-4-5-20250929",
			MaxTokens:      4096,
			ContextWindow:  200000,
			RequestTimeout: "120s",
		},
		OpenAI: OpenAIConfig{
			MaxTokens: 4096,
		},
		Storage: StorageConfig{
			Path: paths.DatabaseFile(),
		},
		Server: ServerConfig{
			Address: ":8080",
		},
		Auth: AuthConfig{
			TokenExpiry:   "24h",
			RefreshExpiry: "168h",
		},
		Skills: SkillsConfig{
			Dir: paths.SkillsDir(),
			Memory: MemoryConfig{
				HalfLifeDays: 30,
				MMRLambda:    0.7,
				Triggers:     []string{"remember", "save this", "note this", "don't forget"},
				SystemPrompt: defaultMemorySystemPrompt,
			},
			Insights: InsightsConfig{
				Enabled:          true,
				Interval:         "24h",
				MinFacts:         20,
				MaxPairs:         6,
				TTL:              "720h",
				QualityThreshold: 3,
			},
			ShellExec: ShellExecConfig{
				Timeout:        "10s",
				ForbiddenPaths: []string{"~/.ssh", "~/.gnupg", "/etc/shadow", "/etc/passwd"},
			},
			Google: GoogleConfig{
				RedirectURL: "http://localhost:8080/api/google/callback",
			},
			External: ExternalSkillsConfig{
				Enabled:      true,
				Dir:          paths.ExternalSkillsDir(),
				MaxInstalled: 50,
				AllowShell:   false,
				AllowDocker:  false,
				AllowWASM:    true,
				Docker: DockerConfig{
					Image:       "python:3.12-slim",
					MemoryLimit: "256m",
					CPULimit:    "0.5",
					Timeout:     "30s",
				},
			},
		},
		Scheduler: SchedulerConfig{
			Enabled:      true,
			PollInterval: "30s",
			Concurrency:  2,
		},
		TechFacts: TechFactsConfig{
			Enabled:  true,
			Interval: "6h",
		},
		Heartbeat: HeartbeatConfig{
			Interval: "6h",
		},
		Embedding: EmbeddingConfig{
			Provider: "onnx",
			ModelDir: paths.ModelsDir(),
		},
		Telegram: TelegramConfig{
			DebounceWindow: "1.5s",
			RateWindow:     "1m",
		},
		Cost: CostConfig{
			AlertThreshold: 0.8,
			Prices:         defaultModelPrices(),
		},
		Routing: RoutingConfig{
			DefaultProvider: "claude",
		},
		Cache: CacheConfig{
			ResponseTTL:       "60m",
			ResponseMaxItems:  1000,
			EmbeddingEnabled:  true,
			EmbeddingMaxItems: 10000,
		},
	}
}

const defaultMemorySystemPrompt = `MEMORY RULES:
- When the user asks you to remember, save, or note something, you MUST call the ` + "`remember`" + ` tool. Never just reply conversationally — always call the tool first, then confirm.
- If the user says "remember this" referring to a previous message, extract the relevant content from conversation history and save it via the ` + "`remember`" + ` tool.
- When recalling information, use the ` + "`recall`" + ` tool to search memory before answering from general knowledge.`

func defaultModelPrices() map[string]ModelPrice {
	return map[string]ModelPrice{
		"claude-sonnet-4-5-20250929": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
		"claude-haiku-3-5-20241022":  {InputPerMillion: 0.80, OutputPerMillion: 4.0},
	}
}
