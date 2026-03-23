package dashboard

import (
	"context"
	"io/fs"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/scheduler"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
)

// ChannelLifecycle is the subset of channelmgr.Manager needed by the dashboard.
// Defined here to avoid an import cycle (channelmgr imports telegram, which imports channel).
type ChannelLifecycle interface {
	AddInstance(ctx context.Context, instance domain.ChannelInstance) error
	UpdateInstance(ctx context.Context, instance domain.ChannelInstance) error
	StopInstance(instanceID string)
	IsRunning(instanceID string) bool
}

// WebChatProvider returns Fiber handlers for the web chat WebSocket.
type WebChatProvider interface {
	FiberUpgradeCheck() fiber.Handler
	FiberHandler() fiber.Handler
}

// GoogleOAuthClient provides OAuth2 operations for Google account management.
// Defined as interface to avoid import cycle with skill/google.
type GoogleOAuthClient interface {
	AuthCodeURL(state string) string
	ExchangeCodeRaw(ctx context.Context, code string) (accessToken, refreshToken string, expiry time.Time, err error)
	EncryptToken(value string) (string, error)
}

// GoogleStatusProvider extends GoogleOAuthClient with credential status reporting.
// Optional — checked with type assertion at runtime.
type GoogleStatusProvider interface {
	GetCredentialStatus(ctx context.Context, userID string) map[string]any
}

// GoogleCredentialUploader extends GoogleOAuthClient with file upload support.
// Optional — checked with type assertion at runtime.
type GoogleCredentialUploader interface {
	UploadCredentials(data []byte, filename, dataDir string) (credType, destPath string, err error)
}

// ExternalSkillResult describes a skill search result from a marketplace.
// Defined here to avoid importing skillmgr into the dashboard package.
type ExternalSkillResult struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Author      string   `json:"author"`
	Tags        []string `json:"tags"`
	Source      string   `json:"source"`
	SourceRef   string   `json:"source_ref"`
	Downloads   int      `json:"downloads,omitempty"`
	Stars       int      `json:"stars,omitempty"`
	UpdatedAt   int64    `json:"updated_at,omitempty"`
}

// ExternalSkillDetail is the full metadata for a marketplace skill (not yet installed).
type ExternalSkillDetail struct {
	Slug             string   `json:"slug"`
	Name             string   `json:"name"`
	Version          string   `json:"version"`
	Description      string   `json:"description"`
	Author           string   `json:"author"`
	OwnerDisplayName string   `json:"owner_display_name,omitempty"`
	Tags             []string `json:"tags"`
	Source           string   `json:"source"`
	SourceRef        string   `json:"source_ref"`
	DownloadURL      string   `json:"download_url,omitempty"`
	Downloads        int      `json:"downloads,omitempty"`
	Stars            int      `json:"stars,omitempty"`
	UpdatedAt        int64    `json:"updated_at,omitempty"`
}

// ExternalSkillManager manages external skill lifecycle for the dashboard.
// Defined as interface to avoid import cycle with skillmgr.
type ExternalSkillManager interface {
	ListInstalled(ctx context.Context) ([]domain.InstalledSkill, error)
	GetInstalled(ctx context.Context, slug string) (*domain.InstalledSkill, error)
	Install(ctx context.Context, source, ref string) (*domain.InstalledSkill, []string, error)
	Uninstall(ctx context.Context, slug string) error
	Enable(ctx context.Context, slug string) error
	Disable(ctx context.Context, slug string) error
	Search(ctx context.Context, source, query string, limit int) ([]ExternalSkillResult, error)
	ResolveMarketplace(ctx context.Context, source, ref string) (*ExternalSkillDetail, error)
}

// CredentialManager manages the credential lifecycle for the dashboard API.
type CredentialManager interface {
	List() []credential.CredentialView
	ListFromDB(ctx context.Context, filter credential.CredentialFilter) ([]credential.CredentialView, error)
	GetByID(ctx context.Context, id int64) (*domain.Credential, error)
	Set(ctx context.Context, req credential.SetRequest) (*domain.Credential, error)
	Rotate(ctx context.Context, req credential.RotateRequest) error
	Delete(ctx context.Context, id int64, deletedBy string) error
	Bind(ctx context.Context, credentialID int64, consumerType, consumerID, createdBy string) error
	Unbind(ctx context.Context, credentialID int64, consumerType, consumerID, removedBy string) error
	ListBindings(ctx context.Context, credentialID int64) ([]domain.CredentialBinding, error)
	ListBindingsByConsumer(ctx context.Context, consumerType, consumerID string) ([]domain.CredentialBinding, error)
	ListCredentialAudit(ctx context.Context, credentialID int64, limit int) ([]domain.CredentialAudit, error)
	EncryptionEnabled() bool
}

// Config holds dependencies for the dashboard server.
type Config struct {
	Address           string
	Store             storage.Repository
	Registry          *skill.Registry
	StaticFS          fs.FS
	Logger            *zap.Logger
	TaskScheduler     *scheduler.Scheduler
	WorkerToken       string // auth token for remote worker API
	ConfigStore       *config.Store
	AuthService       *auth.Service        // nil = auth disabled (backward compat)
	ChannelManager    ChannelLifecycle     // nil = no runtime channel management
	WSHub             *WSHub               // nil = WebSocket disabled
	WebChat           WebChatProvider      // nil = web chat disabled
	GoogleClient      GoogleOAuthClient    // nil = Google OAuth disabled
	SkillManager      ExternalSkillManager // nil = external skills disabled
	TodoProviders     []TodoProvider       // external task providers (Todoist, etc.)
	CredentialManager CredentialManager    // nil = credential API disabled
	SetupMode         bool                 // true = web wizard only, no full app
}

// Server serves the dashboard API and embedded SPA.
type Server struct {
	app               *fiber.App
	address           string
	store             storage.Repository
	registry          *skill.Registry
	startedAt         time.Time
	logger            *zap.Logger
	taskScheduler     *scheduler.Scheduler
	workerToken       string
	configStore       *config.Store
	authService       *auth.Service
	channelManager    ChannelLifecycle
	googleClient      GoogleOAuthClient
	skillManager      ExternalSkillManager
	todoProviders     []TodoProvider
	credentialManager CredentialManager
	setupMode         bool
}

// New creates a new dashboard server.
func New(cfg Config) *Server {
	s := &Server{
		address:           cfg.Address,
		store:             cfg.Store,
		registry:          cfg.Registry,
		startedAt:         time.Now(),
		logger:            cfg.Logger,
		taskScheduler:     cfg.TaskScheduler,
		workerToken:       cfg.WorkerToken,
		configStore:       cfg.ConfigStore,
		authService:       cfg.AuthService,
		channelManager:    cfg.ChannelManager,
		googleClient:      cfg.GoogleClient,
		skillManager:      cfg.SkillManager,
		todoProviders:     cfg.TodoProviders,
		credentialManager: cfg.CredentialManager,
		setupMode:         cfg.SetupMode,
	}

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			s.logger.Error("fiber request error",
				zap.String("method", c.Method()),
				zap.String("path", c.Path()),
				zap.Int("status", code),
				zap.Error(err),
			)
			return c.Status(code).JSON(fiber.Map{"error": err.Error()})
		},
	})

	api := app.Group("/api")

	// Public auth endpoints (no JWT required).
	if s.authService != nil {
		authGroup := api.Group("/auth")
		authGroup.Post("/login", s.handleLogin)
		authGroup.Post("/refresh", s.handleRefresh)
	}

	// System info is public (health checks, version).
	api.Get("/system", s.handleSystem)

	// Protected routes: require JWT when auth is enabled.
	if s.authService != nil {
		api.Use(auth.FiberMiddleware(s.authService))
	}

	// Auth-protected endpoints available to all authenticated users.
	if s.authService != nil {
		api.Post("/auth/change-password", s.handleChangePassword)
		api.Get("/auth/me", s.handleMe)
		api.Patch("/auth/locale", s.handleSetLocale)
	}

	api.Get("/stats", s.handleStats)
	api.Get("/chats", s.handleChats)
	api.Get("/facts", s.handleFacts)
	api.Get("/insights", s.handleInsights)
	api.Get("/reminders", s.handleReminders)
	api.Get("/directives", s.handleDirectives)
	api.Get("/messages", s.handleMessages)
	api.Get("/skills", s.handleSkills)
	api.Put("/skills/:name/toggle", s.handleToggleSkill)
	api.Get("/skills/:name/config", s.handleGetSkillConfig)
	api.Put("/skills/:name/config/:key", s.handleSetSkillConfig)
	api.Get("/techfacts", s.handleTechFacts)

	// Usage stats API (admin only — exposes system-wide cost/token data).
	if s.authService != nil {
		usageGroup := api.Group("/usage", auth.AdminOnly())
		usageGroup.Get("/summary", s.handleUsageSummaryV2)
		usageGroup.Get("/daily", s.handleUsageByDay)
		usageGroup.Get("/by-model", s.handleUsageByModel)
	} else {
		api.Get("/usage/summary", s.handleUsageSummaryV2)
		api.Get("/usage/daily", s.handleUsageByDay)
		api.Get("/usage/by-model", s.handleUsageByModel)
	}
	api.Put("/facts/:id", s.handleUpdateFact)
	api.Delete("/facts/:id", s.handleDeleteFact)
	api.Get("/facts/search", s.handleSearchFacts)

	// Scheduler API
	api.Get("/schedulers", s.handleSchedulersStatus)
	api.Post("/schedulers/:name/trigger", s.handleTriggerJob)

	// Wizard API (admin only when auth is enabled).
	if s.configStore != nil {
		wizardGroup := api.Group("/wizard")
		if s.authService != nil {
			wizardGroup.Use(auth.AdminOnly())
		}
		wizardGroup.Get("/status", s.handleWizardStatus)
		wizardGroup.Get("/schema", s.handleWizardSchema)
		wizardGroup.Post("/complete", s.handleWizardComplete)
		wizardGroup.Post("/import-toml", s.handleImportTOML)
	}

	// Config API (admin only when auth is enabled)
	if s.configStore != nil {
		configGroup := api.Group("/config")
		if s.authService != nil {
			configGroup.Use(auth.AdminOnly())
		}
		configGroup.Get("/", s.handleListConfig)
		configGroup.Get("/schema", s.handleGetConfigSchema)
		configGroup.Get("/models/:provider", s.handleListModels)
		configGroup.Get("/debug", s.handleConfigDebug)
		configGroup.Get("/decrypted", s.handleListConfigDecrypted)
		configGroup.Put("/:key", s.handleSetConfig)
		configGroup.Delete("/:key", s.handleDeleteConfig)
	}

	// User management API (admin only)
	if s.authService != nil {
		users := api.Group("/users", auth.AdminOnly())
		users.Get("/", s.handleListUsers)
		users.Post("/", s.handleCreateUser)
		users.Get("/:id", s.handleGetUser)
		users.Put("/:id", s.handleUpdateUser)
		users.Delete("/:id", s.handleDeleteUser)
		users.Get("/:id/channels", s.handleListUserChannels)
		users.Post("/:id/channels", s.handleBindChannel)
		users.Delete("/:id/channels/:channel_id", s.handleUnbindChannel)
	}

	// Channel instances API (admin only) — communication bots/integrations
	if s.authService != nil {
		channels := api.Group("/channels", auth.AdminOnly())
		channels.Get("/", s.handleListChannelInstances)
		channels.Post("/", s.handleCreateChannelInstance)
		channels.Get("/:id", s.handleGetChannelInstance)
		channels.Put("/:id", s.handleUpdateChannelInstance)
		channels.Delete("/:id", s.handleDeleteChannelInstance)
		channels.Get("/:id/bindings", s.handleListChannelBindings)
		channels.Post("/:id/set-photo", s.handleSetBotPhoto)
	}

	// Agent jobs API (admin only — user-defined scheduled LLM tasks)
	if s.authService != nil {
		agentJobs := api.Group("/agent-jobs", auth.AdminOnly())
		agentJobs.Get("/", s.handleListAgentJobs)
		agentJobs.Post("/", s.handleCreateAgentJob)
		agentJobs.Get("/:id", s.handleGetAgentJob)
		agentJobs.Put("/:id", s.handleUpdateAgentJob)
		agentJobs.Delete("/:id", s.handleDeleteAgentJob)
	}

	// Credentials API (admin only)
	if s.authService != nil && s.credentialManager != nil {
		creds := api.Group("/credentials", auth.AdminOnly())
		creds.Get("/", s.handleListCredentials)
		creds.Post("/", s.handleCreateCredential)
		creds.Get("/:id", s.handleGetCredential)
		creds.Put("/:id", s.handleUpdateCredential)
		creds.Delete("/:id", s.handleDeleteCredential)
		creds.Post("/:id/rotate", s.handleRotateCredential)
		creds.Get("/:id/audit", s.handleListCredentialAudit)
		creds.Get("/:id/bindings", s.handleListCredentialBindings)
		creds.Post("/:id/bindings", s.handleBindCredential)
		creds.Delete("/:id/bindings/:consumer_type/:consumer_id", s.handleUnbindCredential)
		creds.Get("/by-consumer/:consumer_type/:consumer_id", s.handleListCredentialsByConsumer)
	}

	// External skills API (admin only)
	if s.authService != nil && s.skillManager != nil {
		extSkills := api.Group("/skills/external", auth.AdminOnly())
		extSkills.Get("/", s.handleListExternalSkills)
		extSkills.Get("/marketplace/:slug", s.handleGetMarketplaceSkillDetail)
		extSkills.Get("/:slug", s.handleGetExternalSkill)
		extSkills.Post("/install", s.handleInstallExternalSkill)
		extSkills.Post("/search", s.handleSearchExternalSkills)
		extSkills.Delete("/:slug", s.handleUninstallExternalSkill)
		extSkills.Put("/:slug/enable", s.handleEnableExternalSkill)
		extSkills.Put("/:slug/disable", s.handleDisableExternalSkill)
		extSkills.Post("/:slug/update", s.handleUpdateExternalSkill)
	}

	// Google OAuth & account management API (authenticated).
	{
		google := api.Group("/google")
		google.Get("/status", s.handleGoogleStatus)
		google.Post("/upload-credentials", s.handleUploadGoogleCredentials)
		if s.googleClient != nil {
			google.Get("/auth", s.handleGoogleAuth)
			google.Get("/callback", s.handleGoogleCallback)
			google.Get("/accounts", s.handleListGoogleAccounts)
			google.Delete("/accounts/:id", s.handleDeleteGoogleAccount)
			google.Put("/accounts/:id", s.handleUpdateGoogleAccount)
		}
	}

	// Todo items API (user-facing tasks)
	todos := api.Group("/todos")
	todos.Get("/providers", s.handleTodoProviders)
	todos.Get("/today", s.handleTodosToday)
	todos.Get("/overdue", s.handleTodosOverdue)
	todos.Get("/upcoming", s.handleTodosUpcoming)
	todos.Get("/all", s.handleTodosAll)
	todos.Get("/counts", s.handleTodoCounts)
	todos.Post("/", s.handleCreateTodo)
	todos.Post("/sync", s.handleTodoSync) // static routes before parametric
	todos.Post("/:id/complete", s.handleCompleteTodo)
	todos.Delete("/:id", s.handleDeleteTodo)
	// Admin-only: default provider is a system-wide setting.
	if s.authService != nil {
		todos.Put("/default-provider", auth.AdminOnly(), s.handleSetDefaultProvider)
	} else {
		todos.Put("/default-provider", s.handleSetDefaultProvider)
	}

	// Task queue API (for dashboard + remote workers)
	tasks := api.Group("/tasks")
	tasks.Get("/", s.handleListTasks)
	tasks.Get("/counts", s.handleTaskCounts)
	tasks.Post("/claim", s.workerAuth, s.handleClaimTask)
	tasks.Post("/:id/start", s.workerAuth, s.handleStartTask)
	tasks.Post("/:id/complete", s.workerAuth, s.handleCompleteTask)
	tasks.Post("/:id/fail", s.workerAuth, s.handleFailTask)

	// WebSocket endpoint (before SPA catch-all).
	if cfg.WSHub != nil {
		SetupWebSocket(app, cfg.WSHub)
	}

	// Web chat WebSocket endpoint.
	if cfg.WebChat != nil {
		app.Use("/ws/chat", cfg.WebChat.FiberUpgradeCheck())
		app.Get("/ws/chat", cfg.WebChat.FiberHandler())
	}

	// Serve embedded SPA with index.html fallback
	app.Use("/", filesystem.New(filesystem.Config{
		Root:         http.FS(cfg.StaticFS),
		NotFoundFile: "index.html",
	}))

	s.app = app
	return s
}

// workerAuth middleware checks the bearer token for remote worker endpoints.
func (s *Server) workerAuth(c *fiber.Ctx) error {
	if s.workerToken == "" {
		return c.Next() // no auth configured
	}
	auth := c.Get("Authorization")
	if auth != "Bearer "+s.workerToken {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	return c.Next()
}

// Start runs the server and blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.app.Listen(s.address)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return s.app.Shutdown()
	}
}
