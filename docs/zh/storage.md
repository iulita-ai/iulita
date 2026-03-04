# 存储

Iulita 使用 SQLite 作为唯一存储后端，支持 WAL 模式、FTS5 全文搜索、ONNX 向量搜索和 bun ORM。

## SQLite 设置

### 连接

- **驱动**：`modernc.org/sqlite`（纯 Go，无 CGo）
- **ORM**：`uptrace/bun` + `sqlitedialect`
- **DSN**：`file:{path}?cache=shared&mode=rwc`

### 性能 PRAGMA

```sql
PRAGMA journal_mode = WAL;       -- 预写日志（并发读取）
PRAGMA synchronous = NORMAL;     -- WAL 安全，写入速度比 FULL 快 2 倍
PRAGMA mmap_size = 8388608;      -- 8MB 内存映射 I/O
PRAGMA cache_size = -2000;       -- 约 2MB 进程内页面缓存
PRAGMA temp_store = MEMORY;      -- 临时表存在 RAM 中（加速 FTS5/排序）
```

## 数据库模式

### 核心表

| 表 | 用途 | 关键列 |
|------|------|--------|
| `users` | 用户账户 | id (UUID)、username、password_hash、role、timezone |
| `user_channels` | 通道绑定 | user_id、channel_type、channel_user_id、locale |
| `channel_instances` | 机器人/集成配置 | slug、type、enabled、config_json |
| `chat_messages` | 对话历史 | chat_id、user_id、role、content |
| `facts` | 存储的记忆 | user_id、content、source_type、access_count、last_accessed_at |
| `insights` | 交叉引用洞察 | user_id、content、fact_ids、quality、expires_at |
| `directives` | 自定义指令 | user_id、content |
| `tech_facts` | 行为画像 | user_id、category、key、value、confidence |
| `reminders` | 定时提醒 | user_id、title、due_at、fired |
| `tasks` | 调度器任务队列 | type、status、payload、worker_id、capabilities |
| `scheduler_states` | 任务计时 | job_name、last_run、next_run |
| `agent_jobs` | 用户定义的定时 LLM 任务 | name、prompt、cron_expr、delivery_chat_id |
| `config_overrides` | 运行时配置 | key、value、encrypted、updated_by |
| `google_accounts` | OAuth2 令牌 | user_id、account_email、tokens（加密） |
| `installed_skills` | 外部技能 | slug、name、version、source |
| `todo_items` | 统一任务 | user_id、title、provider、external_id、due_date |
| `audit_log` | 审计日志 | user_id、action、details |
| `usage_stats` | 每小时令牌使用量 | chat_id、hour、input_tokens、output_tokens、cost_usd |

### FTS5 表

```sql
CREATE VIRTUAL TABLE facts_fts USING fts5(content, content_rowid=id);
CREATE VIRTUAL TABLE insights_fts USING fts5(content, content_rowid=id);
```

这些是"外部内容" FTS5 表 — 索引通过触发器镜像基表中的内容。

### FTS5 触发器

六个触发器保持 FTS 索引同步：

```sql
-- Facts
CREATE TRIGGER facts_ai AFTER INSERT ON facts BEGIN
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER facts_ad AFTER DELETE ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
END;

-- 关键："UPDATE OF content" — 不是 "UPDATE"
CREATE TRIGGER facts_au AFTER UPDATE OF content ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;
```

**触发器注意事项**：使用 `AFTER UPDATE ON facts`（不带 `OF content`）会导致触发器在任何列更新时触发（如 `access_count++`）。在 modernc SQLite 的 FTS5 实现中，这会导致 `SQL logic error (1)`。修复方法是 `AFTER UPDATE OF content` — 仅当 `content` 特定改变时才触发。

### 向量表

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

**编码**：每个 `float32` → 4 字节小端序。384 维 = 每个向量 1536 字节。

**自动检测**：`decodeVector()` 检查数据是否以 `[` 开头（JSON 数组）以兼容旧格式；否则按二进制解码。

### 缓存表

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

两者都使用 LRU 淘汰（超过最大条目时移除最旧的 `accessed_at` 记录）。

## 多用户数据范围

所有数据表都有 `user_id TEXT` 列。查询在可用时使用用户范围变体：

```
SearchFacts(ctx, chatID, query, limit)        // 旧版，单通道
SearchFactsByUser(ctx, userID, query, limit)  // 跨通道
```

优先使用用户范围变体：用户来自 Telegram、WebChat 和 Console 的事实都可以访问，无论当前通道。

### BackfillUserIDs

将旧数据（多用户支持之前创建的）关联到用户的迁移：

1. 删除 FTS 触发器（必需 — UPDATE 会触发它们）
2. 通过 `chat_id` 连接 `chat_messages → user_channels`
3. 批量 UPDATE：在 facts、insights、messages 等上设置 `user_id`
4. 重新创建 FTS 触发器

## 迁移

所有迁移在 `RunMigrations(ctx)` 中作为单个幂等函数运行：

1. 通过 `bun.CreateTableIfNotExists` 创建所有 18 个领域模型的**表**
2. 缓存的**原始 SQL 表**（非 bun 管理）
3. 通过 `ALTER TABLE ADD COLUMN` 的**增量列**（错误被忽略以实现幂等）
4. **旧版重命名**（如 `dreams → insights`）
5. 为性能关键查询**创建索引**
6. **FTS5 重建** — 始终删除并重新创建触发器 + 表以修复早期版本的损坏

### 关键索引

| 索引 | 用途 |
|------|------|
| `idx_tasks_status_scheduled` | 任务队列：按 scheduled_at 的待处理任务 |
| `idx_tasks_unique_key` | 幂等任务创建 |
| `idx_facts_user` | 用户范围的事实查询 |
| `idx_insights_user` | 用户范围的洞察查询 |
| `idx_user_channels_binding` | 唯一约束 (channel_type, channel_user_id) |
| `idx_todos_user_due` | 任务仪表盘：按到期日期的任务 |
| `idx_todos_external` | 外部任务通过 provider+external_id 去重 |
| `idx_techfacts_unique` | 唯一约束 (chat_id, category, key) |
| `idx_google_accounts_user_email` | 唯一约束 (user_id, account_email) |

## 任务队列

调度器使用 SQLite 作为任务队列，支持原子认领：

### 任务生命周期

```
pending → claimed → running → completed/failed
```

### 原子认领

`ClaimTask(ctx, workerID, capabilities)` 使用事务：
1. SELECT 一个待处理任务，其 `Capabilities` 是工作器能力的子集
2. UPDATE 状态为 `claimed`，设置 `worker_id`
3. 返回任务

这确保没有两个工作器认领同一个任务。

### 清理

- **过期任务**：`running` 超过 5 分钟的任务重置为 `pending`
- **旧任务**：超过 7 天的已完成/失败任务被删除
- **一次性任务**：`one_shot = true` 且 `delete_after_run = true` 的任务完成后被删除

## 混合搜索

完整算法请参阅[记忆与洞察 — 混合搜索](memory-and-insights.md#混合搜索算法)。

### 查询模式

```sql
-- FTS5 搜索
SELECT * FROM facts
WHERE user_id = ?
  AND id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)
ORDER BY access_count DESC, last_accessed_at DESC
LIMIT ?

-- 向量相似度（在 Go 中）
for _, vec := range allVectors {
    score := cosineSimilarity(queryVec, vec.Embedding)
}

-- 组合评分
combined = (1-vectorWeight)*ftsScore + vectorWeight*vecScore
```

## 仓库接口

`internal/storage/storage.go` 中的 `Repository` 接口有 60 多个方法，按领域组织：

| 领域 | 方法 |
|------|------|
| 消息 | Save、GetHistory、Clear、DeleteBefore |
| 事实 | Save、Search（FTS/混合）、Update、Delete、Reinforce、GetAll、GetRecent |
| 洞察 | Save、Search（FTS/混合）、Delete、Reinforce、GetRecent、DeleteExpired |
| 向量 | CreateTables、SaveFactVector、SaveInsightVector、GetWithoutEmbeddings |
| 用户 | Create、Get、Update、Delete、List、BindChannel、GetByChannel |
| 任务 | Create、Claim、Start、Complete、Fail、List、Cleanup |
| 配置 | Get、List、Save、Delete overrides |
| 缓存 | Embedding（get/save/evict）、Response（get/save/evict/stats） |
| 区域设置 | Update、Get（按通道类型或聊天 ID） |
| 待办事项 | CRUD、sync、counts、provider 查询 |
| 代理任务 | CRUD、GetDue |
| Google | Account CRUD、token 存储 |
| 外部技能 | Install、List、Get、Delete |
