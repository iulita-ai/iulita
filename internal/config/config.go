package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// WriteSentinelFile creates a sentinel file at the given path.
// When present, config.Load() skips TOML file loading (DB-managed mode).
func WriteSentinelFile(path string) error {
	return os.WriteFile(path, []byte("db-managed\n"), 0644)
}

type Config struct {
	App           AppConfig           `koanf:"app"`
	Log           LogConfig           `koanf:"log"`
	Proxy         ProxyConfig         `koanf:"proxy"`
	Telegram      TelegramConfig      `koanf:"telegram"`
	Claude        ClaudeConfig        `koanf:"claude"`
	OpenAI        OpenAIConfig        `koanf:"openai"`
	Ollama        OllamaConfig        `koanf:"ollama"`
	Storage       StorageConfig       `koanf:"storage"`
	Server        ServerConfig        `koanf:"server"`
	Auth          AuthConfig          `koanf:"auth"`
	Skills        SkillsConfig        `koanf:"skills"`
	TechFacts     TechFactsConfig     `koanf:"techfacts"`
	Scheduler     SchedulerConfig     `koanf:"scheduler"`
	Heartbeat     HeartbeatConfig     `koanf:"heartbeat"`
	Embedding     EmbeddingConfig     `koanf:"embedding"`
	Security      SecurityConfig      `koanf:"security"`
	Cost          CostConfig          `koanf:"cost"`
	Routing       RoutingConfig       `koanf:"routing"`
	Cache         CacheConfig         `koanf:"cache"`
	Notify        NotifyConfig        `koanf:"notify"`
	Metrics       MetricsConfig       `koanf:"metrics"`
	Transcription TranscriptionConfig `koanf:"transcription"`
}

// CostConfig controls LLM cost tracking and limits.
type CostConfig struct {
	Enabled        bool                  `koanf:"enabled"`
	DailyLimitUSD  float64               `koanf:"daily_limit_usd"` // max daily spend in USD (0 = unlimited)
	AlertThreshold float64               `koanf:"alert_threshold"` // fraction (0-1) at which to warn (default: 0.8)
	Prices         map[string]ModelPrice `koanf:"prices"`          // per-model pricing
}

// ModelPrice defines cost per million tokens for a model.
type ModelPrice struct {
	InputPerMillion  float64 `koanf:"input"`  // $ per 1M input tokens
	OutputPerMillion float64 `koanf:"output"` // $ per 1M output tokens
}

// RoutingConfig controls model routing and query classification.
type RoutingConfig struct {
	Enabled                bool          `koanf:"enabled"`
	DefaultProvider        string        `koanf:"default_provider"` // "claude", "ollama", "openai"
	Routes                 []RouteConfig `koanf:"routes"`           // hint-based routing rules
	ClassificationEnabled  bool          `koanf:"classification_enabled"`
	ClassificationProvider string        `koanf:"classification_provider"` // provider for query classification (e.g. "ollama")
	MaxActionsPerHour      int           `koanf:"max_actions_per_hour"`    // global action rate limit (0 = unlimited)
}

// RouteConfig maps a hint to a specific provider.
type RouteConfig struct {
	Hint        string  `koanf:"hint"`        // e.g. "simple", "complex", "creative"
	Provider    string  `koanf:"provider"`    // "claude", "ollama", "openai"
	Temperature float64 `koanf:"temperature"` // optional temperature override
}

// CacheConfig controls response and embedding caching.
type CacheConfig struct {
	ResponseEnabled   bool   `koanf:"response_enabled"`    // enable LLM response cache
	ResponseTTL       string `koanf:"response_ttl"`        // Go duration, default "60m"
	ResponseMaxItems  int    `koanf:"response_max_items"`  // max cached responses (default: 1000)
	EmbeddingEnabled  bool   `koanf:"embedding_enabled"`   // enable embedding cache
	EmbeddingMaxItems int    `koanf:"embedding_max_items"` // max cached embeddings (default: 10000)
}

// NotifyConfig controls push notifications (Pushover/Ntfy).
type NotifyConfig struct {
	Provider string `koanf:"provider"` // "pushover", "ntfy", or empty (disabled)
	// Pushover
	PushoverToken   string `koanf:"pushover_token"`
	PushoverUserKey string `koanf:"pushover_user_key"`
	// Ntfy
	NtfyURL   string `koanf:"ntfy_url"`   // e.g. "https://ntfy.sh/my-topic"
	NtfyToken string `koanf:"ntfy_token"` // optional auth token
}

// MetricsConfig controls Prometheus metrics export.
type MetricsConfig struct {
	Enabled bool `koanf:"enabled"` // expose /metrics endpoint
}

// DiscordConfig holds Discord bot settings.
type DiscordConfig struct {
	Token             string   `koanf:"token"`
	AllowedChannelIDs []string `koanf:"allowed_channel_ids"` // empty = all channels
}

// TranscriptionConfig controls voice message transcription.
type TranscriptionConfig struct {
	Provider string `koanf:"provider"` // "openai" or empty (disabled)
	APIKey   string `koanf:"api_key"`  // API key for the provider
	Model    string `koanf:"model"`    // model name (default: "whisper-1")
}

// AuthConfig controls user authentication and multi-user mode.
type AuthConfig struct {
	JWTSecret     string `koanf:"jwt_secret"`     // auto-generated if empty
	TokenExpiry   string `koanf:"token_expiry"`   // Go duration, default "24h"
	RefreshExpiry string `koanf:"refresh_expiry"` // Go duration, default "168h" (7 days)
	AllowRegister bool   `koanf:"allow_register"` // allow self-registration via Telegram
	MultiUser     bool   `koanf:"multi_user"`     // enable multi-user mode (admin can create users)
}

type SecurityConfig struct {
	ConfigKeyEnv string `koanf:"config_key_env"` // env var name holding the encryption key (default: IULITA_CONFIG_KEY)
}

type ShellExecConfig struct {
	Enabled        bool     `koanf:"enabled"`
	AllowedBins    []string `koanf:"allowed_bins"`    // e.g. ["date", "uptime", "df"]
	Timeout        string   `koanf:"timeout"`         // Go duration, default: "10s"
	ForbiddenPaths []string `koanf:"forbidden_paths"` // paths blocked from arguments, e.g. ["~/.ssh", "/etc"]
	WorkspaceDir   string   `koanf:"workspace_dir"`   // if set, commands execute in this directory
	SystemPrompt   string   `koanf:"system_prompt"`   // appended to main system prompt for shell execution instructions
}

type HeartbeatConfig struct {
	Enabled  bool     `koanf:"enabled"`
	Interval string   `koanf:"interval"` // Go duration, e.g. "6h"
	ChatIDs  []string `koanf:"chat_ids"` // chats to send heartbeats to
}

type SchedulerConfig struct {
	Enabled      bool   `koanf:"enabled"`
	PollInterval string `koanf:"poll_interval"` // Go duration
	WorkerToken  string `koanf:"worker_token"`  // auth token for remote workers
	Concurrency  int    `koanf:"concurrency"`   // local worker concurrency
}

type TechFactsConfig struct {
	Enabled  bool   `koanf:"enabled"`
	Interval string `koanf:"interval"` // Go duration for deep analysis scheduler
	Delivery bool   `koanf:"delivery"` // send results to Telegram when done
	Model    string `koanf:"model"`    // override model for this job (e.g. "ollama" to use Ollama)
}

type InsightsConfig struct {
	Enabled          bool    `koanf:"enabled"`
	Interval         string  `koanf:"interval"`
	MinFacts         int     `koanf:"min_facts"`
	MaxPairs         int     `koanf:"max_pairs"`
	TTL              string  `koanf:"ttl"`
	QualityThreshold int     `koanf:"quality_threshold"`
	HalfLifeDays     float64 `koanf:"half_life_days"`
	Delivery         bool    `koanf:"delivery"`      // send results to Telegram when done
	Model            string  `koanf:"model"`         // override model for this job (e.g. "ollama" to use Ollama)
	SystemPrompt     string  `koanf:"system_prompt"` // appended to main system prompt for insight instructions
}

type MemoryConfig struct {
	HalfLifeDays float64  `koanf:"half_life_days"`
	MMRLambda    float64  `koanf:"mmr_lambda"`    // 0 = disabled, 0.7 recommended
	VectorWeight float64  `koanf:"vector_weight"` // 0 = pure FTS, 0.5 = balanced, 1 = pure vector
	Triggers     []string `koanf:"triggers"`      // keywords that force the remember tool
	SystemPrompt string   `koanf:"system_prompt"` // appended to main system prompt for memory instructions
}

type EmbeddingConfig struct {
	Provider string `koanf:"provider"`  // "onnx", "openai", or empty (disabled)
	Model    string `koanf:"model"`     // HuggingFace model ID for onnx, or OpenAI model name
	ModelDir string `koanf:"model_dir"` // directory for ONNX model files (default: "data/models")
	APIKey   string `koanf:"api_key"`   // API key for OpenAI embeddings
}

type AppConfig struct {
	SystemPrompt    string `koanf:"system_prompt"`
	DefaultTimezone string `koanf:"default_timezone"` // IANA timezone, e.g. "Europe/Helsinki"
	AutoLinkSummary bool   `koanf:"auto_link_summary"`
	MaxLinks        int    `koanf:"max_links"` // max URLs to fetch per message (default: 3)
}

type LogConfig struct {
	Level    string `koanf:"level"`
	Encoding string `koanf:"encoding"`
}

type ProxyConfig struct {
	URL string `koanf:"url"`
}

type TelegramConfig struct {
	Token          string  `koanf:"token"`
	AllowedIDs     []int64 `koanf:"allowed_ids"`
	DebounceWindow string  `koanf:"debounce_window"` // Go duration, e.g. "1.5s". 0 = disabled.
	RateLimit      int     `koanf:"rate_limit"`      // Max messages per rate_window (0 = unlimited)
	RateWindow     string  `koanf:"rate_window"`     // Go duration, default "1m"
}

type ClaudeConfig struct {
	APIKey         string `koanf:"api_key"`
	Model          string `koanf:"model"`
	MaxTokens      int    `koanf:"max_tokens"`
	ContextWindow  int    `koanf:"context_window"`
	BaseURL        string `koanf:"base_url"`        // custom API base URL (for proxies/gateways)
	Thinking       string `koanf:"thinking"`        // off/low/medium/high — extended thinking budget
	Streaming      bool   `koanf:"streaming"`       // stream responses with Telegram message editing
	RequestTimeout string `koanf:"request_timeout"` // per-message timeout (Go duration, default: "120s")
}

type OllamaConfig struct {
	URL   string `koanf:"url"`   // e.g. "http://localhost:11434"
	Model string `koanf:"model"` // e.g. "llama3"
}

type OpenAIConfig struct {
	APIKey    string `koanf:"api_key"`
	Model     string `koanf:"model"`
	MaxTokens int    `koanf:"max_tokens"`
	BaseURL   string `koanf:"base_url"` // custom base URL for compatible APIs
	Fallback  bool   `koanf:"fallback"` // use as fallback for Claude
}

type StorageConfig struct {
	Path string `koanf:"path"`
}

type ServerConfig struct {
	Enabled bool   `koanf:"enabled"`
	Address string `koanf:"address"`
}

type SkillsConfig struct {
	Dir       string               `koanf:"dir"`
	Memory    MemoryConfig         `koanf:"memory"`
	Insights  InsightsConfig       `koanf:"insights"`
	Web       WebConfig            `koanf:"web"`
	ShellExec ShellExecConfig      `koanf:"shell_exec"`
	Craft     CraftConfig          `koanf:"craft"`
	Google    GoogleConfig         `koanf:"google"`
	Todoist   TodoistConfig        `koanf:"todoist"`
	External  ExternalSkillsConfig `koanf:"external"`
}

// ExternalSkillsConfig controls external skill downloading, installation, and isolation.
type ExternalSkillsConfig struct {
	Enabled      bool         `koanf:"enabled"`       // master switch for external skills
	Dir          string       `koanf:"dir"`           // install directory (default: DataDir/external-skills)
	MaxInstalled int          `koanf:"max_installed"` // max number of installed skills (0 = unlimited)
	AllowShell   bool         `koanf:"allow_shell"`   // allow shell-isolation skills
	AllowDocker  bool         `koanf:"allow_docker"`  // allow Docker-isolated skills
	AllowWASM    bool         `koanf:"allow_wasm"`    // allow WASM-sandboxed skills
	Docker       DockerConfig `koanf:"docker"`
}

// DockerConfig controls Docker-based skill isolation.
type DockerConfig struct {
	Image          string `koanf:"image"`           // base Docker image (default: "python:3.12-slim")
	MemoryLimit    string `koanf:"memory_limit"`    // container memory limit (default: "256m")
	CPULimit       string `koanf:"cpu_limit"`       // container CPU limit (default: "0.5")
	Timeout        string `koanf:"timeout"`         // execution timeout (default: "30s")
	NetworkEnabled bool   `koanf:"network_enabled"` // allow network access in container
}

// GoogleConfig holds Google Workspace OAuth2 app-level credentials.
type GoogleConfig struct {
	ClientID        string `koanf:"client_id"`        // OAuth2 client ID from Google Cloud Console
	ClientSecret    string `koanf:"client_secret"`    // OAuth2 client secret
	RedirectURL     string `koanf:"redirect_url"`     // OAuth2 callback URL (e.g. https://iulita.example.com/api/google/callback)
	CredentialsFile string `koanf:"credentials_file"` // Path to service_account or authorized_user JSON for headless auth
	Scopes          string `koanf:"scopes"`           // Scope preset (readonly/readwrite/full) or JSON array of scope URLs
}

type CraftConfig struct {
	APIURL       string `koanf:"api_url"`       // Full Craft API base URL (e.g. https://connect.craft.do/links/XXX/api/v1)
	APIKey       string `koanf:"api_key"`       // Bearer token for Craft API authentication
	SystemPrompt string `koanf:"system_prompt"` // overrides embedded SKILL.md if set
}

type TodoistConfig struct {
	APIToken     string `koanf:"api_token"`     // Personal API token from Todoist Settings > Integrations > Developer
	SystemPrompt string `koanf:"system_prompt"` // overrides embedded SKILL.md if set
}

type WebConfig struct {
	Provider     string `koanf:"provider"`
	APIKey       string `koanf:"api_key"`
	SystemPrompt string `koanf:"system_prompt"` // overrides embedded SKILL.md if set
}

// ValidateMode specifies which validation rules to apply.
type ValidateMode int

const (
	// ValidateConsole applies minimal validation (console TUI mode).
	ValidateConsole ValidateMode = iota
	// ValidateServer applies full validation (headless server mode).
	ValidateServer
	// ValidateSetup allows starting with no LLM configured (web wizard will configure).
	ValidateSetup
)

// HasAnyLLMProvider returns true if at least one LLM provider is configured.
func (c *Config) HasAnyLLMProvider() bool {
	if c.Claude.APIKey != "" {
		return true
	}
	if c.OpenAI.APIKey != "" && c.OpenAI.Model != "" {
		return true
	}
	if c.Ollama.URL != "" && c.Ollama.Model != "" {
		return true
	}
	return false
}

// Validate checks for required fields and invalid values.
// mode determines which fields are required:
// - ValidateConsole: at least one LLM provider is required
// - ValidateServer: at least one LLM provider + at least one channel
// - ValidateSetup: no requirements (web wizard will configure)
func (c *Config) Validate(mode ValidateMode) error {
	if mode == ValidateSetup {
		return nil
	}
	if !c.HasAnyLLMProvider() {
		return fmt.Errorf("at least one LLM provider is required (Claude, OpenAI, or Ollama). Run 'iulita init' to configure")
	}
	if mode == ValidateServer && c.Telegram.Token == "" && !c.Server.Enabled {
		return fmt.Errorf("server mode requires at least one channel: set telegram.token or enable server with web chat")
	}
	if c.App.DefaultTimezone != "" {
		if _, err := time.LoadLocation(c.App.DefaultTimezone); err != nil {
			return fmt.Errorf("invalid app.default_timezone %q: %w", c.App.DefaultTimezone, err)
		}
	}
	return nil
}

// Load reads configuration using a layered approach:
// 1. Compiled-in defaults (from DefaultConfig)
// 2. TOML config file (optional — skipped if file doesn't exist)
// 3. Environment variables (IULITA_ prefix)
// 4. Secrets from keyring (Claude API key, Telegram token)
//
// Returns the parsed Config, the koanf instance, and whether a config file was loaded.
func Load(path string, paths *Paths) (*Config, *koanf.Koanf, bool, error) {
	k := koanf.New(".")

	// Layer 1: compiled defaults
	defaults := DefaultConfig(paths)
	defaultMap := structToMap(defaults)
	if err := k.Load(confmap.Provider(defaultMap, "."), nil); err != nil {
		return nil, nil, false, fmt.Errorf("loading defaults: %w", err)
	}

	// Layer 2: config file (optional — skipped if db_managed sentinel exists)
	configLoaded := false
	dbManaged := false
	if sentinel := filepath.Join(paths.ConfigDir, "db_managed"); fileExists(sentinel) {
		dbManaged = true
	}
	if path != "" && !dbManaged {
		if _, err := os.Stat(path); err == nil {
			if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
				return nil, nil, false, fmt.Errorf("loading config file %s: %w", path, err)
			}
			configLoaded = true
		}
	}

	// Layer 3: environment variables
	if err := k.Load(env.Provider("IULITA_", ".", func(s string) string {
		return strings.ReplaceAll(
			strings.ToLower(strings.TrimPrefix(s, "IULITA_")),
			"_", ".")
	}), nil); err != nil {
		return nil, nil, false, fmt.Errorf("loading env vars: %w", err)
	}

	// Layer 4: keyring secrets
	ks := NewKeyStore(paths)
	if apiKey := ks.GetSecret("IULITA_CLAUDE_API_KEY", keyringAccountAPI); apiKey != "" {
		_ = k.Load(confmap.Provider(map[string]interface{}{
			"claude.api_key": apiKey,
		}, "."), nil)
	}
	if tgToken := ks.GetSecret("IULITA_TELEGRAM_TOKEN", keyringAccountTG); tgToken != "" {
		_ = k.Load(confmap.Provider(map[string]interface{}{
			"telegram.token": tgToken,
		}, "."), nil)
	}

	// Layer 5: JWT secret — ensure it's stable across restarts.
	if jwtSecret, err := ks.EnsureJWTSecret(); err == nil && jwtSecret != "" {
		_ = k.Load(confmap.Provider(map[string]interface{}{
			"auth.jwt_secret": jwtSecret,
		}, "."), nil)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, nil, false, fmt.Errorf("unmarshaling config: %w", err)
	}

	return &cfg, k, configLoaded, nil
}

// structToMap converts the defaults struct to a flat dotted-key map for koanf.
func structToMap(cfg *Config) map[string]interface{} {
	m := make(map[string]interface{})

	// App
	m["app.system_prompt"] = cfg.App.SystemPrompt
	m["app.auto_link_summary"] = cfg.App.AutoLinkSummary
	m["app.max_links"] = cfg.App.MaxLinks

	// Log
	m["log.level"] = cfg.Log.Level
	m["log.encoding"] = cfg.Log.Encoding

	// Claude
	m["claude.model"] = cfg.Claude.Model
	m["claude.max_tokens"] = cfg.Claude.MaxTokens
	m["claude.context_window"] = cfg.Claude.ContextWindow
	m["claude.request_timeout"] = cfg.Claude.RequestTimeout

	// OpenAI
	m["openai.max_tokens"] = cfg.OpenAI.MaxTokens

	// Storage
	m["storage.path"] = cfg.Storage.Path

	// Server
	m["server.address"] = cfg.Server.Address

	// Auth
	m["auth.jwt_secret"] = cfg.Auth.JWTSecret
	m["auth.token_expiry"] = cfg.Auth.TokenExpiry
	m["auth.refresh_expiry"] = cfg.Auth.RefreshExpiry

	// Skills
	m["skills.dir"] = cfg.Skills.Dir
	m["skills.memory.half_life_days"] = cfg.Skills.Memory.HalfLifeDays
	m["skills.memory.mmr_lambda"] = cfg.Skills.Memory.MMRLambda
	m["skills.memory.system_prompt"] = cfg.Skills.Memory.SystemPrompt
	m["skills.insights.enabled"] = cfg.Skills.Insights.Enabled
	m["skills.insights.interval"] = cfg.Skills.Insights.Interval
	m["skills.insights.min_facts"] = cfg.Skills.Insights.MinFacts
	m["skills.insights.max_pairs"] = cfg.Skills.Insights.MaxPairs
	m["skills.insights.ttl"] = cfg.Skills.Insights.TTL
	m["skills.insights.quality_threshold"] = cfg.Skills.Insights.QualityThreshold
	m["skills.shell_exec.timeout"] = cfg.Skills.ShellExec.Timeout
	m["skills.google.redirect_url"] = cfg.Skills.Google.RedirectURL

	// Scheduler
	m["scheduler.enabled"] = cfg.Scheduler.Enabled
	m["scheduler.poll_interval"] = cfg.Scheduler.PollInterval
	m["scheduler.concurrency"] = cfg.Scheduler.Concurrency

	// TechFacts
	m["techfacts.enabled"] = cfg.TechFacts.Enabled
	m["techfacts.interval"] = cfg.TechFacts.Interval

	// Heartbeat
	m["heartbeat.interval"] = cfg.Heartbeat.Interval

	// Embedding
	m["embedding.model_dir"] = cfg.Embedding.ModelDir

	// Telegram
	m["telegram.debounce_window"] = cfg.Telegram.DebounceWindow
	m["telegram.rate_window"] = cfg.Telegram.RateWindow

	// Cost
	m["cost.alert_threshold"] = cfg.Cost.AlertThreshold

	// Routing
	m["routing.default_provider"] = cfg.Routing.DefaultProvider

	// External skills
	m["skills.external.enabled"] = cfg.Skills.External.Enabled
	m["skills.external.dir"] = cfg.Skills.External.Dir
	m["skills.external.max_installed"] = cfg.Skills.External.MaxInstalled
	m["skills.external.allow_shell"] = cfg.Skills.External.AllowShell
	m["skills.external.allow_docker"] = cfg.Skills.External.AllowDocker
	m["skills.external.allow_wasm"] = cfg.Skills.External.AllowWASM
	m["skills.external.docker.image"] = cfg.Skills.External.Docker.Image
	m["skills.external.docker.memory_limit"] = cfg.Skills.External.Docker.MemoryLimit
	m["skills.external.docker.cpu_limit"] = cfg.Skills.External.Docker.CPULimit
	m["skills.external.docker.timeout"] = cfg.Skills.External.Docker.Timeout

	// Cache
	m["cache.response_ttl"] = cfg.Cache.ResponseTTL
	m["cache.response_max_items"] = cfg.Cache.ResponseMaxItems
	m["cache.embedding_enabled"] = cfg.Cache.EmbeddingEnabled
	m["cache.embedding_max_items"] = cfg.Cache.EmbeddingMaxItems

	return m
}
