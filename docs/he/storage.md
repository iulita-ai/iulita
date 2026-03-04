# אחסון

Iulita משתמש ב-SQLite כמנוע אחסון יחיד עם מצב WAL, חיפוש טקסט מלא FTS5, חיפוש וקטורי ONNX ו-ORM bun.

## הגדרת SQLite

### חיבור

- **דרייבר**: `modernc.org/sqlite` (Go טהור, ללא CGo)
- **ORM**: `uptrace/bun` עם `sqlitedialect`
- **DSN**: `file:{path}?cache=shared&mode=rwc`

### PRAGMAs לביצועים

```sql
PRAGMA journal_mode = WAL;       -- Write-Ahead Logging (קריאות מקביליות)
PRAGMA synchronous = NORMAL;     -- בטוח עם WAL, מהירות כתיבה כפולה לעומת FULL
PRAGMA mmap_size = 8388608;      -- 8MB קלט/פלט ממופה לזיכרון
PRAGMA cache_size = -2000;       -- ~2MB מטמון דפים בתהליך
PRAGMA temp_store = MEMORY;      -- טבלאות זמניות ב-RAM (מאיץ FTS5/מיונים)
```

## סכמה

### טבלאות ליבה

| טבלה | מטרה | עמודות מרכזיות |
|-------|---------|-------------|
| `users` | חשבונות משתמשים | id (UUID), username, password_hash, role, timezone |
| `user_channels` | חיבורי ערוצים | user_id, channel_type, channel_user_id, locale |
| `channel_instances` | הגדרות בוט/אינטגרציה | slug, type, enabled, config_json |
| `chat_messages` | היסטוריית שיחות | chat_id, user_id, role, content |
| `facts` | זכרונות מאוחסנים | user_id, content, source_type, access_count, last_accessed_at |
| `insights` | תובנות הצלבה | user_id, content, fact_ids, quality, expires_at |
| `directives` | הוראות מותאמות | user_id, content |
| `tech_facts` | פרופיל התנהגותי | user_id, category, key, value, confidence |
| `reminders` | תזכורות מבוססות זמן | user_id, title, due_at, fired |
| `tasks` | תור משימות מתזמן | type, status, payload, worker_id, capabilities |
| `scheduler_states` | תזמון משימות | job_name, last_run, next_run |
| `agent_jobs` | משימות LLM מתוזמנות מוגדרות משתמש | name, prompt, cron_expr, delivery_chat_id |
| `config_overrides` | הגדרות בזמן ריצה | key, value, encrypted, updated_by |
| `google_accounts` | טוקני OAuth2 | user_id, account_email, tokens (מוצפנים) |
| `installed_skills` | מיומנויות חיצוניות | slug, name, version, source |
| `todo_items` | משימות מאוחדות | user_id, title, provider, external_id, due_date |
| `audit_log` | מעקב ביקורת | user_id, action, details |
| `usage_stats` | שימוש בטוקנים לשעה | chat_id, hour, input_tokens, output_tokens, cost_usd |

### טבלאות FTS5

```sql
CREATE VIRTUAL TABLE facts_fts USING fts5(content, content_rowid=id);
CREATE VIRTUAL TABLE insights_fts USING fts5(content, content_rowid=id);
```

אלו טבלאות FTS5 עם "תוכן חיצוני" — האינדקס משקף תוכן מטבלאות הבסיס דרך טריגרים.

### טריגרי FTS5

שישה טריגרים שומרים על אינדקסי FTS מסונכרנים:

```sql
-- עובדות
CREATE TRIGGER facts_ai AFTER INSERT ON facts BEGIN
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER facts_ad AFTER DELETE ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
END;

-- קריטי: "UPDATE OF content" — לא "UPDATE"
CREATE TRIGGER facts_au AFTER UPDATE OF content ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;
```

**מלכודת הטריגר**: שימוש ב-`AFTER UPDATE ON facts` (ללא `OF content`) גורם לטריגר להפעל בכל עדכון עמודה (למשל `access_count++`). עם מימוש FTS5 של modernc SQLite, זה גורם ל-`SQL logic error (1)`. הפתרון הוא `AFTER UPDATE OF content` — הטריגר מופעל רק כאשר `content` משתנה ספציפית.

### טבלאות וקטורים

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

**קידוד**: כל `float32` → 4 בתים LittleEndian. 384 ממדים = 1536 בתים לכל וקטור.

**זיהוי אוטומטי**: `decodeVector()` בודק אם הנתונים מתחילים ב-`[` (מערך JSON) לתאימות לאחור; אחרת מפענח כבינארי.

### טבלאות מטמון

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

שתיהן משתמשות בפינוי LRU (הישן ביותר ב-`accessed_at` מוסר כאשר חורג ממקסימום הרשומות).

## היקף נתונים מרובה משתמשים

לכל טבלאות הנתונים יש עמודת `user_id TEXT`. שאילתות משתמשות בגרסאות מוגבלות למשתמש כאשר זמינות:

```
SearchFacts(ctx, chatID, query, limit)        // ישן, ערוץ בודד
SearchFactsByUser(ctx, userID, query, limit)  // חוצה ערוצים
```

הגרסה המוגבלת למשתמש מועדפת: עובדות של משתמש מ-Telegram, WebChat ו-Console כולן נגישות ללא קשר לערוץ הנוכחי.

### BackfillUserIDs

מיגרציה ששייכת נתונים ישנים (שנוצרו לפני תמיכת ריבוי משתמשים) למשתמשים:

1. הסרת טריגרי FTS (נדרש — UPDATE היה מפעיל אותם)
2. חיבור `chat_messages → user_channels` על `chat_id`
3. UPDATE מרוכז: הגדרת `user_id` על עובדות, תובנות, הודעות וכו'
4. יצירה מחדש של טריגרי FTS

## מיגרציות

כל המיגרציות רצות ב-`RunMigrations(ctx)` כפונקציה אידמפוטנטית יחידה:

1. **יצירת טבלאות** דרך `bun.CreateTableIfNotExists` לכל 18 מודלי הדומיין
2. **טבלאות SQL גולמיות** למטמונים (לא מנוהלים על ידי bun)
3. **עמודות תוספתיות** דרך `ALTER TABLE ADD COLUMN` (שגיאות מתעלמות לאידמפוטנטיות)
4. **שינויי שם ישנים** (למשל `dreams → insights`)
5. **יצירת אינדקסים** לשאילתות קריטיות לביצועים
6. **יצירה מחדש של FTS5** — תמיד מוחק ויוצר מחדש טריגרים + טבלאות לתיקון שחיתות מגרסאות קודמות

### אינדקסים מרכזיים

| אינדקס | מטרה |
|-------|---------|
| `idx_tasks_status_scheduled` | תור משימות: משימות ממתינות לפי scheduled_at |
| `idx_tasks_unique_key` | יצירת משימות אידמפוטנטית |
| `idx_facts_user` | שאילתות עובדות מוגבלות למשתמש |
| `idx_insights_user` | שאילתות תובנות מוגבלות למשתמש |
| `idx_user_channels_binding` | ייחודי (channel_type, channel_user_id) |
| `idx_todos_user_due` | לוח משימות: משימות לפי תאריך יעד |
| `idx_todos_external` | מניעת כפילויות משימות חיצוניות לפי provider+external_id |
| `idx_techfacts_unique` | ייחודי (chat_id, category, key) |
| `idx_google_accounts_user_email` | ייחודי (user_id, account_email) |

## תור משימות

המתזמן משתמש ב-SQLite כתור משימות עם תביעה אטומית:

### מחזור חיי משימה

```
pending → claimed → running → completed/failed
```

### תביעה אטומית

`ClaimTask(ctx, workerID, capabilities)` משתמש בטרנזקציה:
1. SELECT משימה ממתינה ש-`Capabilities` שלה הוא תת-קבוצה של יכולות ה-worker
2. UPDATE סטטוס ל-`claimed`, הגדרת `worker_id`
3. החזרת המשימה

זה מבטיח ששני workers לא תובעים את אותה משימה.

### ניקוי

- **משימות מיושנות**: משימות `running` מעל 5 דקות מאופסות ל-`pending`
- **משימות ישנות**: משימות completed/failed ישנות מ-7 ימים נמחקות
- **חד-פעמיות**: משימות עם `one_shot = true` ו-`delete_after_run = true` נמחקות לאחר השלמה

## חיפוש היברידי

ראה [זיכרון ותובנות — חיפוש היברידי](memory-and-insights.md#אלגוריתם-חיפוש-היברידי) לאלגוריתם המלא.

### תבנית שאילתה

```sql
-- חיפוש FTS5
SELECT * FROM facts
WHERE user_id = ?
  AND id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)
ORDER BY access_count DESC, last_accessed_at DESC
LIMIT ?

-- דמיון וקטורי (ב-Go)
for _, vec := range allVectors {
    score := cosineSimilarity(queryVec, vec.Embedding)
}

-- ניקוד משולב
combined = (1-vectorWeight)*ftsScore + vectorWeight*vecScore
```

## ממשק Repository

ממשק `Repository` ב-`internal/storage/storage.go` מכיל 60+ שיטות מסודרות לפי דומיין:

| דומיין | שיטות |
|--------|---------|
| הודעות | Save, GetHistory, Clear, DeleteBefore |
| עובדות | Save, Search (FTS/Hybrid), Update, Delete, Reinforce, GetAll, GetRecent |
| תובנות | Save, Search (FTS/Hybrid), Delete, Reinforce, GetRecent, DeleteExpired |
| וקטורים | CreateTables, SaveFactVector, SaveInsightVector, GetWithoutEmbeddings |
| משתמשים | Create, Get, Update, Delete, List, BindChannel, GetByChannel |
| משימות | Create, Claim, Start, Complete, Fail, List, Cleanup |
| הגדרות | Get, List, Save, Delete overrides |
| מטמונים | Embedding (get/save/evict), Response (get/save/evict/stats) |
| Locale | Update, Get (לפי סוג ערוץ או chat ID) |
| Todo | CRUD, sync, counts, שאילתות ספק |
| Agent Jobs | CRUD, GetDue |
| Google | Account CRUD, אחסון טוקנים |
| מיומנויות חיצוניות | Install, List, Get, Delete |
