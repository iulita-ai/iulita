package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"
)

// EmbedFunc generates embeddings for texts. Used as a callback to avoid import cycles.
type EmbedFunc func(ctx context.Context, texts []string) ([][]float32, error)

// Store implements storage.Repository using SQLite via bun.
type Store struct {
	db           *bun.DB
	halfLifeDays float64   // temporal decay half-life for memory ranking
	mmrLambda    float64   // MMR diversity parameter (0 = disabled)
	embedFunc    EmbedFunc // optional: auto-embed on save
}

// SetHalfLifeDays configures the temporal decay half-life for memory ranking.
func (s *Store) SetHalfLifeDays(days float64) {
	s.halfLifeDays = days
}

// SetMMRLambda configures the MMR diversity parameter for memory retrieval.
// 0 = disabled, 0.7 recommended. Higher values favor relevance over diversity.
func (s *Store) SetMMRLambda(lambda float64) {
	s.mmrLambda = lambda
}

// SetEmbedFunc sets a callback for auto-embedding facts and insights on save.
func (s *Store) SetEmbedFunc(fn EmbedFunc) {
	s.embedFunc = fn
}

// New creates a new SQLite store at the given file path.
func New(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?cache=shared&mode=rwc", path)
	sqldb, err := sql.Open(sqliteshim.ShimName, dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}

	db := bun.NewDB(sqldb, sqlitedialect.New())

	ctx := context.Background()

	// Enable WAL mode for concurrent read/write support.
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}

	// Performance tuning: safe with WAL mode, significant speedup.
	pragmas := []string{
		`PRAGMA synchronous=NORMAL`, // safe with WAL; 2x write speedup vs FULL
		`PRAGMA mmap_size=8388608`,  // 8MB memory-mapped I/O
		`PRAGMA cache_size=-2000`,   // ~2MB page cache in process
		`PRAGMA temp_store=MEMORY`,  // temp tables in RAM (faster FTS5, sorts)
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			return nil, fmt.Errorf("setting pragma %q: %w", p, err)
		}
	}

	return &Store{db: db}, nil
}

func (s *Store) RunMigrations(ctx context.Context) error {
	models := []struct {
		model any
		name  string
	}{
		{(*domain.User)(nil), "users"},
		{(*domain.UserChannel)(nil), "user_channels"},
		{(*domain.ChannelInstance)(nil), "channel_instances"},
		{(*domain.ChatMessage)(nil), "chat_messages"},
		{(*domain.Reminder)(nil), "reminders"},
		{(*domain.Directive)(nil), "directives"},
		{(*domain.Fact)(nil), "facts"},
		{(*domain.Insight)(nil), "insights"},
		{(*domain.TechFact)(nil), "tech_facts"},
		{(*domain.Task)(nil), "tasks"},
		{(*domain.SchedulerState)(nil), "scheduler_states"},
		{(*domain.AuditEntry)(nil), "audit_log"},
		{(*domain.UsageRecord)(nil), "usage_stats"},
		{(*domain.ConfigOverride)(nil), "config_overrides"},
		{(*domain.AgentJob)(nil), "agent_jobs"},
		{(*domain.GoogleAccount)(nil), "google_accounts"},
		{(*domain.InstalledSkill)(nil), "installed_skills"},
		{(*domain.TodoItem)(nil), "todo_items"},
	}

	// Rename legacy "dreams" table to "insights" (preserves existing data).
	s.db.ExecContext(ctx, `ALTER TABLE dreams RENAME TO insights`)

	for _, m := range models {
		_, err := s.db.NewCreateTable().
			Model(m.model).
			IfNotExists().
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("creating %s table: %w", m.name, err)
		}
	}

	// Add new columns to insights table (tolerates "duplicate column" errors).
	insightColumns := []string{
		`ALTER TABLE insights ADD COLUMN quality INTEGER DEFAULT 0`,
		`ALTER TABLE insights ADD COLUMN access_count INTEGER DEFAULT 0`,
		`ALTER TABLE insights ADD COLUMN last_accessed_at DATETIME`,
		`ALTER TABLE insights ADD COLUMN expires_at DATETIME`,
	}
	for _, stmt := range insightColumns {
		s.db.ExecContext(ctx, stmt) // ignore "duplicate column" errors
	}

	// Unique index for tech_facts upsert support.
	s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_techfacts_unique ON tech_facts(chat_id, category, key)`)

	// FTS tables and triggers are created/rebuilt later in the migration (after user_id columns).

	// Add one-shot task columns (tolerates "duplicate column" errors).
	s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN one_shot BOOLEAN NOT NULL DEFAULT 0`)
	s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN delete_after_run BOOLEAN NOT NULL DEFAULT 0`)

	// Task queue indexes for efficient claiming and listing.
	taskIndexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_tasks_status_scheduled ON tasks(status, scheduled_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_unique_key ON tasks(unique_key) WHERE unique_key != ''`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_type ON tasks(type)`,
	}
	for _, stmt := range taskIndexes {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("creating task index: %w", err)
		}
	}

	// Clear unique_key on completed/failed tasks so jobs can be re-created.
	s.db.ExecContext(ctx, `UPDATE tasks SET unique_key = '' WHERE status IN ('done', 'failed') AND unique_key != ''`)

	// Usage stats: unique index for hourly upsert.
	s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_stats_chat_hour ON usage_stats(chat_id, hour)`)

	// Add cost_usd column to usage_stats (tolerates "duplicate column" error).
	s.db.ExecContext(ctx, `ALTER TABLE usage_stats ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0`)

	// Google accounts: unique index for user_id + account_email.
	s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_google_accounts_user_email ON google_accounts(user_id, account_email)`)

	// Embedding cache table.
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS embedding_cache (
			content_hash TEXT PRIMARY KEY,
			embedding BLOB NOT NULL,
			dimensions INTEGER NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("creating embedding_cache table: %w", err)
	}

	// Response cache table.
	_, err = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS response_cache (
			prompt_hash TEXT PRIMARY KEY,
			model TEXT NOT NULL,
			response TEXT NOT NULL,
			usage_json TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			hit_count INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return fmt.Errorf("creating response_cache table: %w", err)
	}

	// Audit log: index for querying by chat.
	s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_audit_log_chat ON audit_log(chat_id, created_at)`)

	// --- Multi-user migration: add user_id columns to all data tables ---
	// NOTE: SQLite doesn't allow NOT NULL on ALTER TABLE ADD COLUMN unless DEFAULT is provided.
	// Some drivers (modernc) reject NOT NULL entirely on ALTER TABLE — use nullable with default.
	userIDColumns := []string{
		`ALTER TABLE chat_messages ADD COLUMN user_id TEXT DEFAULT ''`,
		`ALTER TABLE facts ADD COLUMN user_id TEXT DEFAULT ''`,
		`ALTER TABLE insights ADD COLUMN user_id TEXT DEFAULT ''`,
		`ALTER TABLE directives ADD COLUMN user_id TEXT DEFAULT ''`,
		`ALTER TABLE tech_facts ADD COLUMN user_id TEXT DEFAULT ''`,
		`ALTER TABLE audit_log ADD COLUMN user_id TEXT DEFAULT ''`,
		`ALTER TABLE usage_stats ADD COLUMN user_id TEXT DEFAULT ''`,
	}
	for _, stmt := range userIDColumns {
		s.db.ExecContext(ctx, stmt) // ignore "duplicate column" errors
	}

	// Add channel_instance_id to user_channels (tolerates "duplicate column" error).
	s.db.ExecContext(ctx, `ALTER TABLE user_channels ADD COLUMN channel_instance_id TEXT NOT NULL DEFAULT ''`)

	// Add locale column for i18n support (tolerates "duplicate column" error).
	s.db.ExecContext(ctx, `ALTER TABLE user_channels ADD COLUMN locale TEXT NOT NULL DEFAULT 'en'`)

	// Add rich metadata columns to installed_skills (tolerates "duplicate column" errors).
	skillMetaCols := []string{
		`ALTER TABLE installed_skills ADD COLUMN capabilities TEXT DEFAULT ''`,
		`ALTER TABLE installed_skills ADD COLUMN config_keys TEXT DEFAULT ''`,
		`ALTER TABLE installed_skills ADD COLUMN secret_keys TEXT DEFAULT ''`,
		`ALTER TABLE installed_skills ADD COLUMN requires_bins TEXT DEFAULT ''`,
		`ALTER TABLE installed_skills ADD COLUMN requires_env TEXT DEFAULT ''`,
		`ALTER TABLE installed_skills ADD COLUMN allowed_tools TEXT DEFAULT ''`,
		`ALTER TABLE installed_skills ADD COLUMN has_code BOOLEAN DEFAULT 0`,
		`ALTER TABLE installed_skills ADD COLUMN effective_mode TEXT DEFAULT ''`,
		`ALTER TABLE installed_skills ADD COLUMN install_warnings TEXT DEFAULT ''`,
	}
	for _, stmt := range skillMetaCols {
		s.db.ExecContext(ctx, stmt)
	}

	// Rebuild FTS indexes: drop all triggers, drop and recreate FTS tables,
	// repopulate from source, recreate triggers with "UPDATE OF content" fix.
	// This fixes corrupted FTS indexes caused by earlier trigger issues.
	for _, t := range []string{"facts_ai", "facts_ad", "facts_au", "insights_ai", "insights_ad", "insights_au"} {
		s.db.ExecContext(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s`, t))
	}
	s.db.ExecContext(ctx, `DROP TABLE IF EXISTS facts_fts`)
	s.db.ExecContext(ctx, `DROP TABLE IF EXISTS insights_fts`)

	_, err = s.db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(content, content_rowid=id)`)
	if err != nil {
		return fmt.Errorf("recreating facts_fts: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS insights_fts USING fts5(content, content_rowid=id)`)
	if err != nil {
		return fmt.Errorf("recreating insights_fts: %w", err)
	}

	s.db.ExecContext(ctx, `INSERT INTO facts_fts(rowid, content) SELECT id, content FROM facts`)
	s.db.ExecContext(ctx, `INSERT INTO insights_fts(rowid, content) SELECT id, content FROM insights`)

	// Recreate triggers with "UPDATE OF content" to avoid firing on non-content updates.
	// Use regular DELETE (not the special 'delete' command) — more reliable with standalone FTS tables.
	ftsTriggers := []string{
		`CREATE TRIGGER facts_ai AFTER INSERT ON facts BEGIN
			INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
		END`,
		`CREATE TRIGGER facts_ad AFTER DELETE ON facts BEGIN
			DELETE FROM facts_fts WHERE rowid = old.id;
		END`,
		`CREATE TRIGGER facts_au AFTER UPDATE OF content ON facts BEGIN
			DELETE FROM facts_fts WHERE rowid = old.id;
			INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
		END`,
		`CREATE TRIGGER insights_ai AFTER INSERT ON insights BEGIN
			INSERT INTO insights_fts(rowid, content) VALUES (new.id, new.content);
		END`,
		`CREATE TRIGGER insights_ad AFTER DELETE ON insights BEGIN
			DELETE FROM insights_fts WHERE rowid = old.id;
		END`,
		`CREATE TRIGGER insights_au AFTER UPDATE OF content ON insights BEGIN
			DELETE FROM insights_fts WHERE rowid = old.id;
			INSERT INTO insights_fts(rowid, content) VALUES (new.id, new.content);
		END`,
	}
	for _, t := range ftsTriggers {
		if _, err := s.db.ExecContext(ctx, t); err != nil {
			return fmt.Errorf("creating FTS trigger: %w", err)
		}
	}

	// Indexes for user-scoped queries.
	userIndexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_facts_user ON facts(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_insights_user ON insights(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_user ON chat_messages(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_techfacts_user ON tech_facts(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_directives_user ON directives(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_log(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_user ON usage_stats(user_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_user_channels_binding ON user_channels(channel_type, channel_user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_user_channels_user ON user_channels(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_channel_instances_type ON channel_instances(type)`,
	}
	for _, stmt := range userIndexes {
		s.db.ExecContext(ctx, stmt)
	}

	// Todo items indexes.
	todoIndexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_todos_user_due ON todo_items(user_id, due_date)`,
		`CREATE INDEX IF NOT EXISTS idx_todos_user_completed ON todo_items(user_id, completed_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_todos_external ON todo_items(user_id, provider, external_id) WHERE external_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_todos_provider ON todo_items(provider)`,
	}
	for _, stmt := range todoIndexes {
		s.db.ExecContext(ctx, stmt)
	}

	return nil
}

func (s *Store) SaveMessage(ctx context.Context, msg *domain.ChatMessage) error {
	_, err := s.db.NewInsert().Model(msg).Exec(ctx)
	if err != nil {
		return fmt.Errorf("inserting message: %w", err)
	}
	return nil
}

func (s *Store) GetHistory(ctx context.Context, chatID string, limit int) ([]domain.ChatMessage, error) {
	var msgs []domain.ChatMessage

	// Get the last N messages ordered by creation time ascending.
	// We use a subquery approach: select last N desc, then re-order asc.
	err := s.db.NewSelect().
		Model(&msgs).
		Where("chat_id = ?", chatID).
		Order("id DESC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying history: %w", err)
	}

	// Reverse to get chronological order.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	return msgs, nil
}

func (s *Store) GetHistoryBefore(ctx context.Context, chatID string, beforeID int64, limit int) ([]domain.ChatMessage, error) {
	var msgs []domain.ChatMessage
	err := s.db.NewSelect().
		Model(&msgs).
		Where("chat_id = ?", chatID).
		Where("id < ?", beforeID).
		Order("id DESC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying history before %d: %w", beforeID, err)
	}
	// Reverse to chronological order.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (s *Store) ClearHistory(ctx context.Context, chatID string) error {
	_, err := s.db.NewDelete().
		Model((*domain.ChatMessage)(nil)).
		Where("chat_id = ?", chatID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("clearing history: %w", err)
	}
	return nil
}

func (s *Store) DeleteMessagesBefore(ctx context.Context, chatID string, beforeID int64) error {
	_, err := s.db.NewDelete().
		Model((*domain.ChatMessage)(nil)).
		Where("chat_id = ? AND id < ?", chatID, beforeID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("deleting messages before %d: %w", beforeID, err)
	}
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
