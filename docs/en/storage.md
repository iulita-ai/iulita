# Storage

Iulita uses SQLite as its single storage backend with WAL mode, FTS5 full-text search, ONNX vector search, and bun ORM.

## SQLite Setup

### Connection

- **Driver**: `modernc.org/sqlite` (pure Go, no CGo)
- **ORM**: `uptrace/bun` with `sqlitedialect`
- **DSN**: `file:{path}?cache=shared&mode=rwc`

### Performance PRAGMAs

```sql
PRAGMA journal_mode = WAL;       -- Write-Ahead Logging (concurrent reads)
PRAGMA synchronous = NORMAL;     -- Safe with WAL, 2x write speedup vs FULL
PRAGMA mmap_size = 8388608;      -- 8MB memory-mapped I/O
PRAGMA cache_size = -2000;       -- ~2MB in-process page cache
PRAGMA temp_store = MEMORY;      -- Temp tables in RAM (speeds up FTS5/sorts)
```

## Schema

### Core Tables

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `users` | User accounts | id (UUID), username, password_hash, role, timezone |
| `user_channels` | Channel bindings | user_id, channel_type, channel_user_id, locale |
| `channel_instances` | Bot/integration configs | slug, type, enabled, config_json |
| `chat_messages` | Conversation history | chat_id, user_id, role, content |
| `facts` | Stored memories | user_id, content, source_type, access_count, last_accessed_at |
| `insights` | Cross-reference insights | user_id, content, fact_ids, quality, expires_at |
| `directives` | Custom instructions | user_id, content |
| `tech_facts` | Behavioral profile | user_id, category, key, value, confidence |
| `reminders` | Time-based reminders | user_id, title, due_at, fired |
| `tasks` | Scheduler task queue | type, status, payload, worker_id, capabilities |
| `scheduler_states` | Job timing | job_name, last_run, next_run |
| `agent_jobs` | User-defined scheduled LLM tasks | name, prompt, cron_expr, delivery_chat_id |
| `config_overrides` | Runtime config | key, value, encrypted, updated_by |
| `google_accounts` | OAuth2 tokens | user_id, account_email, tokens (encrypted) |
| `installed_skills` | External skills | slug, name, version, source |
| `todo_items` | Unified tasks | user_id, title, provider, external_id, due_date |
| `audit_log` | Audit trail | user_id, action, details |
| `usage_stats` | Token usage per hour | chat_id, hour, input_tokens, output_tokens, cost_usd |

### FTS5 Tables

```sql
CREATE VIRTUAL TABLE facts_fts USING fts5(content, content_rowid=id);
CREATE VIRTUAL TABLE insights_fts USING fts5(content, content_rowid=id);
```

These are "external content" FTS5 tables — the index mirrors content from the base tables via triggers.

### FTS5 Triggers

Six triggers keep FTS indexes in sync:

```sql
-- Facts
CREATE TRIGGER facts_ai AFTER INSERT ON facts BEGIN
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER facts_ad AFTER DELETE ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
END;

-- CRITICAL: "UPDATE OF content" — NOT "UPDATE"
CREATE TRIGGER facts_au AFTER UPDATE OF content ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;
```

**The trigger gotcha**: Using `AFTER UPDATE ON facts` (without `OF content`) causes the trigger to fire on ANY column update (e.g., `access_count++`). With modernc SQLite's FTS5 implementation, this causes `SQL logic error (1)`. The fix is `AFTER UPDATE OF content` — triggers only when `content` specifically changes.

### Vector Tables

```sql
CREATE TABLE IF NOT EXISTS fact_vectors (
    fact_id INTEGER PRIMARY KEY REFERENCES facts(id) ON DELETE CASCADE,
    embedding BLOB NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS insight_vectors (
    insight_id INTEGER PRIMARY KEY REFERENCES insights(id) ON DELETE CASCADE,
    embedding BLOB NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

**Encoding**: Each `float32` → 4 bytes LittleEndian. 384 dimensions = 1536 bytes per vector.

**Auto-detect**: `decodeVector()` checks if data starts with `[` (JSON array) for legacy compatibility; otherwise decodes as binary.

### Cache Tables

```sql
CREATE TABLE IF NOT EXISTS embedding_cache (
    content_hash TEXT PRIMARY KEY,
    embedding BLOB NOT NULL,
    dimensions INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS response_cache (
    prompt_hash TEXT PRIMARY KEY,
    model TEXT NOT NULL,
    response TEXT NOT NULL,
    usage_json TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    hit_count INTEGER NOT NULL DEFAULT 0
);
```

Both use LRU eviction (oldest `accessed_at` removed when exceeding max entries).

## Multi-User Data Scoping

All data tables have a `user_id TEXT` column. Queries use user-scoped variants when available:

```
SearchFacts(ctx, chatID, query, limit)        // legacy, single-channel
SearchFactsByUser(ctx, userID, query, limit)  // cross-channel
```

The user-scoped variant is preferred: a user's facts from Telegram, WebChat, and Console are all accessible regardless of current channel.

### BackfillUserIDs

Migration that associates legacy data (created before multi-user support) with users:

1. Drop FTS triggers (required — UPDATE would fire them)
2. Join `chat_messages → user_channels` on `chat_id`
3. Bulk UPDATE: set `user_id` on facts, insights, messages, etc.
4. Recreate FTS triggers

## Migrations

All migrations run in `RunMigrations(ctx)` as a single idempotent function:

1. **Table creation** via `bun.CreateTableIfNotExists` for all 18 domain models
2. **Raw SQL tables** for caches (not bun-managed)
3. **Additive columns** via `ALTER TABLE ADD COLUMN` (errors ignored for idempotency)
4. **Legacy renames** (e.g., `dreams → insights`)
5. **Index creation** for performance-critical queries
6. **FTS5 recreation** — always drop and recreate triggers + tables to fix corruption from earlier versions

### Key Indexes

| Index | Purpose |
|-------|---------|
| `idx_tasks_status_scheduled` | Task queue: pending tasks by scheduled_at |
| `idx_tasks_unique_key` | Idempotent task creation |
| `idx_facts_user` | User-scoped fact queries |
| `idx_insights_user` | User-scoped insight queries |
| `idx_user_channels_binding` | Unique (channel_type, channel_user_id) |
| `idx_todos_user_due` | Task dashboard: tasks by due date |
| `idx_todos_external` | External task dedup by provider+external_id |
| `idx_techfacts_unique` | Unique (chat_id, category, key) |
| `idx_google_accounts_user_email` | Unique (user_id, account_email) |

## Task Queue

The scheduler uses SQLite as a task queue with atomic claiming:

### Task Lifecycle

```
pending → claimed → running → completed/failed
```

### Atomic Claiming

`ClaimTask(ctx, workerID, capabilities)` uses a transaction:
1. SELECT a pending task whose `Capabilities` is a subset of the worker's
2. UPDATE status to `claimed`, set `worker_id`
3. Return the task

This ensures no two workers claim the same task.

### Cleanup

- **Stale tasks**: tasks `running` for > 5 minutes are reset to `pending`
- **Old tasks**: completed/failed tasks older than 7 days are deleted
- **One-shot**: tasks with `one_shot = true` and `delete_after_run = true` are deleted after completion

## Hybrid Search

See [Memory and Insights — Hybrid Search](memory-and-insights.md#hybrid-search-algorithm) for the complete algorithm.

### Query Pattern

```sql
-- FTS5 search
SELECT * FROM facts
WHERE user_id = ?
  AND id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)
ORDER BY access_count DESC, last_accessed_at DESC
LIMIT ?

-- Vector similarity (in Go)
for _, vec := range allVectors {
    score := cosineSimilarity(queryVec, vec.Embedding)
}

-- Combined scoring
combined = (1-vectorWeight)*ftsScore + vectorWeight*vecScore
```

## Repository Interface

The `Repository` interface in `internal/storage/storage.go` has 60+ methods organized by domain:

| Domain | Methods |
|--------|---------|
| Messages | Save, GetHistory, Clear, DeleteBefore |
| Facts | Save, Search (FTS/Hybrid), Update, Delete, Reinforce, GetAll, GetRecent |
| Insights | Save, Search (FTS/Hybrid), Delete, Reinforce, GetRecent, DeleteExpired |
| Vectors | CreateTables, SaveFactVector, SaveInsightVector, GetWithoutEmbeddings |
| Users | Create, Get, Update, Delete, List, BindChannel, GetByChannel |
| Tasks | Create, Claim, Start, Complete, Fail, List, Cleanup |
| Config | Get, List, Save, Delete overrides |
| Caches | Embedding (get/save/evict), Response (get/save/evict/stats) |
| Locale | Update, Get (by channel type or chat ID) |
| Todos | CRUD, sync, counts, provider queries |
| Agent Jobs | CRUD, GetDue |
| Google | Account CRUD, token storage |
| External Skills | Install, List, Get, Delete |
