# Хранилище

Iulita использует SQLite в качестве единственного бэкенда хранения с WAL-режимом, полнотекстовым поиском FTS5, ONNX-векторным поиском и ORM bun.

## Настройка SQLite

### Подключение

- **Драйвер**: `modernc.org/sqlite` (чистый Go, без CGo)
- **ORM**: `uptrace/bun` с `sqlitedialect`
- **DSN**: `file:{path}?cache=shared&mode=rwc`

### PRAGMAs для производительности

```sql
PRAGMA journal_mode = WAL;       -- Write-Ahead Logging (параллельное чтение)
PRAGMA synchronous = NORMAL;     -- Безопасно с WAL, 2x ускорение записи vs FULL
PRAGMA mmap_size = 8388608;      -- 8 МБ memory-mapped I/O
PRAGMA cache_size = -2000;       -- ~2 МБ кэш страниц в процессе
PRAGMA temp_store = MEMORY;      -- Временные таблицы в RAM (ускоряет FTS5/сортировки)
```

## Схема

### Основные таблицы

| Таблица | Назначение | Ключевые колонки |
|---------|-----------|------------------|
| `users` | Аккаунты пользователей | id (UUID), username, password_hash, role, timezone |
| `user_channels` | Привязки каналов | user_id, channel_type, channel_user_id, locale |
| `channel_instances` | Конфигурации ботов/интеграций | slug, type, enabled, config_json |
| `chat_messages` | История разговоров | chat_id, user_id, role, content |
| `facts` | Сохранённые воспоминания | user_id, content, source_type, access_count, last_accessed_at |
| `insights` | Перекрёстные инсайты | user_id, content, fact_ids, quality, expires_at |
| `directives` | Пользовательские инструкции | user_id, content |
| `tech_facts` | Поведенческий профиль | user_id, category, key, value, confidence |
| `reminders` | Напоминания по времени | user_id, title, due_at, fired |
| `tasks` | Очередь задач планировщика | type, status, payload, worker_id, capabilities |
| `scheduler_states` | Тайминг задач | job_name, last_run, next_run |
| `agent_jobs` | Пользовательские запланированные LLM-задачи | name, prompt, cron_expr, delivery_chat_id |
| `config_overrides` | Конфигурация рантайма | key, value, encrypted, updated_by |
| `google_accounts` | OAuth2-токены | user_id, account_email, tokens (зашифрованы) |
| `installed_skills` | Внешние навыки | slug, name, version, source |
| `todo_items` | Единые задачи | user_id, title, provider, external_id, due_date |
| `audit_log` | Аудит-лог | user_id, action, details |
| `usage_stats` | Использование токенов по часам | chat_id, hour, input_tokens, output_tokens, cost_usd |

### Таблицы FTS5

```sql
CREATE VIRTUAL TABLE facts_fts USING fts5(content, content_rowid=id);
CREATE VIRTUAL TABLE insights_fts USING fts5(content, content_rowid=id);
```

Это FTS5-таблицы с «внешним контентом» — индекс зеркалирует содержимое базовых таблиц через триггеры.

### Триггеры FTS5

Шесть триггеров поддерживают FTS-индексы в актуальном состоянии:

```sql
-- Facts
CREATE TRIGGER facts_ai AFTER INSERT ON facts BEGIN
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER facts_ad AFTER DELETE ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
END;

-- КРИТИЧНО: "UPDATE OF content" — НЕ "UPDATE"
CREATE TRIGGER facts_au AFTER UPDATE OF content ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;
```

**Подводный камень триггеров**: Использование `AFTER UPDATE ON facts` (без `OF content`) приводит к срабатыванию триггера при ЛЮБОМ обновлении колонки (напр., `access_count++`). С реализацией FTS5 modernc SQLite это вызывает `SQL logic error (1)`. Исправление — `AFTER UPDATE OF content` — триггер срабатывает только при изменении `content`.

### Таблицы векторов

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

**Кодирование**: Каждый `float32` -> 4 байта LittleEndian. 384 измерения = 1536 байт на вектор.

**Автоопределение**: `decodeVector()` проверяет, начинаются ли данные с `[` (JSON-массив) для обратной совместимости; иначе декодирует как бинарный формат.

### Таблицы кэша

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

Оба используют LRU-вытеснение (самый старый по `accessed_at` удаляется при превышении максимума записей).

## Мультипользовательское разграничение данных

Все таблицы данных имеют колонку `user_id TEXT`. Запросы используют варианты с привязкой к пользователю, когда они доступны:

```
SearchFacts(ctx, chatID, query, limit)        // устаревший, одноканальный
SearchFactsByUser(ctx, userID, query, limit)  // кроссканальный
```

Вариант с привязкой к пользователю предпочтителен: факты пользователя из Telegram, WebChat и Console доступны независимо от текущего канала.

### BackfillUserIDs

Миграция, которая привязывает устаревшие данные (созданные до мультипользовательской поддержки) к пользователям:

1. Удаление FTS-триггеров (обязательно — UPDATE вызвал бы их)
2. JOIN `chat_messages → user_channels` по `chat_id`
3. Массовый UPDATE: установка `user_id` на facts, insights, messages и т.д.
4. Пересоздание FTS-триггеров

## Миграции

Все миграции выполняются в `RunMigrations(ctx)` как единая идемпотентная функция:

1. **Создание таблиц** через `bun.CreateTableIfNotExists` для всех 18 доменных моделей
2. **Raw SQL таблицы** для кэшей (не управляются bun)
3. **Аддитивные колонки** через `ALTER TABLE ADD COLUMN` (ошибки игнорируются для идемпотентности)
4. **Устаревшие переименования** (напр., `dreams → insights`)
5. **Создание индексов** для критичных по производительности запросов
6. **Пересоздание FTS5** — всегда удалять и пересоздавать триггеры + таблицы для исправления повреждений от ранних версий

### Ключевые индексы

| Индекс | Назначение |
|--------|-----------|
| `idx_tasks_status_scheduled` | Очередь задач: ожидающие задачи по scheduled_at |
| `idx_tasks_unique_key` | Идемпотентное создание задач |
| `idx_facts_user` | Запросы фактов по пользователю |
| `idx_insights_user` | Запросы инсайтов по пользователю |
| `idx_user_channels_binding` | Уникальность (channel_type, channel_user_id) |
| `idx_todos_user_due` | Дашборд задач: задачи по дате |
| `idx_todos_external` | Дедупликация внешних задач по provider+external_id |
| `idx_techfacts_unique` | Уникальность (chat_id, category, key) |
| `idx_google_accounts_user_email` | Уникальность (user_id, account_email) |

## Очередь задач

Планировщик использует SQLite как очередь задач с атомарным захватом:

### Жизненный цикл задачи

```
pending → claimed → running → completed/failed
```

### Атомарный захват

`ClaimTask(ctx, workerID, capabilities)` использует транзакцию:
1. SELECT ожидающей задачи, чьи `Capabilities` являются подмножеством capabilities воркера
2. UPDATE статуса на `claimed`, установка `worker_id`
3. Возврат задачи

Это гарантирует, что два воркера не захватят одну и ту же задачу.

### Очистка

- **Зависшие задачи**: задачи в статусе `running` более 5 минут сбрасываются на `pending`
- **Старые задачи**: завершённые/неудачные задачи старше 7 дней удаляются
- **Одноразовые**: задачи с `one_shot = true` и `delete_after_run = true` удаляются после завершения

## Гибридный поиск

См. [Память и инсайты — Гибридный поиск](memory-and-insights.md#алгоритм-гибридного-поиска) для полного алгоритма.

### Паттерн запроса

```sql
-- FTS5-поиск
SELECT * FROM facts
WHERE user_id = ?
  AND id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)
ORDER BY access_count DESC, last_accessed_at DESC
LIMIT ?

-- Векторное сходство (в Go)
for _, vec := range allVectors {
    score := cosineSimilarity(queryVec, vec.Embedding)
}

-- Комбинированная оценка
combined = (1-vectorWeight)*ftsScore + vectorWeight*vecScore
```

## Интерфейс Repository

Интерфейс `Repository` в `internal/storage/storage.go` содержит 60+ методов, организованных по доменам:

| Домен | Методы |
|-------|--------|
| Сообщения | Save, GetHistory, Clear, DeleteBefore |
| Факты | Save, Search (FTS/Hybrid), Update, Delete, Reinforce, GetAll, GetRecent |
| Инсайты | Save, Search (FTS/Hybrid), Delete, Reinforce, GetRecent, DeleteExpired |
| Векторы | CreateTables, SaveFactVector, SaveInsightVector, GetWithoutEmbeddings |
| Пользователи | Create, Get, Update, Delete, List, BindChannel, GetByChannel |
| Задачи | Create, Claim, Start, Complete, Fail, List, Cleanup |
| Конфигурация | Get, List, Save, Delete overrides |
| Кэши | Embedding (get/save/evict), Response (get/save/evict/stats) |
| Локаль | Update, Get (по типу канала или chat ID) |
| Задачи (Todos) | CRUD, sync, counts, запросы по провайдерам |
| Agent Jobs | CRUD, GetDue |
| Google | CRUD аккаунтов, хранение токенов |
| Внешние навыки | Install, List, Get, Delete |
