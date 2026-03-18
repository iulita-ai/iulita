package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/iulita-ai/iulita/internal/assistant"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/bookmark"
	"github.com/iulita-ai/iulita/internal/channel"
	consolech "github.com/iulita-ai/iulita/internal/channel/console"
	"github.com/iulita-ai/iulita/internal/channel/telegram"
	"github.com/iulita-ai/iulita/internal/channelmgr"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/cost"
	"github.com/iulita-ai/iulita/internal/dashboard"
	"github.com/iulita-ai/iulita/internal/doctor"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/llm/claude"
	"github.com/iulita-ai/iulita/internal/llm/ollama"
	"github.com/iulita-ai/iulita/internal/llm/onnx"
	openaillm "github.com/iulita-ai/iulita/internal/llm/openai"
	"github.com/iulita-ai/iulita/internal/metrics"
	"github.com/iulita-ai/iulita/internal/notify"
	"github.com/iulita-ai/iulita/internal/ratelimit"
	"github.com/iulita-ai/iulita/internal/scheduler"
	"github.com/iulita-ai/iulita/internal/scheduler/handlers"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/craft"
	"github.com/iulita-ai/iulita/internal/skill/datetime"
	"github.com/iulita-ai/iulita/internal/skill/delegate"
	"github.com/iulita-ai/iulita/internal/skill/directives"
	"github.com/iulita-ai/iulita/internal/skill/exchange"
	"github.com/iulita-ai/iulita/internal/skill/geolocation"
	googleskill "github.com/iulita-ai/iulita/internal/skill/google"
	insightskill "github.com/iulita-ai/iulita/internal/skill/insights"
	localeskill "github.com/iulita-ai/iulita/internal/skill/locale"
	"github.com/iulita-ai/iulita/internal/skill/memory"
	"github.com/iulita-ai/iulita/internal/skill/orchestrate"
	"github.com/iulita-ai/iulita/internal/skill/pdfreader"
	"github.com/iulita-ai/iulita/internal/skill/reminders"
	"github.com/iulita-ai/iulita/internal/skill/shellexec"
	"github.com/iulita-ai/iulita/internal/skill/skillinfo"
	tasksskill "github.com/iulita-ai/iulita/internal/skill/tasks"
	"github.com/iulita-ai/iulita/internal/skill/todoist"
	"github.com/iulita-ai/iulita/internal/skill/tokenusage"
	"github.com/iulita-ai/iulita/internal/skill/weather"
	"github.com/iulita-ai/iulita/internal/skill/webfetch"
	"github.com/iulita-ai/iulita/internal/skill/websearch"
	"github.com/iulita-ai/iulita/internal/skillmgr"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"github.com/iulita-ai/iulita/internal/transcription"
	"github.com/iulita-ai/iulita/internal/version"
	"github.com/iulita-ai/iulita/internal/web"
	"github.com/iulita-ai/iulita/ui"
)

// registrySkillChecker adapts skill.Registry to dashboard.SkillEnabledChecker.
type registrySkillChecker struct {
	r *skill.Registry
}

func (c registrySkillChecker) IsEnabled(name string) bool {
	return !c.r.IsDisabled(name)
}

func main() {
	// Handle --version / -v flag.
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println("iulita " + version.String())
		return
	}

	// Handle --doctor subcommand.
	if len(os.Args) > 1 && os.Args[1] == "--doctor" {
		runDoctor()
		return
	}

	// Default mode is console TUI. Use --server (-d) for headless server mode.
	serverMode := false
	initMode := false
	printDefaults := false
	var filteredArgs []string
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--server", "-d":
			serverMode = true
		case "init":
			initMode = true
		case "--print-defaults":
			printDefaults = true
		default:
			filteredArgs = append(filteredArgs, arg)
		}
	}
	consoleMode := !serverMode

	// Resolve XDG-compliant paths.
	paths := config.ResolvePaths()
	if err := paths.EnsureDirs(); err != nil {
		log.Fatalf("failed to create data directories: %v", err)
	}

	// Handle 'iulita init' subcommand.
	if initMode {
		if printDefaults {
			content, err := config.GenerateDefaultConfig(paths)
			if err != nil {
				log.Fatalf("failed to generate config: %v", err)
			}
			fmt.Print(content)
			return
		}
		result, err := config.RunSetupWizard(paths)
		if err != nil {
			log.Fatalf("setup failed: %v", err)
		}
		fmt.Printf("\nSetup complete! Secrets saved to %s.\n", result.SavedTo)
		fmt.Println("Run 'iulita' to start the assistant.")
		return
	}

	// Config file resolution: explicit arg > env > XDG path > cwd fallback
	configPath := ""
	if v := os.Getenv("IULITA_CONFIG"); v != "" {
		configPath = v
	}
	if len(filteredArgs) > 0 {
		configPath = filteredArgs[0]
	}
	if configPath == "" {
		// Try XDG config path first, fall back to cwd config.toml
		xdgConfig := paths.ConfigFile()
		if _, err := os.Stat(xdgConfig); err == nil {
			configPath = xdgConfig
		} else if _, err := os.Stat("config.toml"); err == nil {
			configPath = "config.toml"
		}
		// configPath may remain empty — that's OK, defaults will be used
	}

	cfg, koanfInstance, configLoaded, err := config.Load(configPath, paths)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	_ = configLoaded // used for logging below

	logger, err := buildLogger(cfg.Log, consoleMode, paths.LogFile())
	if err != nil {
		log.Fatalf("failed to build logger: %v", err)
	}
	defer logger.Sync()

	// Attach version to every log message.
	logger = logger.With(zap.String("version", version.Short()))

	if configLoaded {
		logger.Info("config loaded from file", zap.String("path", configPath))
	} else {
		logger.Info("no config file found, using defaults + env vars",
			zap.String("config_dir", paths.ConfigDir),
			zap.String("data_dir", paths.DataDir),
		)
	}

	if cfg.Telegram.Token != "" && len(cfg.Telegram.AllowedIDs) == 0 {
		logger.Warn("telegram.allowed_ids is empty — bot is accessible to everyone")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// WaitGroup for miscellaneous background goroutines (backfill, etc.).
	var wgBackground sync.WaitGroup

	// Storage — ensure parent directory exists (for XDG or custom paths).
	if dir := filepath.Dir(cfg.Storage.Path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			logger.Fatal("failed to create storage directory", zap.String("dir", dir), zap.Error(err))
		}
	}
	store, err := sqlite.New(cfg.Storage.Path)
	if err != nil {
		logger.Fatal("failed to open storage", zap.Error(err))
	}

	if err := store.RunMigrations(ctx); err != nil {
		logger.Fatal("failed to run migrations", zap.Error(err))
	}

	// Initialize i18n catalog.
	if err := i18n.Init(); err != nil {
		logger.Fatal("failed to initialize i18n", zap.Error(err))
	}

	// Bootstrap default admin user on first run + bind existing Telegram IDs.
	// Must run BEFORE backfill so channel bindings exist for user_id resolution.
	var authService *auth.Service
	if err := bootstrapAdminUser(ctx, store, cfg.Telegram.AllowedIDs, logger); err != nil {
		logger.Fatal("failed to bootstrap admin user", zap.Error(err))
	}

	// Backfill user_id on facts/insights/tech_facts/messages from channel bindings.
	if n, err := store.BackfillUserIDs(ctx); err != nil {
		logger.Warn("user_id backfill failed", zap.Error(err))
	} else {
		logger.Info("user_id backfill completed", zap.Int64("updated", n))
	}

	// Runtime config store (DB overrides on top of TOML+env).
	keyStore := config.NewKeyStore(paths)
	var encryptor *config.Encryptor
	if encKey, err := keyStore.EnsureEncryptionKey(); err != nil {
		logger.Warn("failed to ensure encryption key, trying env fallback", zap.Error(err))
		encryptor, err = config.NewEncryptorFromEnv(cfg.Security.ConfigKeyEnv)
		if err != nil {
			logger.Fatal("failed to create config encryptor", zap.Error(err))
		}
	} else if encKey != nil {
		encryptor, err = config.NewEncryptor(encKey)
		if err != nil {
			logger.Fatal("failed to create config encryptor from keystore", zap.Error(err))
		}
	}
	if encryptor != nil {
		logger.Info("config encryption enabled")
	}
	cfgStore := config.NewStore(cfg, koanfInstance, store, encryptor, logger)
	if err := cfgStore.LoadOverrides(ctx); err != nil {
		logger.Fatal("failed to load config overrides", zap.Error(err))
	}

	// Check wizard completion state and determine validation mode.
	// DB overrides may contain LLM keys from a previous wizard run.
	setupMode := false
	wizardCompleted := false
	if val, ok := cfgStore.Get("_system.wizard_completed"); ok && val == "true" {
		wizardCompleted = true
	}

	// Re-read effective config with DB overrides applied for validation.
	// Check if LLM providers are configured (base config + DB overrides).
	hasLLMFromOverrides := false
	if apiKey, ok := cfgStore.GetEffective("claude.api_key"); ok && apiKey != "" {
		hasLLMFromOverrides = true
	}
	if apiKey, ok := cfgStore.GetEffective("openai.api_key"); ok && apiKey != "" {
		if model, ok2 := cfgStore.GetEffective("openai.model"); ok2 && model != "" {
			hasLLMFromOverrides = true
		}
	}
	if url, ok := cfgStore.GetEffective("ollama.url"); ok && url != "" {
		if model, ok2 := cfgStore.GetEffective("ollama.model"); ok2 && model != "" {
			hasLLMFromOverrides = true
		}
	}

	validateMode := config.ValidateConsole
	if serverMode {
		cfg.Server.Enabled = true
		validateMode = config.ValidateServer
	}

	// In server mode, allow setup mode if wizard not completed and no LLM configured.
	if serverMode && !wizardCompleted && !cfg.HasAnyLLMProvider() && !hasLLMFromOverrides {
		validateMode = config.ValidateSetup
		setupMode = true
		logger.Info("starting in setup mode — web wizard required")
	}

	if err := cfg.Validate(validateMode); err != nil {
		if !cfg.HasAnyLLMProvider() && consoleMode {
			fmt.Println("No LLM provider configured. Run 'iulita init' to set up.")
			os.Exit(1)
		}
		log.Fatalf("invalid config: %v", err)
	}

	// Auth service — always created so dashboard login works (including setup mode).
	{
		tokenExpiry := 24 * time.Hour
		refreshExpiry := 7 * 24 * time.Hour
		if cfg.Auth.TokenExpiry != "" {
			if d, err := time.ParseDuration(cfg.Auth.TokenExpiry); err == nil {
				tokenExpiry = d
			}
		}
		if cfg.Auth.RefreshExpiry != "" {
			if d, err := time.ParseDuration(cfg.Auth.RefreshExpiry); err == nil {
				refreshExpiry = d
			}
		}
		authService = auth.NewService(store, cfg.Auth.JWTSecret, tokenExpiry, refreshExpiry)
		logger.Info("auth service ready")
	}

	// Setup mode: start only the dashboard server for the web wizard.
	// No LLM providers, channels, skills, or scheduler — just auth + config + dashboard.
	if setupMode {
		staticFS, err := ui.DistFS()
		if err != nil {
			logger.Fatal("failed to load dashboard UI", zap.Error(err))
		}

		address := cfg.Server.Address
		if address == "" {
			address = ":8080"
		}

		dashSrv := dashboard.New(dashboard.Config{
			Address:     address,
			Store:       store,
			StaticFS:    staticFS,
			Logger:      logger,
			ConfigStore: cfgStore,
			AuthService: authService,
			SetupMode:   true,
		})

		logger.Info("starting in setup mode — complete the wizard at the dashboard",
			zap.String("address", address),
		)
		go func() {
			if err := dashSrv.Start(ctx); err != nil && ctx.Err() == nil {
				logger.Error("dashboard server error", zap.Error(err))
			}
		}()

		<-ctx.Done()
		logger.Info("shutdown signal received")
		if err := store.Close(); err != nil {
			logger.Error("failed to close storage", zap.Error(err))
		}
		logger.Info("iulita stopped (setup mode)")
		return
	}

	// Bootstrap config-sourced Telegram channel instance from config.toml.
	if cfg.Telegram.Token != "" {
		if err := bootstrapConfigChannelInstance(ctx, store, "tg-config", domain.ChannelTypeTelegram, "Telegram (config.toml)", true, logger); err != nil {
			logger.Error("failed to bootstrap config channel instance", zap.Error(err))
		}
	}

	// Bootstrap Web Chat stub (always present, disabled by default).
	if err := bootstrapConfigChannelInstance(ctx, store, "webchat", domain.ChannelTypeWeb, "Web Chat", false, logger); err != nil {
		logger.Error("failed to bootstrap web chat channel instance", zap.Error(err))
	}

	// Bootstrap Console channel in console mode (default).
	// In server mode, explicitly disable it so a leftover DB entry doesn't start the TUI.
	if consoleMode {
		if err := bootstrapConfigChannelInstance(ctx, store, "console", domain.ChannelTypeConsole, "Console", true, logger); err != nil {
			logger.Error("failed to bootstrap console channel instance", zap.Error(err))
		}
	} else {
		disableChannelInstance(ctx, store, "console", logger)
	}

	// User resolver for mapping channel identities to iulita users.
	userResolver := channel.NewDBUserResolver(store, cfg.Auth.AllowRegister, logger)

	// Embedding provider for hybrid search.
	var embedder llm.EmbeddingProvider
	if cfg.Embedding.Provider != "" {
		if err := store.CreateVectorTables(ctx); err != nil {
			logger.Error("failed to create vector tables", zap.Error(err))
		}

		switch strings.ToLower(cfg.Embedding.Provider) {
		case "onnx":
			modelDir := cfg.Embedding.ModelDir
			if modelDir == "" {
				modelDir = "data/models"
			}
			onnxProvider, err := onnx.New(modelDir, cfg.Embedding.Model, logger)
			if err != nil {
				logger.Error("failed to create ONNX embedding provider", zap.Error(err))
			} else {
				embedder = onnxProvider
				logger.Info("ONNX embedding provider ready",
					zap.String("model_dir", modelDir),
					zap.Int("dimensions", onnxProvider.Dimensions()),
				)
			}
		default:
			logger.Warn("unknown embedding provider", zap.String("provider", cfg.Embedding.Provider))
		}

		// Wrap embedder with caching layer if enabled.
		if embedder != nil && cfg.Cache.EmbeddingEnabled {
			embeddingCache := llm.NewStorageEmbeddingCacheAdapter(store)
			maxItems := cfg.Cache.EmbeddingMaxItems
			if maxItems <= 0 {
				maxItems = 10000
			}
			embedder = llm.NewCachedEmbeddingProvider(embedder, embeddingCache, maxItems)
			logger.Info("embedding cache enabled", zap.Int("max_items", maxItems))
		}
	}

	if cfg.Skills.Memory.HalfLifeDays > 0 {
		store.SetHalfLifeDays(cfg.Skills.Memory.HalfLifeDays)
	}
	if cfg.Skills.Memory.MMRLambda > 0 {
		store.SetMMRLambda(cfg.Skills.Memory.MMRLambda)
	}
	logger.Info("storage ready", zap.String("path", cfg.Storage.Path))

	// HTTP clients (proxy support).
	transport := buildTransport(cfg.Proxy.URL, logger)
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	// LLM HTTP client has no timeout — cancellation is managed by context deadlines
	// in HandleMessage. This avoids premature kills during extended thinking or
	// large context requests.
	llmHTTPClient := &http.Client{Transport: transport}
	tgHTTPClient := &http.Client{Transport: transport} // no timeout for long polling

	// LLM provider chain — build from configured providers.
	var rawProvider *claude.Provider // may be nil if Claude not configured
	var llmProvider llm.Provider

	if cfg.Claude.APIKey != "" {
		rawProvider = claude.New(cfg.Claude.APIKey, cfg.Claude.Model, cfg.Claude.MaxTokens, cfg.Claude.BaseURL, llmHTTPClient)
		llmProvider = llm.NewRetryProvider(rawProvider, llm.DefaultRetryConfig())
		logger.Info("claude provider ready", zap.String("model", cfg.Claude.Model))
	}

	var openaiProvider llm.Provider
	if cfg.OpenAI.APIKey != "" && cfg.OpenAI.Model != "" {
		openaiMaxTokens := cfg.OpenAI.MaxTokens
		if openaiMaxTokens <= 0 {
			openaiMaxTokens = 4096
		}
		openaiProvider = openaillm.New(cfg.OpenAI.APIKey, cfg.OpenAI.Model, openaiMaxTokens, cfg.OpenAI.BaseURL, httpClient)
		if llmProvider == nil {
			// OpenAI is primary provider.
			llmProvider = llm.NewRetryProvider(openaiProvider, llm.DefaultRetryConfig())
			logger.Info("openai primary provider configured", zap.String("model", cfg.OpenAI.Model))
		} else if cfg.OpenAI.Fallback {
			llmProvider = llm.NewFallbackProvider(llmProvider, openaiProvider)
			logger.Info("openai fallback provider configured", zap.String("model", cfg.OpenAI.Model))
		} else {
			logger.Info("openai provider available", zap.String("model", cfg.OpenAI.Model))
		}
	}

	// Wrap with response caching if enabled.
	if cfg.Cache.ResponseEnabled {
		responseCache := llm.NewStorageResponseCacheAdapter(store)
		ttl := 60 * time.Minute
		if cfg.Cache.ResponseTTL != "" {
			if d, err := time.ParseDuration(cfg.Cache.ResponseTTL); err == nil {
				ttl = d
			}
		}
		maxItems := cfg.Cache.ResponseMaxItems
		if maxItems <= 0 {
			maxItems = 1000
		}
		llmProvider = llm.NewCachingProvider(llmProvider, responseCache, ttl, maxItems)
		logger.Info("LLM response cache enabled", zap.Duration("ttl", ttl), zap.Int("max_items", maxItems))
	}

	// Build Ollama provider (used for routing, delegation, classification, or as primary).
	var ollamaProvider llm.Provider
	if cfg.Ollama.URL != "" && cfg.Ollama.Model != "" {
		raw := ollama.New(cfg.Ollama.URL, cfg.Ollama.Model, httpClient)
		ollamaProvider = llm.NewXMLToolProvider(raw) // enable tool calling via XML injection
		if llmProvider == nil {
			// Ollama is primary provider.
			llmProvider = ollamaProvider
			logger.Info("ollama primary provider configured (XML tool enabled)", zap.String("model", cfg.Ollama.Model))
		} else {
			logger.Info("ollama provider ready (XML tool enabled)", zap.String("model", cfg.Ollama.Model))
		}
	}

	// Auto-register Claude Haiku as a cheap/fast routing target when Claude is configured.
	var claudeHaikuProvider llm.Provider
	if rawProvider != nil {
		haikuModel := "claude-haiku-4-5-20251001"
		// Don't create a haiku instance if the primary model is already haiku.
		if cfg.Claude.Model != haikuModel && cfg.Claude.Model != "claude-haiku-4-5" {
			haikuRaw := claude.New(cfg.Claude.APIKey, haikuModel, cfg.Claude.MaxTokens, cfg.Claude.BaseURL, llmHTTPClient)
			claudeHaikuProvider = llm.NewRetryProvider(haikuRaw, llm.DefaultRetryConfig())
			logger.Info("claude-haiku provider ready (auto-registered)", zap.String("model", haikuModel))
		}
	}

	// Model routing with hint-based provider selection.
	// Enable routing automatically when claude-haiku or other secondary providers are available,
	// even without explicit routing.enabled in config.
	// Activate routing when there are genuinely distinct providers to route between.
	hasSecondaryProviders := claudeHaikuProvider != nil ||
		(openaiProvider != nil && llmProvider != openaiProvider) ||
		(ollamaProvider != nil && llmProvider != ollamaProvider)
	if cfg.Routing.Enabled || hasSecondaryProviders {
		routes := make(map[string]llm.Provider)
		providerMap := map[string]llm.Provider{"claude": llmProvider}
		if claudeHaikuProvider != nil {
			providerMap["claude-haiku"] = claudeHaikuProvider
			routes["claude-haiku"] = claudeHaikuProvider
		}
		if openaiProvider != nil {
			providerMap["openai"] = openaiProvider
			routes["openai"] = openaiProvider
		}
		if ollamaProvider != nil {
			providerMap["ollama"] = ollamaProvider
			routes["ollama"] = ollamaProvider
		}
		for _, route := range cfg.Routing.Routes {
			if p, ok := providerMap[route.Provider]; ok {
				routes[route.Hint] = p
			}
		}
		router := llm.NewRoutingProvider(llmProvider, routes)

		// Optionally wrap with query classification.
		if cfg.Routing.ClassificationEnabled && ollamaProvider != nil {
			classifier := ollamaProvider
			if p, ok := providerMap[cfg.Routing.ClassificationProvider]; ok {
				classifier = p
			}
			llmProvider = llm.NewClassifyingProvider(classifier, router)
			logger.Info("query classification enabled", zap.String("classifier", cfg.Routing.ClassificationProvider))
		} else {
			llmProvider = router
		}
		logger.Info("model routing enabled", zap.Int("routes", len(routes)))
	}

	// Cost tracker.
	var costTracker *cost.Tracker
	if cfg.Cost.Enabled {
		costTracker = cost.New(cfg.Cost)
		logger.Info("cost tracking enabled", zap.Float64("daily_limit_usd", cfg.Cost.DailyLimitUSD))
	}

	// Global action rate limiter.
	var actionLimiter *ratelimit.ActionLimiter
	if cfg.Routing.MaxActionsPerHour > 0 {
		actionLimiter = ratelimit.NewActionLimiter(cfg.Routing.MaxActionsPerHour, time.Hour)
		logger.Info("action rate limiter enabled", zap.Int("max_per_hour", cfg.Routing.MaxActionsPerHour))
	}

	// Skills
	registry := skill.NewRegistry()

	var caps []string
	if cfg.Skills.Web.APIKey != "" {
		caps = append(caps, "web")
	}
	caps = append(caps, "memory")
	registry.SetCapabilities(caps)
	registry.Register(datetime.New())

	// Exchange rate skill (no auth needed, free API).
	exchangeManifest, err := exchange.LoadManifest()
	if err != nil {
		logger.Warn("failed to load exchange manifest", zap.Error(err))
	}
	registry.RegisterWithManifest(exchange.New(httpClient), exchangeManifest)

	// Geolocation skill (no auth needed for primary path, free APIs).
	geoManifest, err := geolocation.LoadManifest()
	if err != nil {
		logger.Warn("failed to load geolocation manifest", zap.Error(err))
	}
	geoSkill := geolocation.New(httpClient)
	registry.RegisterWithManifest(geoSkill, geoManifest)

	// Weather skill (uses geolocation via registry for auto-detect, free APIs).
	weatherManifest, err := weather.LoadManifest()
	if err != nil {
		logger.Warn("failed to load weather manifest", zap.Error(err))
	}
	registry.RegisterWithManifest(weather.New(store, registry, httpClient), weatherManifest)

	registry.Register(reminders.New(store))
	registry.Register(directives.New(store))

	// Locale switching skill (with force triggers for reliable tool invocation).
	localeManifest, err := localeskill.LoadManifest()
	if err != nil {
		logger.Warn("failed to load locale manifest", zap.Error(err))
	}
	registry.RegisterWithManifest(localeskill.New(store), localeManifest)

	// Memory skills — register with manifest for system prompt.
	memManifest, err := memory.LoadManifest()
	if err != nil {
		logger.Warn("failed to load memory manifest", zap.Error(err))
	}
	// Override system prompt from config if provided.
	if cfg.Skills.Memory.SystemPrompt != "" && memManifest != nil {
		memManifest.SystemPrompt = cfg.Skills.Memory.SystemPrompt
	}
	registry.RegisterWithManifest(memory.NewRemember(store), memManifest)
	registry.RegisterInGroup(memory.NewRecall(store), "memory")
	registry.RegisterInGroup(memory.NewForget(store), "memory")

	// Insight skills — register with manifest.
	insightManifest, err := insightskill.LoadManifest()
	if err != nil {
		logger.Warn("failed to load insights manifest", zap.Error(err))
	}
	if cfg.Skills.Insights.SystemPrompt != "" && insightManifest != nil {
		insightManifest.SystemPrompt = cfg.Skills.Insights.SystemPrompt
	}
	registry.RegisterWithManifest(insightskill.NewList(store), insightManifest)
	registry.RegisterInGroup(insightskill.NewDismiss(store), "insights")
	registry.RegisterInGroup(insightskill.NewPromote(store), "insights")

	// Web skills — always register (capability-gated).
	webManifest, err := websearch.LoadManifest()
	if err != nil {
		logger.Warn("failed to load websearch manifest", zap.Error(err))
	}
	if cfg.Skills.Web.SystemPrompt != "" && webManifest != nil {
		webManifest.SystemPrompt = cfg.Skills.Web.SystemPrompt
	}
	braveClient := web.NewBraveClient(cfg.Skills.Web.APIKey, httpClient)
	ddgClient := web.NewDDGClient(httpClient)
	searcher := web.NewFallbackSearcher(braveClient, ddgClient)
	webSearchSkill := websearch.New(searcher, braveClient)
	webSearchSkill.SetReloader(registry, cfgStore)
	registry.RegisterWithManifest(webSearchSkill, webManifest)
	registry.RegisterInGroup(webfetch.New(web.NewSafeHTTPClient(15*time.Second, httpClient)), "websearch")
	// Web capability always on — DDG fallback requires no API key.
	caps = append(caps, "web")
	registry.SetCapabilities(caps)
	if cfg.Skills.Web.APIKey != "" {
		logger.Info("web search: Brave (primary) + DDG (fallback)")
	} else {
		logger.Info("web search: DDG only (no Brave API key)")
	}

	// Shell exec — register with manifest.
	if cfg.Skills.ShellExec.Enabled && len(cfg.Skills.ShellExec.AllowedBins) > 0 {
		timeout := 10 * time.Second
		if cfg.Skills.ShellExec.Timeout != "" {
			if d, err := time.ParseDuration(cfg.Skills.ShellExec.Timeout); err == nil {
				timeout = d
			}
		}
		shellManifest, err := shellexec.LoadManifest()
		if err != nil {
			logger.Warn("failed to load shellexec manifest", zap.Error(err))
		}
		if cfg.Skills.ShellExec.SystemPrompt != "" && shellManifest != nil {
			shellManifest.SystemPrompt = cfg.Skills.ShellExec.SystemPrompt
		}
		registry.RegisterWithManifest(shellexec.New(shellexec.Config{
			AllowedBins:    cfg.Skills.ShellExec.AllowedBins,
			Timeout:        timeout,
			ForbiddenPaths: cfg.Skills.ShellExec.ForbiddenPaths,
			WorkspaceDir:   cfg.Skills.ShellExec.WorkspaceDir,
		}), shellManifest)
		logger.Info("shell exec skill registered", zap.Strings("allowed_bins", cfg.Skills.ShellExec.AllowedBins))
	}

	// Craft skills — always register (capability-gated).
	craftManifest, err := craft.LoadManifest()
	if err != nil {
		logger.Warn("failed to load craft manifest", zap.Error(err))
	}
	if cfg.Skills.Craft.SystemPrompt != "" && craftManifest != nil {
		craftManifest.SystemPrompt = cfg.Skills.Craft.SystemPrompt
	}
	craftClient := craft.NewClient(cfg.Skills.Craft.APIURL, cfg.Skills.Craft.APIKey, httpClient)
	craftSearch := craft.NewSearch(craftClient)
	craftSearch.SetReloader(registry, cfgStore)
	registry.RegisterWithManifest(craftSearch, craftManifest)
	registry.RegisterInGroup(craft.NewRead(craftClient), "craft")
	registry.RegisterInGroup(craft.NewWrite(craftClient), "craft")
	registry.RegisterInGroup(craft.NewTasks(craftClient), "craft")
	if cfg.Skills.Craft.APIURL != "" && cfg.Skills.Craft.APIKey != "" {
		caps = append(caps, "craft")
		registry.SetCapabilities(caps)
		logger.Info("craft skills registered (active)")
	} else {
		logger.Info("craft skills registered (inactive, no credentials)")
	}

	// Google Workspace skills — always register (capability-gated).
	var googleCrypto googleskill.CryptoProvider
	if encryptor != nil {
		googleCrypto = encryptor
	}
	googleClient := googleskill.NewClientWithOptions(googleskill.ClientOptions{
		ClientID:        cfg.Skills.Google.ClientID,
		ClientSecret:    cfg.Skills.Google.ClientSecret,
		RedirectURL:     cfg.Skills.Google.RedirectURL,
		Store:           store,
		Crypto:          googleCrypto,
		Logger:          logger,
		CredentialsFile: cfg.Skills.Google.CredentialsFile,
		Scopes:          googleskill.ParseScopesConfig(cfg.Skills.Google.Scopes),
	})
	googleManifest, err := googleskill.LoadManifest()
	if err != nil {
		logger.Warn("failed to load google manifest", zap.Error(err))
	}
	googleMailSkill := googleskill.NewMail(googleClient)
	googleMailSkill.SetReloader(registry, cfgStore)
	registry.RegisterWithManifest(googleMailSkill, googleManifest)
	registry.RegisterInGroup(googleskill.NewCalendar(googleClient, store), "google_workspace")
	registry.RegisterInGroup(googleskill.NewContacts(googleClient), "google_workspace")
	registry.RegisterInGroup(googleskill.NewTasks(googleClient), "google_workspace")
	googleAuthSkill := googleskill.NewAuthSkill(googleClient, cfgStore)
	googleAuthSkill.SetDataDir(paths.DataDir)
	registry.RegisterInGroup(googleAuthSkill, "google_workspace")
	googleActive := (cfg.Skills.Google.ClientID != "" && cfg.Skills.Google.ClientSecret != "") ||
		cfg.Skills.Google.CredentialsFile != "" ||
		os.Getenv("IULITA_GOOGLE_TOKEN") != "" ||
		os.Getenv("IULITA_GOOGLE_CREDENTIALS_FILE") != ""
	if googleActive {
		caps = append(caps, "google")
		registry.SetCapabilities(caps)
		logger.Info("google workspace skills registered (active)")
	} else {
		logger.Info("google workspace skills registered (inactive, no credentials)")
	}
	// Google client is always created (even with empty credentials) so it's safe to assign.
	// Skills are capability-gated; dashboard OAuth endpoints check credentials at call time.
	var dashboardGoogleClient dashboard.GoogleOAuthClient = googleClient

	// Todoist skill — API token-based task management.
	todoistClient := todoist.NewClient(cfg.Skills.Todoist.APIToken, httpClient, logger)
	todoistManifest, err := todoist.LoadManifest()
	if err != nil {
		logger.Warn("failed to load todoist manifest", zap.Error(err))
	}
	if cfg.Skills.Todoist.SystemPrompt != "" && todoistManifest != nil {
		todoistManifest.SystemPrompt = cfg.Skills.Todoist.SystemPrompt
	}
	todoistSkill := todoist.NewSkill(todoistClient, logger)
	todoistSkill.SetReloader(registry, cfgStore)
	registry.RegisterWithManifest(todoistSkill, todoistManifest)
	todoistActive := cfg.Skills.Todoist.APIToken != "" || os.Getenv("IULITA_TODOIST_TOKEN") != ""
	if todoistActive {
		if envToken := os.Getenv("IULITA_TODOIST_TOKEN"); envToken != "" && cfg.Skills.Todoist.APIToken == "" {
			todoistClient.UpdateToken(envToken)
		}
		caps = append(caps, "todoist")
		registry.SetCapabilities(caps)
		logger.Info("todoist skill registered (active)")
	} else {
		logger.Info("todoist skill registered (inactive, no API token)")
	}

	// Unified tasks meta-skill — aggregates Todoist, Google Tasks, Craft Tasks.
	tasksManifest, err := tasksskill.LoadManifest()
	if err != nil {
		logger.Warn("failed to load tasks manifest", zap.Error(err))
	}
	unifiedTasks := tasksskill.NewSkill(registry, logger)
	unifiedTasks.RegisterProvider("todoist", "todoist", todoistSkill)
	unifiedTasks.RegisterProvider("google_tasks", "google", googleskill.NewTasks(googleClient))
	unifiedTasks.RegisterProvider("craft_tasks", "craft", craft.NewTasks(craftClient))
	registry.RegisterWithManifest(unifiedTasks, tasksManifest)
	logger.Info("unified tasks meta-skill registered")

	// Delegate skill — send subtasks to secondary LLM providers.
	delegateProviders := make(map[string]llm.Provider)
	defaultDelegate := ""
	if ollamaProvider != nil {
		delegateProviders["ollama"] = ollamaProvider
		defaultDelegate = "ollama"
	}
	if cfg.OpenAI.APIKey != "" && cfg.OpenAI.Model != "" {
		openaiMaxTokens := cfg.OpenAI.MaxTokens
		if openaiMaxTokens <= 0 {
			openaiMaxTokens = 4096
		}
		delegateProviders["openai"] = openaillm.New(cfg.OpenAI.APIKey, cfg.OpenAI.Model, openaiMaxTokens, cfg.OpenAI.BaseURL, httpClient)
		if defaultDelegate == "" {
			defaultDelegate = "openai"
		}
	}
	if len(delegateProviders) > 0 {
		delegateManifest, err := delegate.LoadManifest()
		if err != nil {
			logger.Warn("failed to load delegate manifest", zap.Error(err))
		}
		registry.RegisterWithManifest(delegate.New(delegateProviders, defaultDelegate), delegateManifest)
		logger.Info("delegate skill registered", zap.String("default", defaultDelegate))
	}

	// Orchestrate skill — multi-agent parallel task execution.
	orchestrateManifest, err := orchestrate.LoadManifest()
	if err != nil {
		logger.Warn("failed to load orchestrate manifest", zap.Error(err))
	}
	orchestrateSkill := orchestrate.New(llmProvider, registry, nil, logger)
	registry.RegisterWithManifest(orchestrateSkill, orchestrateManifest)
	logger.Info("orchestrate skill registered")

	// PDF reader skill.
	pdfManifest, err := pdfreader.LoadManifest()
	if err != nil {
		logger.Warn("failed to load pdfreader manifest", zap.Error(err))
	}
	registry.RegisterWithManifest(pdfreader.New(), pdfManifest)

	// Token usage statistics skill.
	tokenUsageManifest, err := tokenusage.LoadManifest()
	if err != nil {
		logger.Warn("failed to load token_usage manifest", zap.Error(err))
	}
	registry.RegisterWithManifest(tokenusage.New(store), tokenUsageManifest)

	logger.Info("skills registry ready", zap.Int("registered", len(registry.EnabledSkills())))

	// External text skills (from skills/ directory).
	if cfg.Skills.Dir != "" {
		extManifests, err := skill.LoadExternalManifests(cfg.Skills.Dir)
		if err != nil {
			logger.Fatal("failed to load text skills", zap.Error(err))
		}
		for _, m := range extManifests {
			registry.AddManifest(m)
		}
		if len(extManifests) > 0 {
			names := make([]string, len(extManifests))
			for i, m := range extManifests {
				names[i] = m.Name
			}
			logger.Info("loaded text skills", zap.Strings("skills", names))
		}
	}

	// External skills from marketplace (ClawhHub, URL, local).
	var extMgr *skillmgr.Manager
	if cfg.Skills.External.Enabled {
		if err := os.MkdirAll(cfg.Skills.External.Dir, 0755); err != nil {
			logger.Warn("failed to create external skills dir", zap.Error(err))
		}
		extCaps := skillmgr.RuntimeCaps{
			ShellExecEnabled:  cfg.Skills.ShellExec.Enabled && len(cfg.Skills.ShellExec.AllowedBins) > 0,
			WebfetchAvailable: true,                                              // webfetch is always registered unconditionally
			HTTPClient:        web.NewSafeHTTPClient(15*time.Second, httpClient), // SSRF-safe client for proxy skills
		}
		extMgr = skillmgr.NewManager(store, registry, cfg.Skills.External, extCaps, logger)
		extMgr.RegisterSource(skillmgr.NewClawhHubSource("", web.NewSafeHTTPClient(30*time.Second, httpClient), logger))
		extMgr.RegisterSource(skillmgr.NewURLSource(web.NewSafeHTTPClient(60*time.Second, httpClient)))
		extMgr.RegisterSource(skillmgr.NewLocalSource())
		if cfg.Skills.External.AllowDocker {
			extMgr.RegisterExecutor(skillmgr.NewDockerExecutor(cfg.Skills.External.Docker))
		}
		if cfg.Skills.External.AllowWASM {
			wasmExec := skillmgr.NewWASMExecutor(ctx)
			extMgr.RegisterExecutor(wasmExec)
			defer wasmExec.Close(ctx)
		}
		if err := extMgr.LoadAll(ctx); err != nil {
			logger.Warn("failed to load external skills", zap.Error(err))
		}
	}

	if extMgr != nil {
		registry.Register(skillinfo.NewWithExternalManager(registry, cfgStore, extMgr))
	} else {
		registry.Register(skillinfo.New(registry, cfgStore))
	}

	// Register skill config keys dynamically (skills declare their own keys in SKILL.md).
	skillKeys := registry.ConfigKeys()
	cfgStore.RegisterKeys(skillKeys)
	if len(skillKeys) > 0 {
		logger.Info("registered skill config keys", zap.Int("count", len(skillKeys)))
	}
	// Register secret keys so Store auto-encrypts and rejects placeholders.
	cfgStore.SetSecretKeys(registry.SecretKeys())

	// Assistant
	asst := assistant.New(llmProvider, store, registry, cfg.App.SystemPrompt, cfg.App.DefaultTimezone, cfg.Claude.ContextWindow, logger)
	asst.SetModelInfo(cfg.Claude.Model, "claude")

	// Event bus for decoupled event handling.
	bus := eventbus.New(logger)
	asst.SetEventBus(bus)

	// Wire config store to event bus for hot-reload.
	cfgStore.SetPublisher(&eventbus.ConfigChangeAdapter{Bus: bus})
	registerConfigReload(bus, cfgStore, asst, store, registry, rawProvider, extMgr, logger)

	// Replay DB-stored config overrides so skills pick up tokens saved via dashboard.
	cfgStore.ReplayOverrides(ctx)

	// Diagnostic: log capabilities and enabled skills after replay.
	{
		caps := registry.Capabilities()
		var skillNames []string
		for _, s := range registry.EnabledSkills() {
			skillNames = append(skillNames, s.Name())
		}
		logger.Info("post-replay state",
			zap.Strings("capabilities", caps),
			zap.Strings("enabled_skills", skillNames),
			zap.Int("skill_count", len(skillNames)),
		)
	}

	// Wire embedding provider to assistant for hybrid search.
	if embedder != nil && cfg.Skills.Memory.VectorWeight > 0 {
		asst.SetEmbedding(embedder, cfg.Skills.Memory.VectorWeight)
		logger.Info("hybrid search enabled", zap.Float64("vector_weight", cfg.Skills.Memory.VectorWeight))
	}

	// Wire auto-embedding into store (facts/insights auto-embed on save).
	if embedder != nil {
		store.SetEmbedFunc(embedder.Embed)

		// Backfill embeddings for existing facts/insights without vectors (tracked for shutdown).
		wgBackground.Add(1)
		go func() {
			defer wgBackground.Done()
			backfillEmbeddings(context.WithoutCancel(ctx), store, embedder, logger)
		}()
	}

	// Configure link enrichment if enabled.
	if cfg.App.AutoLinkSummary {
		maxLinks := cfg.App.MaxLinks
		if maxLinks <= 0 {
			maxLinks = 3
		}
		asst.SetLinkEnrichment(maxLinks)
		logger.Info("auto link summary enabled", zap.Int("max_links", maxLinks))
	}

	// Configure memory trigger keywords for forcing the remember tool.
	if len(cfg.Skills.Memory.Triggers) > 0 {
		asst.SetMemoryTriggers(cfg.Skills.Memory.Triggers)
		logger.Info("memory triggers configured", zap.Strings("triggers", cfg.Skills.Memory.Triggers))
	}

	// Configure request timeout.
	if cfg.Claude.RequestTimeout != "" {
		if d, err := time.ParseDuration(cfg.Claude.RequestTimeout); err == nil {
			asst.SetRequestTimeout(d)
			logger.Info("custom request timeout", zap.Duration("timeout", d))
		}
	}

	// Configure extended thinking if set.
	switch strings.ToLower(cfg.Claude.Thinking) {
	case "low":
		asst.SetThinkingBudget(1024)
	case "medium":
		asst.SetThinkingBudget(4096)
	case "high":
		asst.SetThinkingBudget(16000)
	}

	// Channel manager — manages lifecycle of all communication channel instances.
	var debounceWindow time.Duration
	if cfg.Telegram.DebounceWindow != "" {
		if d, err := time.ParseDuration(cfg.Telegram.DebounceWindow); err == nil {
			debounceWindow = d
		}
	}
	var configRateLimit int
	var configRateWindow time.Duration
	if cfg.Telegram.RateLimit > 0 {
		configRateLimit = cfg.Telegram.RateLimit
		configRateWindow = time.Minute
		if cfg.Telegram.RateWindow != "" {
			if d, err := time.ParseDuration(cfg.Telegram.RateWindow); err == nil {
				configRateWindow = d
			}
		}
		logger.Info("rate limiting enabled",
			zap.Int("rate", configRateLimit), zap.Duration("window", configRateWindow))
	}

	// Voice message transcription.
	var transcriber telegram.TranscriptionProvider
	if cfg.Transcription.Provider == "openai" && cfg.Transcription.APIKey != "" {
		transcriber = transcription.NewOpenAI(cfg.Transcription.APIKey, cfg.Transcription.Model, httpClient)
		logger.Info("voice transcription enabled", zap.String("provider", "openai"))
	}

	mgr := channelmgr.New(channelmgr.Config{
		Store:            store,
		CfgStore:         cfgStore,
		HTTPClient:       tgHTTPClient,
		UserResolver:     userResolver,
		ClearFn:          store.ClearHistory,
		ConfigToken:      effectiveTelegramToken(cfgStore, cfg.Telegram.Token),
		ConfigAllowedIDs: cfg.Telegram.AllowedIDs,
		ConfigDebounce:   debounceWindow,
		ConfigRateLimit:  configRateLimit,
		ConfigRateWindow: configRateWindow,
		Transcriber:      transcriber,
		Logger:           logger,
	})

	// Register event bus subscribers (after tg is available).
	eventbus.RegisterAuditSubscriber(bus, store, logger)
	eventbus.RegisterConfigAuditSubscriber(bus, store, logger)
	// Pass costTracker as explicit nil interface when cost tracking is disabled,
	// to avoid the nil-pointer-in-non-nil-interface trap.
	var usageCostCalc eventbus.UsageCostCalculator
	if costTracker != nil {
		usageCostCalc = costTracker
	}
	eventbus.RegisterUsageSubscriber(bus, store, usageCostCalc, logger)
	eventbus.RegisterFailureAlertSubscriber(bus, mgr, 3, logger)

	// Telegram token hot-reload subscriber — restarts config-sourced Telegram on token change.
	bus.Subscribe(eventbus.ConfigChanged, func(_ context.Context, evt eventbus.Event) error {
		p, ok := evt.Payload.(eventbus.ConfigChangedPayload)
		if !ok || p.Key != "telegram.token" {
			return nil
		}
		token, _ := cfgStore.GetEffective("telegram.token")
		mgr.UpdateConfigToken(token)
		if token != "" {
			logger.Info("hot-reloaded telegram.token, restarting config-sourced channel")
		} else {
			logger.Info("telegram.token cleared, stopping config-sourced channel")
		}
		return nil
	})

	// Prometheus metrics subscriber.
	if cfg.Metrics.Enabled {
		m := metrics.New()
		m.RegisterSubscribers(bus)
		logger.Info("prometheus metrics enabled")
	}

	// Cost limit check subscriber — warn when daily limit exceeded (in-memory tracker).
	if costTracker != nil {
		bus.Subscribe(eventbus.LLMUsage, func(_ context.Context, evt eventbus.Event) error {
			p, ok := evt.Payload.(eventbus.LLMUsagePayload)
			if !ok {
				return nil
			}
			exceeded, current := costTracker.Track(p.Model, llm.Usage{
				InputTokens:              p.InputTokens,
				OutputTokens:             p.OutputTokens,
				CacheReadInputTokens:     p.CacheReadInputTokens,
				CacheCreationInputTokens: p.CacheCreationInputTokens,
			})
			if exceeded {
				logger.Warn("daily cost limit exceeded", zap.Float64("cost_usd", current), zap.Float64("limit", cfg.Cost.DailyLimitUSD))
			}
			return nil
		})
	}

	// Push notification subscriber — send alerts on task failures.
	var notifier notify.Notifier
	switch strings.ToLower(cfg.Notify.Provider) {
	case "pushover":
		if cfg.Notify.PushoverToken != "" && cfg.Notify.PushoverUserKey != "" {
			notifier = notify.NewPushover(cfg.Notify.PushoverToken, cfg.Notify.PushoverUserKey)
			logger.Info("pushover notifications enabled")
		}
	case "ntfy":
		if cfg.Notify.NtfyURL != "" {
			notifier = notify.NewNtfy(cfg.Notify.NtfyURL, cfg.Notify.NtfyToken)
			logger.Info("ntfy notifications enabled")
		}
	}
	if notifier != nil {
		bus.SubscribeAsync(eventbus.TaskFailed, func(ctx context.Context, evt eventbus.Event) error {
			p, ok := evt.Payload.(eventbus.TaskFailedPayload)
			if !ok {
				return nil
			}
			return notifier.Send(ctx, "iulita: Task Failed", fmt.Sprintf("Task %s failed: %s", p.TaskType, p.Error))
		})
	}

	// Action rate limiter is available for use by the assistant/skills.
	_ = actionLimiter // available for future use in middleware

	// Configure streaming.
	if cfg.Claude.Streaming {
		asst.SetStreaming(mgr)
		logger.Info("streaming responses enabled")
	}

	// Configure real-time status notifications for web/console chat.
	asst.SetStatusNotifier(mgr)

	// Wire deferred dependencies for orchestrate skill.
	orchestrateSkill.SetNotifier(mgr)
	orchestrateSkill.SetEventBus(bus)

	// Attach sender for approval confirmation prompts.
	asst.SetMessageSender(mgr)

	// Attach interactive prompt support for skills (weather, etc.).
	asst.SetPrompterFactory(mgr)

	// Set console user ID and dependencies for /status and /compact.
	if consoleMode {
		if users, err := store.ListUsers(ctx); err == nil {
			for _, u := range users {
				if u.Role == domain.RoleAdmin {
					mgr.SetConsoleUserID(u.ID)
					break
				}
			}
		}

		mgr.SetConsoleStatusProvider(&consolech.StatusProvider{
			EnabledSkills: func() int { return len(registry.EnabledSkills()) },
			TotalSkills:   func() int { return len(registry.AllSkills()) },
			DailyCost: func() float64 {
				if costTracker != nil {
					return costTracker.DailyCost()
				}
				return 0
			},
			SessionStats: asst.SessionStats,
		})
		mgr.SetConsoleCompactFunc(func(ctx context.Context, chatID string) (int, error) {
			return asst.CompressNow(ctx, chatID)
		})
		// When TUI exits, trigger application shutdown (avoids double Ctrl+C).
		mgr.SetConsoleOnExit(cancel)
	}

	// Register chat commands on each channel instance as it starts.
	mgr.SetCommandRegistrar(func(tg *telegram.Channel) {
		tg.RegisterCommand("/usage", "Show token usage for this session", func(ctx context.Context, chatID string) string {
			in, out, reqs := asst.SessionStats()
			return fmt.Sprintf("Session usage:\nRequests: %d\nInput tokens: %d\nOutput tokens: %d\nTotal tokens: %d",
				reqs, in, out, in+out)
		})
		tg.RegisterCommand("/status", "Show memory stats and counts", func(ctx context.Context, chatID string) string {
			msgs, _ := store.CountMessages(ctx, chatID)
			insights, _ := store.CountInsights(ctx, chatID)
			reminders, _ := store.ListReminders(ctx, chatID)
			taskCounts, _ := store.CountTasksByStatus(ctx)
			var pending int
			for _, v := range taskCounts {
				pending += v
			}
			return fmt.Sprintf("Status:\nMessages: %d\nInsights: %d\nReminders: %d\nTasks: %d",
				msgs, insights, len(reminders), pending)
		})
		tg.RegisterCommand("/help", "Show available commands", func(_ context.Context, _ string) string {
			var names []string
			for _, s := range registry.EnabledSkills() {
				names = append(names, s.Name())
			}
			return "Available commands:\n/clear — Clear conversation history\n/usage — Show token usage\n/status — Show memory stats\n/version — Show bot version\n/help — Show this message\n\nSkills: " + strings.Join(names, ", ")
		})
		tg.RegisterCommand("/version", "Show bot version", func(_ context.Context, _ string) string {
			return "iulita " + version.String()
		})
	})

	// --- Bookmark service for "remember" button ---
	bookmarkSvc := bookmark.New(store, logger)
	mgr.SetBookmarkService(bookmarkSvc)

	// Background services tracked by WaitGroup for graceful shutdown.
	var wg sync.WaitGroup

	// --- Unified Task Scheduler ---
	var taskScheduler *scheduler.Scheduler

	pollInterval := 30 * time.Second
	if cfg.Scheduler.PollInterval != "" {
		if d, err := time.ParseDuration(cfg.Scheduler.PollInterval); err == nil {
			pollInterval = d
		}
	}

	taskScheduler = scheduler.NewScheduler(store, scheduler.SchedulerConfig{
		PollInterval: pollInterval,
	}, logger)

	// Register job definitions.
	// Todo sync — hourly background sync from external task providers.
	todoProviders := []dashboard.TodoProvider{
		dashboard.NewTodoistProvider(todoistClient, todoistClient, logger),
	}
	todoSyncHandler := dashboard.NewTodoSyncHandler(store, todoProviders, registrySkillChecker{registry}, logger)
	taskScheduler.RegisterJob(scheduler.JobDefinition{
		Name:     "todo_sync",
		CronExpr: "0 * * * *", // every hour
		Enabled:  true,
		CreateTasks: func(ctx context.Context) []domain.Task {
			return []domain.Task{{
				Type:           "todo.sync",
				Payload:        "{}",
				Capabilities:   "storage",
				MaxAttempts:    2,
				ScheduledAt:    time.Now(),
				DeleteAfterRun: false,
			}}
		},
	})

	taskScheduler.RegisterJob(handlers.ReminderJob(store, logger))
	taskScheduler.RegisterJob(handlers.InsightJob(store, cfg.Skills.Insights, logger))
	taskScheduler.RegisterJob(handlers.InsightCleanupJob(cfg.Skills.Insights))
	taskScheduler.RegisterJob(handlers.TechFactJob(store, cfg.TechFacts, logger))
	taskScheduler.RegisterJob(handlers.HeartbeatJob(cfg.Heartbeat))
	taskScheduler.RegisterJob(handlers.AgentJobsJob(store, logger))

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := taskScheduler.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("task scheduler error", zap.Error(err))
		}
	}()

	// --- Local Worker ---
	workerCaps := []string{"storage", "llm", "telegram"}
	concurrency := cfg.Scheduler.Concurrency
	if concurrency <= 0 {
		concurrency = 2
	}

	worker := scheduler.NewWorker(store, scheduler.WorkerConfig{
		Capabilities: workerCaps,
		Concurrency:  concurrency,
		PollInterval: 5 * time.Second,
	}, logger)
	worker.SetEventBus(bus)

	// Register task handlers.
	worker.Register(handlers.NewReminderFireHandler(store, mgr))

	insightProvider := resolveJobProvider(cfg.Skills.Insights.Model, llmProvider, cfg.Ollama, httpClient, logger, "insight")
	insightHandler := handlers.NewInsightGenerateHandler(store, insightProvider, cfg.Skills.Insights, logger)
	if cfg.Skills.Insights.Delivery {
		insightHandler.SetSender(mgr)
	}
	worker.Register(insightHandler)

	worker.Register(handlers.NewInsightCleanupHandler(store))

	techfactProvider := resolveJobProvider(cfg.TechFacts.Model, llmProvider, cfg.Ollama, httpClient, logger, "techfact")
	techFactHandler := handlers.NewTechFactAnalyzeHandler(store, techfactProvider, logger)
	if cfg.TechFacts.Delivery {
		techFactHandler.SetSender(mgr)
	}
	worker.Register(techFactHandler)

	// Agent job handler — user-defined scheduled LLM tasks.
	{
		agentJobProvider := resolveJobProvider("", llmProvider, cfg.Ollama, httpClient, logger, "agent_job")
		worker.Register(handlers.NewAgentJobHandler(store, agentJobProvider, mgr, logger))
	}

	if cfg.Heartbeat.Enabled {
		heartbeatProvider := llm.Provider(llmProvider)
		if cfg.Ollama.URL != "" && cfg.Ollama.Model != "" {
			heartbeatProvider = ollama.New(cfg.Ollama.URL, cfg.Ollama.Model, httpClient)
			logger.Info("heartbeat using ollama provider", zap.String("model", cfg.Ollama.Model))
		}
		worker.Register(handlers.NewHeartbeatHandler(store, heartbeatProvider, mgr, logger))
	}

	// Bookmark refinement handler — LLM summarization of bookmarked facts.
	{
		refineProvider := resolveJobProvider("", llmProvider, cfg.Ollama, httpClient, logger, "bookmark_refine")
		worker.Register(handlers.NewRefineBookmarkHandler(store, refineProvider, logger))
	}

	// Register todo sync task handler.
	worker.Register(todoSyncHandler)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := worker.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("worker error", zap.Error(err))
		}
	}()

	// Assistant priority message queue loop (processes steer/followUp injections).
	wg.Add(1)
	go func() {
		defer wg.Done()
		asst.Run(ctx)
	}()

	// WebSocket hub for real-time dashboard updates.
	wsHub := dashboard.NewWSHub(logger)
	wsHub.RegisterEventSubscribers(bus)

	// Dashboard server (optional)
	if cfg.Server.Enabled {
		staticFS, err := ui.DistFS()
		if err != nil {
			logger.Fatal("failed to load dashboard UI", zap.Error(err))
		}
		var dashSkillMgr dashboard.ExternalSkillManager
		if extMgr != nil {
			dashSkillMgr = &extSkillMgrAdapter{mgr: extMgr}
		}
		dashSrv := dashboard.New(dashboard.Config{
			Address:        cfg.Server.Address,
			Store:          store,
			Registry:       registry,
			StaticFS:       staticFS,
			Logger:         logger,
			TaskScheduler:  taskScheduler,
			WorkerToken:    cfg.Scheduler.WorkerToken,
			ConfigStore:    cfgStore,
			AuthService:    authService,
			ChannelManager: mgr,
			WSHub:          wsHub,
			WebChat:        mgr,
			GoogleClient:   dashboardGoogleClient,
			SkillManager:   dashSkillMgr,
			TodoProviders:  todoProviders,
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("dashboard server starting", zap.String("address", cfg.Server.Address))
			if err := dashSrv.Start(ctx); err != nil && ctx.Err() == nil {
				logger.Error("dashboard server error", zap.Error(err))
			}
		}()
	}

	logger.Info("starting iulita", zap.String("version", version.String()))
	if err := mgr.StartAll(ctx, asst.HandleMessage); err != nil {
		logger.Fatal("failed to start channel manager", zap.Error(err))
	}
	<-ctx.Done()
	logger.Info("shutdown signal received, stopping services...")

	// Phase 1: Stop accepting new messages.
	logger.Info("shutdown: stopping channel instances")
	mgr.StopAll()

	// Phase 2: Wait for assistant background goroutines (tech analysis, insight reinforcement).
	logger.Info("shutdown: waiting for assistant background tasks")
	asst.Shutdown()

	// Phase 3: Wait for miscellaneous background goroutines (embedding backfill, etc.).
	logger.Info("shutdown: waiting for background goroutines")
	wgBackground.Wait()

	// Phase 4: Close embedding provider.
	if closer, ok := embedder.(interface{ Close() }); ok {
		closer.Close()
	}

	// Phase 5: Wait for async event handlers to finish.
	logger.Info("shutdown: waiting for event bus")
	bus.Shutdown()

	// Phase 6: Wait for scheduler, worker, and dashboard with timeout.
	logger.Info("shutdown: waiting for scheduler, worker, dashboard")
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Info("all background services stopped")
	case <-time.After(10 * time.Second):
		logger.Warn("shutdown timeout exceeded, forcing exit")
	}

	// Phase 7: Close storage last (after all components have stopped).
	if err := store.Close(); err != nil {
		logger.Error("failed to close storage", zap.Error(err))
	}

	logger.Info("iulita stopped")
}

// extSkillMgrAdapter adapts skillmgr.Manager to dashboard.ExternalSkillManager.
type extSkillMgrAdapter struct {
	mgr *skillmgr.Manager
}

func (a *extSkillMgrAdapter) ListInstalled(ctx context.Context) ([]domain.InstalledSkill, error) {
	return a.mgr.ListInstalled(ctx)
}
func (a *extSkillMgrAdapter) GetInstalled(ctx context.Context, slug string) (*domain.InstalledSkill, error) {
	return a.mgr.GetInstalled(ctx, slug)
}
func (a *extSkillMgrAdapter) Install(ctx context.Context, source, ref string) (*domain.InstalledSkill, []string, error) {
	return a.mgr.Install(ctx, source, ref)
}
func (a *extSkillMgrAdapter) Uninstall(ctx context.Context, slug string) error {
	return a.mgr.Uninstall(ctx, slug)
}
func (a *extSkillMgrAdapter) Enable(ctx context.Context, slug string) error {
	return a.mgr.Enable(ctx, slug)
}
func (a *extSkillMgrAdapter) Disable(ctx context.Context, slug string) error {
	return a.mgr.Disable(ctx, slug)
}
func (a *extSkillMgrAdapter) ResolveMarketplace(ctx context.Context, source, ref string) (*dashboard.ExternalSkillDetail, error) {
	r, err := a.mgr.ResolveMarketplace(ctx, source, ref)
	if err != nil {
		return nil, err
	}
	return &dashboard.ExternalSkillDetail{
		Slug:             r.Slug,
		Name:             r.Name,
		Version:          r.Version,
		Description:      r.Description,
		Author:           r.Author,
		OwnerDisplayName: r.OwnerDisplayName,
		Tags:             r.Tags,
		Source:           r.Source,
		SourceRef:        r.SourceRef,
		Downloads:        r.Downloads,
		Stars:            r.Stars,
		UpdatedAt:        r.UpdatedAt,
	}, nil
}

func (a *extSkillMgrAdapter) Search(ctx context.Context, source, query string, limit int) ([]dashboard.ExternalSkillResult, error) {
	refs, err := a.mgr.Search(ctx, source, query, limit)
	if err != nil {
		return nil, err
	}
	results := make([]dashboard.ExternalSkillResult, len(refs))
	for i, r := range refs {
		results[i] = dashboard.ExternalSkillResult{
			Slug:        r.Slug,
			Name:        r.Name,
			Version:     r.Version,
			Description: r.Description,
			Author:      r.Author,
			Tags:        r.Tags,
			Source:      r.Source,
			SourceRef:   r.SourceRef,
			Downloads:   r.Downloads,
			Stars:       r.Stars,
			UpdatedAt:   r.UpdatedAt,
		}
	}
	return results, nil
}

func buildLogger(cfg config.LogConfig, consoleMode bool, logFile string) (*zap.Logger, error) {
	level, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		level = zapcore.InfoLevel
	}

	encoding := cfg.Encoding
	if encoding == "" {
		encoding = "console"
	}

	outputPaths := []string{"stdout"}
	errorPaths := []string{"stderr"}
	if consoleMode {
		// In console mode, redirect logs to file to avoid interfering with TUI.
		outputPaths = []string{logFile}
		errorPaths = []string{logFile}
	}

	zapCfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Encoding:         encoding,
		OutputPaths:      outputPaths,
		ErrorOutputPaths: errorPaths,
		EncoderConfig:    zap.NewProductionEncoderConfig(),
	}

	if encoding == "console" {
		zapCfg.EncoderConfig = zap.NewDevelopmentEncoderConfig()
	}

	logger, err := zapCfg.Build()
	if err != nil {
		return nil, err
	}

	if consoleMode {
		// Redirect stderr and standard log to file so nothing leaks into
		// the TUI. This catches telegram-bot-api (log.Println), net/http,
		// panics, and runtime errors. We do NOT touch stdout/fd1 because
		// bubbletea needs it for rendering.
		if f, fErr := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); fErr == nil {
			log.SetOutput(f)
			os.Stderr = f
			// Redirect fd 2 at OS level so Go runtime panics and C libs
			// also write to the log file instead of the terminal.
			dupFd(int(f.Fd()), 2) //nolint:errcheck
		}
	}

	return logger, nil
}

// resolveJobProvider picks a provider for a background job based on the "model" config field.
// "ollama" → uses Ollama if configured, otherwise falls back to the default provider.
func resolveJobProvider(model string, defaultProvider llm.Provider, ollamaCfg config.OllamaConfig, httpClient *http.Client, logger *zap.Logger, jobName string) llm.Provider {
	if strings.EqualFold(model, "ollama") && ollamaCfg.URL != "" && ollamaCfg.Model != "" {
		logger.Info(jobName+" job using ollama provider", zap.String("model", ollamaCfg.Model))
		return ollama.New(ollamaCfg.URL, ollamaCfg.Model, httpClient)
	}
	return defaultProvider
}

// backfillEmbeddings generates embeddings for facts/insights that don't have them yet.
func backfillEmbeddings(ctx context.Context, store *sqlite.Store, embedder llm.EmbeddingProvider, logger *zap.Logger) {
	const batchSize = 32

	// Backfill facts.
	for {
		facts, err := store.FactsWithoutEmbeddings(ctx, batchSize)
		if err != nil || len(facts) == 0 {
			break
		}
		texts := make([]string, len(facts))
		for i, f := range facts {
			texts[i] = f.Content
		}
		vecs, err := embedder.Embed(ctx, texts)
		if err != nil {
			logger.Error("backfill: failed to embed facts", zap.Error(err))
			break
		}
		for i, f := range facts {
			if i < len(vecs) {
				if err := store.SaveFactVector(ctx, f.ID, vecs[i]); err != nil {
					logger.Debug("backfill: failed to save fact vector", zap.Int64("id", f.ID), zap.Error(err))
				}
			}
		}
		logger.Info("backfilled fact embeddings", zap.Int("count", len(facts)))
		if len(facts) < batchSize {
			break
		}
	}

	// Backfill insights.
	for {
		insights, err := store.InsightsWithoutEmbeddings(ctx, batchSize)
		if err != nil || len(insights) == 0 {
			break
		}
		texts := make([]string, len(insights))
		for i, ins := range insights {
			texts[i] = ins.Content
		}
		vecs, err := embedder.Embed(ctx, texts)
		if err != nil {
			logger.Error("backfill: failed to embed insights", zap.Error(err))
			break
		}
		for i, ins := range insights {
			if i < len(vecs) {
				if err := store.SaveInsightVector(ctx, ins.ID, vecs[i]); err != nil {
					logger.Debug("backfill: failed to save insight vector", zap.Int64("id", ins.ID), zap.Error(err))
				}
			}
		}
		logger.Info("backfilled insight embeddings", zap.Int("count", len(insights)))
		if len(insights) < batchSize {
			break
		}
	}
}

// registerConfigReload subscribes to config.changed events and applies runtime updates.
// Core keys have explicit handlers; skill keys are dispatched to the registry.
func registerConfigReload(bus *eventbus.Bus, cfgStore *config.Store, asst *assistant.Assistant, store *sqlite.Store, registry *skill.Registry, claudeProvider *claude.Provider, extMgr *skillmgr.Manager, logger *zap.Logger) {
	bus.Subscribe(eventbus.ConfigChanged, func(_ context.Context, evt eventbus.Event) error {
		p, ok := evt.Payload.(eventbus.ConfigChangedPayload)
		if !ok {
			return nil
		}

		switch p.Key {
		case "claude.thinking":
			if val, ok := cfgStore.Get("claude.thinking"); ok {
				switch strings.ToLower(val) {
				case "low":
					asst.SetThinkingBudget(1024)
				case "medium":
					asst.SetThinkingBudget(4096)
				case "high":
					asst.SetThinkingBudget(16000)
				default:
					asst.SetThinkingBudget(0)
				}
				logger.Info("hot-reloaded claude.thinking", zap.String("value", val))
			} else {
				base := cfgStore.Base()
				switch strings.ToLower(base.Claude.Thinking) {
				case "low":
					asst.SetThinkingBudget(1024)
				case "medium":
					asst.SetThinkingBudget(4096)
				case "high":
					asst.SetThinkingBudget(16000)
				default:
					asst.SetThinkingBudget(0)
				}
			}

		case "claude.model":
			if claudeProvider != nil {
				if val, ok := cfgStore.Get("claude.model"); ok && val != "" {
					claudeProvider.UpdateModel(val)
					logger.Info("hot-reloaded claude.model", zap.String("value", val))
				} else {
					claudeProvider.UpdateModel(cfgStore.Base().Claude.Model)
					logger.Info("claude.model reverted to default")
				}
			}

		case "claude.max_tokens":
			if claudeProvider != nil {
				if val, ok := cfgStore.Get("claude.max_tokens"); ok {
					if n, err := strconv.Atoi(val); err == nil && n > 0 {
						claudeProvider.UpdateMaxTokens(n)
						logger.Info("hot-reloaded claude.max_tokens", zap.Int("value", n))
					}
				} else {
					claudeProvider.UpdateMaxTokens(cfgStore.Base().Claude.MaxTokens)
					logger.Info("claude.max_tokens reverted to default")
				}
			}

		case "skills.memory.half_life_days":
			if val, ok := cfgStore.Get("skills.memory.half_life_days"); ok {
				if days, err := parseFloat(val); err == nil && days > 0 {
					store.SetHalfLifeDays(days)
					logger.Info("hot-reloaded memory.half_life_days", zap.Float64("value", days))
				}
			} else {
				store.SetHalfLifeDays(cfgStore.Base().Skills.Memory.HalfLifeDays)
			}

		case "skills.memory.mmr_lambda":
			if val, ok := cfgStore.Get("skills.memory.mmr_lambda"); ok {
				if lambda, err := parseFloat(val); err == nil {
					store.SetMMRLambda(lambda)
					logger.Info("hot-reloaded memory.mmr_lambda", zap.Float64("value", lambda))
				}
			} else {
				store.SetMMRLambda(cfgStore.Base().Skills.Memory.MMRLambda)
			}

		case "skills.external.allow_shell":
			if extMgr != nil {
				val, _ := cfgStore.Get(p.Key)
				v := strings.EqualFold(val, "true")
				extMgr.SetAllowShell(v)
				logger.Info("hot-reloaded skills.external.allow_shell", zap.Bool("value", v))
			}
		case "skills.external.allow_docker":
			if extMgr != nil {
				val, _ := cfgStore.Get(p.Key)
				v := strings.EqualFold(val, "true")
				extMgr.SetAllowDocker(v)
				logger.Info("hot-reloaded skills.external.allow_docker", zap.Bool("value", v))
			}
		case "skills.external.allow_wasm":
			if extMgr != nil {
				val, _ := cfgStore.Get(p.Key)
				v := strings.EqualFold(val, "true")
				extMgr.SetAllowWASM(v)
				logger.Info("hot-reloaded skills.external.allow_wasm", zap.Bool("value", v))
			}

		default:
			// Dispatch skill config changes to the registry.
			if strings.HasPrefix(p.Key, "skills.") {
				val, found := cfgStore.Get(p.Key)
				registry.DispatchConfigChanged(p.Key, val, !found)
				if found {
					logger.Info("hot-reloaded skill config", zap.String("key", p.Key))
				} else {
					logger.Info("skill config reverted to default", zap.String("key", p.Key))
				}
			} else {
				logger.Debug("config changed (no hot-reload handler)", zap.String("key", p.Key))
			}
		}

		return nil
	})
	logger.Info("config hot-reload subscriber registered")
}

// effectiveTelegramToken returns the DB-stored token if available, else the base config value.
// This ensures that tokens set via dashboard (stored encrypted in DB) are used at startup
// even when config.toml doesn't have the token (zero-config / Docker flow).
func effectiveTelegramToken(cfgStore *config.Store, baseToken string) string {
	if val, ok := cfgStore.GetEffective("telegram.token"); ok && val != "" {
		return val
	}
	return baseToken
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}

func runDoctor() {
	paths := config.ResolvePaths()
	configPath := ""
	if v := os.Getenv("IULITA_CONFIG"); v != "" {
		configPath = v
	}
	if configPath == "" {
		xdgConfig := paths.ConfigFile()
		if _, err := os.Stat(xdgConfig); err == nil {
			configPath = xdgConfig
		} else if _, err := os.Stat("config.toml"); err == nil {
			configPath = "config.toml"
		}
	}
	cfg, _, _, err := config.Load(configPath, paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	doc := doctor.New()
	doc.AddCheck(doctor.CheckSQLite(cfg.Storage.Path))
	doc.AddCheck(doctor.CheckTelegram(cfg.Telegram.Token))
	doc.AddCheck(doctor.CheckClaude(cfg.Claude.APIKey, cfg.Claude.Model))
	doc.AddCheck(doctor.CheckOllama(cfg.Ollama.URL))
	if cfg.Server.Enabled {
		doc.AddCheck(doctor.CheckDashboard(cfg.Server.Address))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := doc.RunAll(ctx)
	doc.PrintResults(os.Stdout, results)

	for _, r := range results {
		if r.Status == "FAIL" {
			os.Exit(1)
		}
	}
}

// bootstrapAdminUser creates the default admin user if no users exist yet,
// and ensures the configured Telegram allowed_ids are bound to the admin user.
func bootstrapAdminUser(ctx context.Context, store *sqlite.Store, telegramIDs []int64, logger *zap.Logger) error {
	users, err := store.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("listing users: %w", err)
	}

	var adminID string

	if len(users) == 0 {
		// First run — create admin user.
		hash, err := auth.HashPassword("admin")
		if err != nil {
			return fmt.Errorf("hashing default password: %w", err)
		}

		admin := &domain.User{
			ID:             uuid.Must(uuid.NewV7()).String(),
			Username:       "admin",
			PasswordHash:   hash,
			Role:           domain.RoleAdmin,
			DisplayName:    "Admin",
			Timezone:       "UTC",
			MustChangePass: true,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		if err := store.CreateUser(ctx, admin); err != nil {
			return fmt.Errorf("creating admin user: %w", err)
		}
		adminID = admin.ID
		logger.Info("bootstrapped default admin user (username: admin, password: admin — change on first login)")
	} else {
		// Find first admin user for binding.
		for _, u := range users {
			if u.Role == domain.RoleAdmin {
				adminID = u.ID
				break
			}
		}
	}

	if adminID == "" || len(telegramIDs) == 0 {
		return nil
	}

	// Ensure each allowed Telegram ID is bound to admin.
	for _, tgID := range telegramIDs {
		tgUserID := strconv.FormatInt(tgID, 10)
		existing, err := store.GetUserByChannel(ctx, "telegram", tgUserID)
		if err != nil {
			logger.Error("failed to check channel binding", zap.Int64("telegram_id", tgID), zap.Error(err))
			continue
		}
		if existing != nil {
			continue // already bound
		}
		ch := &domain.UserChannel{
			UserID:        adminID,
			ChannelType:   "telegram",
			ChannelUserID: tgUserID,
			Enabled:       true,
		}
		if err := store.BindChannel(ctx, ch); err != nil {
			logger.Error("failed to bind telegram ID to admin", zap.Int64("telegram_id", tgID), zap.Error(err))
		} else {
			logger.Info("bound telegram ID to admin user", zap.Int64("telegram_id", tgID))
		}
	}

	return nil
}

// bootstrapConfigChannelInstance ensures a config-sourced channel instance exists in the DB.
// If already present, it's a no-op. Config is empty (token stays in config.toml only).
func bootstrapConfigChannelInstance(ctx context.Context, store *sqlite.Store, id, channelType, name string, enabled bool, logger *zap.Logger) error {
	existing, err := store.GetChannelInstance(ctx, id)
	if err != nil {
		return fmt.Errorf("checking channel instance %s: %w", id, err)
	}
	if existing != nil {
		// Re-enable if explicitly requested (e.g. console mode) but previously disabled.
		if enabled && !existing.Enabled {
			existing.Enabled = true
			if err := store.UpdateChannelInstance(ctx, existing); err != nil {
				logger.Warn("failed to re-enable channel instance", zap.String("id", id), zap.Error(err))
			} else {
				logger.Info("re-enabled channel instance", zap.String("id", id))
			}
		}
		return nil
	}

	ci := &domain.ChannelInstance{
		ID:      id,
		Type:    channelType,
		Name:    name,
		Config:  "{}", // token is in config.toml, not stored here
		Source:  domain.ChannelSourceConfig,
		Enabled: enabled,
	}
	if err := store.CreateChannelInstance(ctx, ci); err != nil {
		return fmt.Errorf("creating channel instance %s: %w", id, err)
	}
	logger.Info("bootstrapped config channel instance", zap.String("id", id), zap.String("type", channelType))
	return nil
}

// disableChannelInstance disables a channel instance in the DB if it exists and is enabled.
// Used to prevent leftover console instances from starting in server mode.
func disableChannelInstance(ctx context.Context, store *sqlite.Store, id string, logger *zap.Logger) {
	existing, err := store.GetChannelInstance(ctx, id)
	if err != nil || existing == nil {
		return
	}
	if existing.Enabled {
		existing.Enabled = false
		if err := store.UpdateChannelInstance(ctx, existing); err != nil {
			logger.Warn("failed to disable channel instance", zap.String("id", id), zap.Error(err))
		} else {
			logger.Info("disabled channel instance for server mode", zap.String("id", id))
		}
	}
}

func buildTransport(proxyURL string, logger *zap.Logger) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()

	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			logger.Fatal("invalid proxy URL", zap.String("url", proxyURL), zap.Error(err))
		}
		transport.Proxy = http.ProxyURL(u)
		logger.Info("using proxy from config", zap.String("proxy", proxyURL))
	} else {
		logger.Info("proxy: using standard env vars (HTTP_PROXY/HTTPS_PROXY/NO_PROXY)")
	}

	return transport
}
