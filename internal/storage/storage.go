package storage

import (
	"context"
	"errors"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// ErrNotFound is returned by storage when a record does not exist.
var ErrNotFound = errors.New("not found")

// Repository persists chat messages and reminders.
type Repository interface {
	// Users
	CreateUser(ctx context.Context, u *domain.User) error
	GetUser(ctx context.Context, id string) (*domain.User, error)
	GetUserByUsername(ctx context.Context, username string) (*domain.User, error)
	UpdateUser(ctx context.Context, u *domain.User) error
	ListUsers(ctx context.Context) ([]domain.User, error)
	DeleteUser(ctx context.Context, id string) error

	// Channel bindings (user ↔ channel identity)
	BindChannel(ctx context.Context, uc *domain.UserChannel) error
	UnbindChannel(ctx context.Context, id int64) error
	UpdateChannel(ctx context.Context, uc *domain.UserChannel) error
	GetUserByChannel(ctx context.Context, channelType, channelUserID string) (*domain.User, error)
	ListUserChannels(ctx context.Context, userID string) ([]domain.UserChannel, error)
	ListAllChannels(ctx context.Context) ([]domain.UserChannel, error)
	GetChannelInstanceIDByChat(ctx context.Context, chatID string) (string, error)

	// Channel instances (communication bots/integrations)
	CreateChannelInstance(ctx context.Context, ci *domain.ChannelInstance) error
	GetChannelInstance(ctx context.Context, id string) (*domain.ChannelInstance, error)
	ListChannelInstances(ctx context.Context) ([]domain.ChannelInstance, error)
	UpdateChannelInstance(ctx context.Context, ci *domain.ChannelInstance) error
	DeleteChannelInstance(ctx context.Context, id string) error

	SaveMessage(ctx context.Context, msg *domain.ChatMessage) error
	GetHistory(ctx context.Context, chatID string, limit int) ([]domain.ChatMessage, error)
	GetHistoryBefore(ctx context.Context, chatID string, beforeID int64, limit int) ([]domain.ChatMessage, error)
	ClearHistory(ctx context.Context, chatID string) error
	DeleteMessagesBefore(ctx context.Context, chatID string, beforeID int64) error

	CreateReminder(ctx context.Context, r *domain.Reminder) error
	ListReminders(ctx context.Context, chatID string) ([]domain.Reminder, error)
	DeleteReminder(ctx context.Context, id int64, chatID string) error
	GetDueReminders(ctx context.Context, now time.Time) ([]domain.Reminder, error)
	MarkReminderFired(ctx context.Context, id int64) error

	// Directives (user-scoped: chatID parameter is kept for backward compat, userID preferred)
	SaveDirective(ctx context.Context, d *domain.Directive) error
	GetDirective(ctx context.Context, chatID string) (*domain.Directive, error)
	GetDirectiveByUser(ctx context.Context, userID string) (*domain.Directive, error)
	DeleteDirective(ctx context.Context, chatID string) error

	// Facts (user-scoped: chatID parameter kept for backward compat, userID preferred)
	SaveFact(ctx context.Context, f *domain.Fact) error
	SearchFacts(ctx context.Context, chatID, query string, limit int) ([]domain.Fact, error)
	SearchFactsByUser(ctx context.Context, userID, query string, limit int) ([]domain.Fact, error)
	DeleteFact(ctx context.Context, id int64, chatID string) error
	DeleteFactByID(ctx context.Context, id int64) error
	UpdateFactContent(ctx context.Context, id int64, content string) error
	DeleteFactsByQuery(ctx context.Context, chatID, query string) (int, error)
	ReinforceFact(ctx context.Context, id int64) error
	GetRecentFacts(ctx context.Context, chatID string, limit int) ([]domain.Fact, error)
	GetRecentFactsByUser(ctx context.Context, userID string, limit int) ([]domain.Fact, error)
	GetAllFacts(ctx context.Context, chatID string) ([]domain.Fact, error)
	GetAllFactsByUser(ctx context.Context, userID string) ([]domain.Fact, error)

	// Embedding vectors
	CreateVectorTables(ctx context.Context) error
	SaveFactVector(ctx context.Context, factID int64, embedding []float32) error
	SaveInsightVector(ctx context.Context, insightID int64, embedding []float32) error
	SearchFactsHybrid(ctx context.Context, chatID string, query string, queryVec []float32, limit int, vectorWeight float64) ([]domain.Fact, error)
	SearchFactsHybridByUser(ctx context.Context, userID string, query string, queryVec []float32, limit int, vectorWeight float64) ([]domain.Fact, error)
	SearchInsightsHybrid(ctx context.Context, chatID string, query string, queryVec []float32, limit int, vectorWeight float64) ([]domain.Insight, error)
	SearchInsightsHybridByUser(ctx context.Context, userID string, query string, queryVec []float32, limit int, vectorWeight float64) ([]domain.Insight, error)
	FactsWithoutEmbeddings(ctx context.Context, limit int) ([]domain.Fact, error)
	InsightsWithoutEmbeddings(ctx context.Context, limit int) ([]domain.Insight, error)

	// Insights (user-scoped)
	SaveInsight(ctx context.Context, d *domain.Insight) error
	GetRecentInsights(ctx context.Context, chatID string, limit int) ([]domain.Insight, error)
	GetRecentInsightsByUser(ctx context.Context, userID string, limit int) ([]domain.Insight, error)
	DeleteExpiredInsights(ctx context.Context) error
	DeleteInsight(ctx context.Context, id int64, chatID string) error
	ReinforceInsight(ctx context.Context, id int64) error
	SearchInsights(ctx context.Context, chatID, query string, limit int) ([]domain.Insight, error)
	SearchInsightsByUser(ctx context.Context, userID, query string, limit int) ([]domain.Insight, error)
	CountInsights(ctx context.Context, chatID string) (int, error)
	CountInsightsByUser(ctx context.Context, userID string) (int, error)

	// Tech Facts (user-scoped)
	UpsertTechFact(ctx context.Context, f *domain.TechFact) error
	GetTechFacts(ctx context.Context, chatID string) ([]domain.TechFact, error)
	GetTechFactsByUser(ctx context.Context, userID string) ([]domain.TechFact, error)
	GetTechFactsByCategory(ctx context.Context, chatID, category string) ([]domain.TechFact, error)
	GetTechFactsByCategoryAndUser(ctx context.Context, userID, category string) ([]domain.TechFact, error)
	CountTechFacts(ctx context.Context, chatID string) (int, error)
	CountTechFactsByUser(ctx context.Context, userID string) (int, error)

	// Task queue
	CreateTask(ctx context.Context, t *domain.Task) error
	CreateTaskIfNotExists(ctx context.Context, t *domain.Task) (created bool, err error)
	ClaimTask(ctx context.Context, workerID string, capabilities []string) (*domain.Task, error)
	StartTask(ctx context.Context, taskID int64, workerID string) error
	CompleteTask(ctx context.Context, taskID int64, result string) error
	FailTask(ctx context.Context, taskID int64, errMsg string) error
	ListTasks(ctx context.Context, filter TaskFilter) ([]domain.Task, error)
	CountTasksByStatus(ctx context.Context) (map[domain.TaskStatus]int, error)
	CleanupStaleTasks(ctx context.Context, timeout time.Duration) (int, error)
	DeleteOldTasks(ctx context.Context, olderThan time.Duration) (int, error)

	// Scheduler state persistence
	GetSchedulerState(ctx context.Context, name string) (*domain.SchedulerState, error)
	SaveSchedulerState(ctx context.Context, state *domain.SchedulerState) error

	// Dashboard
	GetChatIDs(ctx context.Context) ([]string, error)
	GetUserIDs(ctx context.Context) ([]string, error) // list distinct user IDs with data
	CountMessages(ctx context.Context, chatID string) (int, error)
	ListAllReminders(ctx context.Context, chatID string) ([]domain.Reminder, error)

	// Audit log
	SaveAuditEntry(ctx context.Context, e *domain.AuditEntry) error

	// Config overrides
	GetConfigOverride(ctx context.Context, key string) (*domain.ConfigOverride, error)
	ListConfigOverrides(ctx context.Context) ([]domain.ConfigOverride, error)
	SaveConfigOverride(ctx context.Context, o *domain.ConfigOverride) error
	DeleteConfigOverride(ctx context.Context, key string) error

	// Usage stats
	IncrementUsage(ctx context.Context, chatID string, inputTokens, outputTokens int64) error
	GetUsageStats(ctx context.Context, chatID string) ([]domain.UsageRecord, error)
	UpsertUsage(ctx context.Context, rec UsageUpsert) error
	GetUsageSummary(ctx context.Context, filter UsageFilter) (*UsageSummary, error)
	GetUsageByDay(ctx context.Context, filter UsageFilter) ([]DailyUsage, error)
	GetUsageByModel(ctx context.Context, filter UsageFilter) ([]ModelUsage, error)

	// Embedding cache
	GetCachedEmbedding(ctx context.Context, contentHash string) ([]float32, error)
	SaveCachedEmbedding(ctx context.Context, contentHash string, embedding []float32) error
	EvictOldEmbeddings(ctx context.Context, maxEntries int) error

	// Response cache
	GetCachedResponse(ctx context.Context, hash string, maxAge time.Duration) (*CachedResponse, error)
	SaveCachedResponse(ctx context.Context, hash, model, response, usageJSON string) error
	EvictResponseCache(ctx context.Context, maxEntries int) error
	GetResponseCacheStats(ctx context.Context) (entries int, totalHits int, err error)

	// Cost tracking
	IncrementUsageWithCost(ctx context.Context, chatID string, inputTokens, outputTokens int64, costUSD float64) error
	GetDailyCost(ctx context.Context) (float64, error)
	GetDailyCostByChat(ctx context.Context, chatID string) (float64, error)

	// Agent jobs (user-defined scheduled LLM tasks)
	CreateAgentJob(ctx context.Context, j *domain.AgentJob) error
	GetAgentJob(ctx context.Context, id int64) (*domain.AgentJob, error)
	ListAgentJobs(ctx context.Context) ([]domain.AgentJob, error)
	UpdateAgentJob(ctx context.Context, j *domain.AgentJob) error
	DeleteAgentJob(ctx context.Context, id int64) error
	GetDueAgentJobs(ctx context.Context, now time.Time) ([]domain.AgentJob, error)
	UpdateAgentJobSchedule(ctx context.Context, id int64, lastRun, nextRun time.Time) error

	// Google accounts (OAuth2 tokens)
	SaveGoogleAccount(ctx context.Context, a *domain.GoogleAccount) error
	GetGoogleAccount(ctx context.Context, id int64) (*domain.GoogleAccount, error)
	GetGoogleAccountByEmail(ctx context.Context, userID, email string) (*domain.GoogleAccount, error)
	GetDefaultGoogleAccount(ctx context.Context, userID string) (*domain.GoogleAccount, error)
	ListGoogleAccounts(ctx context.Context, userID string) ([]domain.GoogleAccount, error)
	DeleteGoogleAccount(ctx context.Context, id int64) error
	UpdateGoogleAccountMeta(ctx context.Context, id int64, alias string, isDefault bool) error
	UpdateGoogleTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiry time.Time) error

	// External skills (installed from marketplace/URL)
	SaveInstalledSkill(ctx context.Context, s *domain.InstalledSkill) error
	GetInstalledSkill(ctx context.Context, slug string) (*domain.InstalledSkill, error)
	ListInstalledSkills(ctx context.Context) ([]domain.InstalledSkill, error)
	UpdateInstalledSkill(ctx context.Context, s *domain.InstalledSkill) error
	DeleteInstalledSkill(ctx context.Context, slug string) error

	// Todo items (user-facing tasks)
	CreateTodoItem(ctx context.Context, item *domain.TodoItem) error
	GetTodoItem(ctx context.Context, id int64, userID string) (*domain.TodoItem, error)
	UpdateTodoItem(ctx context.Context, item *domain.TodoItem) error
	DeleteTodoItem(ctx context.Context, id int64, userID string) error
	CompleteTodoItem(ctx context.Context, id int64, userID string) error
	ReopenTodoItem(ctx context.Context, id int64, userID string) error
	ListTodoItemsDueToday(ctx context.Context, userID string, now time.Time) ([]domain.TodoItem, error)
	ListTodoItemsOverdue(ctx context.Context, userID string, now time.Time) ([]domain.TodoItem, error)
	ListTodoItemsUpcoming(ctx context.Context, userID string, now time.Time, days int) ([]domain.TodoItem, error)
	ListTodoItemsAll(ctx context.Context, userID string, limit int) ([]domain.TodoItem, error)
	UpsertTodoItemByExternal(ctx context.Context, item *domain.TodoItem) error
	DeleteSyncedTodoItemsNotIn(ctx context.Context, userID, provider string, keepExternalIDs []string) (int, error)
	CountTodoItemsByProvider(ctx context.Context, userID string) (map[string]int, error)

	// Credentials
	SaveCredential(ctx context.Context, c *domain.Credential) error
	GetCredential(ctx context.Context, id int64) (*domain.Credential, error)
	GetCredentialByName(ctx context.Context, name string) (*domain.Credential, error)
	GetCredentialByNameAndOwner(ctx context.Context, name, ownerID string) (*domain.Credential, error)
	ListCredentials(ctx context.Context, filter CredentialFilter) ([]domain.Credential, error)
	UpdateCredential(ctx context.Context, c *domain.Credential) error
	DeleteCredential(ctx context.Context, id int64) error

	// Credential bindings
	SaveCredentialBinding(ctx context.Context, b *domain.CredentialBinding) error
	DeleteCredentialBinding(ctx context.Context, credentialID int64, consumerType, consumerID string) error
	ListCredentialBindings(ctx context.Context, credentialID int64) ([]domain.CredentialBinding, error)
	ListCredentialBindingsByConsumer(ctx context.Context, consumerType, consumerID string) ([]domain.CredentialBinding, error)

	// Credential audit
	SaveCredentialAudit(ctx context.Context, a *domain.CredentialAudit) error
	ListCredentialAudit(ctx context.Context, credentialID int64, limit int) ([]domain.CredentialAudit, error)

	// Credential binding helpers
	ListChannelInstanceCredentialBindings(ctx context.Context) (map[string]ChannelCredentialBinding, error)

	// Locale
	UpdateChannelLocale(ctx context.Context, chatID string, locale string) error
	GetChannelLocale(ctx context.Context, channelType, channelUserID string) (string, error)
	GetChannelLocaleByChatID(ctx context.Context, chatID string) (string, error)

	// Data migration
	BackfillUserIDs(ctx context.Context) (int64, error)

	RunMigrations(ctx context.Context) error
	Close() error
}

// CachedResponse represents a cached LLM response.
type CachedResponse struct {
	Response  string
	UsageJSON string
	HitCount  int
}

// TaskFilter specifies criteria for listing tasks.
type TaskFilter struct {
	Status *domain.TaskStatus
	Type   string
	Limit  int
}

// UsageUpsert is the input for upserting a usage stats row.
type UsageUpsert struct {
	ChatID              string
	UserID              string
	Model               string
	Provider            string
	Hour                time.Time
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	Requests            int64
	CostUSD             float64
}

// UsageFilter specifies criteria for querying usage stats.
type UsageFilter struct {
	ChatID   string
	UserID   string
	Model    string
	Provider string
	From     time.Time
	To       time.Time
}

// UsageSummary is the aggregated usage summary.
type UsageSummary struct {
	TotalInputTokens         int64
	TotalOutputTokens        int64
	TotalCacheReadTokens     int64
	TotalCacheCreationTokens int64
	TotalRequests            int64
	TotalCostUSD             float64
}

// DailyUsage is the per-day aggregation of usage stats.
type DailyUsage struct {
	Date                string  `json:"date"`
	Model               string  `json:"model,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	Requests            int64   `json:"requests"`
	CostUSD             float64 `json:"cost_usd"`
}

// ChannelCredentialBinding maps a channel instance to its bound credential.
type ChannelCredentialBinding struct {
	CredentialID   int64
	CredentialName string
}

// CredentialFilter specifies criteria for listing credentials.
type CredentialFilter struct {
	Scope   domain.CredentialScope
	OwnerID string
	Type    domain.CredentialType
}

// ModelUsage is the per-model aggregation of usage stats.
type ModelUsage struct {
	Model               string  `json:"model"`
	Provider            string  `json:"provider"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	Requests            int64   `json:"requests"`
	CostUSD             float64 `json:"cost_usd"`
}
