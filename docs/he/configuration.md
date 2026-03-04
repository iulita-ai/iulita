# הגדרות

Iulita משתמש במערכת הגדרות שכבתית התומכת בהתקנה מקומית ללא הגדרות, תוך מתן אפשרות להתאמה אישית מלאה לפריסות מתקדמות.

## שכבות הגדרה

ההגדרות נטענות לפי סדר, כאשר שכבות מאוחרות דורסות שכבות מוקדמות:

```
1. ברירות מחדל מקומפלות (תמיד קיימות)
2. קובץ TOML (~/.config/iulita/config.toml, אופציונלי)
3. משתני סביבה (קידומת IULITA_*)
4. סודות keyring (macOS Keychain, Linux SecretService)
5. דריסות DB (טבלת config_overrides, ניתנות לעריכה בזמן ריצה)
```

### שכבה 1: ברירות מחדל מקומפלות

`DefaultConfig()` מספקת הגדרה עובדת ללא קבצים חיצוניים. כל מזהי מודלים, timeouts, הגדרות זיכרון ודגלי תכונות מגיעים עם ברירות מחדל הגיוניות. המערכת עובדת מהקופסה עם מפתח API בלבד.

### שכבה 2: קובץ TOML

אופציונלי. ממוקם ב-`~/.config/iulita/config.toml` (או `$IULITA_HOME/config.toml`).

קובץ ה-TOML **מדולג** אם:
- לא קיים קובץ בנתיב ההגדרות
- קובץ sentinel `db_managed` קיים (מצב אשף דפדפני)

ראה `config.toml.example` לעיון מלא.

### שכבה 3: משתני סביבה

ניתן לדרוס כל הגדרה דרך משתני סביבה `IULITA_*`:

```
IULITA_CLAUDE_API_KEY      → claude.api_key
IULITA_TELEGRAM_TOKEN      → telegram.token
IULITA_CLAUDE_MODEL        → claude.model
IULITA_STORAGE_PATH        → storage.path
IULITA_SERVER_ADDRESS      → server.address
IULITA_PROXY_URL           → proxy.url
```

**כלל מיפוי**: הסר קידומת `IULITA_`, המר לאותיות קטנות, החלף `_` ב-`.`.

### שכבה 4: סודות Keyring

סודות נשמרים בצורה מאובטחת ב-keyring של מערכת ההפעלה:

| סוד | משתנה סביבה | חשבון Keyring |
|--------|-------------|-----------------|
| מפתח API של Claude | `IULITA_CLAUDE_API_KEY` | `claude-api-key` |
| טוקן Telegram | `IULITA_TELEGRAM_TOKEN` | `telegram-token` |
| סוד JWT | `IULITA_JWT_SECRET` | `jwt-secret` |
| מפתח הצפנת הגדרות | `IULITA_CONFIG_KEY` | `config-encryption-key` |

**שרשרת חלופות** לכל סוד: משתנה סביבה → keyring → קובץ חלופי (למפתח הצפנה בלבד) → יצירה אוטומטית (ל-JWT בלבד).

ה-keyring משתמש ב-`zalando/go-keyring`:
- **macOS**: Keychain
- **Linux**: SecretService (GNOME Keyring, KDE Wallet)
- **חלופה**: קובץ מוצפן ב-`~/.config/iulita/encryption.key`

### שכבה 5: דריסות DB (Config Store)

הגדרות הניתנות לעריכה בזמן ריצה המאוחסנות בטבלת SQLite `config_overrides`. מנוהלות דרך:
- עורך הגדרות בלוח הבקרה
- כלי `skills` מבוסס צ'אט (פעולת `set_config`)
- אשף התקנה דפדפני

**תכונות:**
- הצפנת AES-256-GCM לערכים סודיים
- טעינה חמה מיידית דרך אפיק אירועים
- רישום ביקורת (מי שינה מה, מתי)
- הגנה על מפתחות שדורשים הפעלה מחדש

## נתיבים תואמי XDG

| פלטפורמה | הגדרות | נתונים | מטמון | מצב |
|----------|--------|------|-------|-------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

**דריסה**: הגדר `IULITA_HOME` לשימוש בשורש מותאם אישית עם תתי-תיקיות `data/`, `cache/`, `state/`.

### נתיבים נגזרים

| נתיב | מיקום |
|------|----------|
| קובץ הגדרות | `{ConfigDir}/config.toml` |
| מסד נתונים | `{DataDir}/iulita.db` |
| מודלי ONNX | `{DataDir}/models/` |
| מיומנויות | `{DataDir}/skills/` |
| מיומנויות חיצוניות | `{DataDir}/external-skills/` |
| קובץ לוג | `{StateDir}/iulita.log` |
| מפתח הצפנה | `{ConfigDir}/encryption.key` |

## סעיפי הגדרות

### App

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `app.system_prompt` | (מובנה) | פרומפט מערכת בסיסי לעוזר |
| `app.context_window` | 200000 | תקציב טוקנים להקשר |
| `app.request_timeout` | 120s | timeout לכל הודעה |

### Claude (LLM ראשי)

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `claude.api_key` | — | מפתח API של Anthropic (נדרש) |
| `claude.model` | `claude-sonnet-4-5-20250929` | מזהה מודל |
| `claude.max_tokens` | 8192 | מקסימום טוקני פלט |
| `claude.base_url` | — | דריסת כתובת בסיס API |
| `claude.thinking` | 0 | תקציב חשיבה מורחבת (0 = מושבת) |

### Ollama (LLM מקומי)

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `ollama.url` | `http://localhost:11434` | כתובת שרת Ollama |
| `ollama.model` | `llama3` | שם מודל |

### OpenAI (תאימות)

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `openai.api_key` | — | מפתח API |
| `openai.model` | `gpt-4` | מזהה מודל |
| `openai.base_url` | `https://api.openai.com/v1` | כתובת בסיס API |

### Telegram

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `telegram.token` | — | טוקן בוט (ניתן לטעינה חמה) |
| `telegram.allowed_ids` | `[]` | רשימה לבנה של מזהי משתמשים (ריק = כולם) |
| `telegram.debounce_window` | 2s | חלון איחוד הודעות |

### Storage

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `storage.path` | `{DataDir}/iulita.db` | נתיב מסד נתונים SQLite (הפעלה מחדש בלבד) |

### Server

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `server.enabled` | true | הפעל שרת לוח בקרה |
| `server.address` | `:8080` | כתובת האזנה (הפעלה מחדש בלבד) |

### Auth

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `auth.jwt_secret` | (נוצר אוטומטית) | מפתח חתימת JWT |
| `auth.token_ttl` | 24h | TTL טוקן גישה |
| `auth.refresh_ttl` | 7d | TTL טוקן רענון |

### Proxy

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `proxy.url` | — | פרוקסי HTTP/SOCKS5 (הפעלה מחדש בלבד) |

### Memory

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `skills.memory.half_life_days` | 30 | זמן מחצית חיים של דעיכה זמנית |
| `skills.memory.mmr_lambda` | 0 | גיוון MMR (0.7 מומלץ) |
| `skills.memory.vector_weight` | 0 | משקל חיפוש היברידי |
| `skills.memory.triggers` | `[]` | מילות מפתח לטריגר זיכרון |

### Insights

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `skills.insights.min_facts` | 20 | מספר עובדות מינימלי ליצירה |
| `skills.insights.max_pairs` | 6 | מקסימום זוגות אשכולות לריצה |
| `skills.insights.ttl` | 720h | תפוגת תובנה (30 יום) |
| `skills.insights.interval` | 24h | תדירות יצירה |
| `skills.insights.quality_threshold` | 0 | ניקוד איכות מינימלי |

### Embedding

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `embedding.enabled` | true | הפעל הטבעות ONNX |
| `embedding.model` | `KnightsAnalytics/all-MiniLM-L6-v2` | שם מודל |

### Scheduler

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `scheduler.enabled` | true | הפעל מתזמן משימות |
| `scheduler.worker_token` | — | טוקן Bearer עבור workers מרוחקים |

### Cost

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `cost.daily_limit_usd` | 0 | מגבלת עלות יומית (0 = ללא הגבלה) |

### Cache

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `cache.enabled` | false | הפעל מטמון תשובות |
| `cache.ttl` | 60m | TTL מטמון |
| `cache.max_items` | 1000 | מקסימום תשובות במטמון |

### Metrics

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `metrics.enabled` | false | הפעל מטריקות Prometheus |
| `metrics.address` | `:9090` | כתובת שרת מטריקות |

## אשף התקנה

### אשף CLI (`iulita init`)

התקנה אינטראקטיבית שמדריכה דרך:
1. בחירת ספק LLM (Claude/OpenAI/Ollama, בחירה מרובה)
2. הזנת מפתח API (נשמר ב-keyring)
3. אינטגרציות אופציונליות (Telegram, פרוקסי, הטבעות)
4. בחירת מודל (שליפה דינמית מהספק)

סודות הולכים ל-keyring; הגדרות לא-סודיות הולכים ל-`config.toml`.

### אשף התקנה דפדפני (Docker)

לפריסות Docker ללא גישה לטרמינל:

1. השרת מתחיל ב**מצב התקנה** כאשר לא מוגדר LLM והאשף לא הושלם
2. מצב לוח בקרה בלבד (ללא מיומנויות, מתזמן או ערוצים)
3. אשף 5 שלבים: ברוכים הבאים/ייבוא → ספק → הגדרות → תכונות → השלמה
4. תמיכת ייבוא TOML (הדבקת הגדרות קיימות)
5. יוצר קובץ sentinel `db_managed` (משבית טעינת TOML)
6. מגדיר `_system.wizard_completed` ב-config_overrides

## טעינה חמה

הגדרות אלו יכולות להשתנות בזמן ריצה ללא הפעלה מחדש:

| הגדרה | טריגר | מנגנון |
|---------|---------|-----------|
| מודל/טוקנים/מפתח Claude | עורך הגדרות בלוח הבקרה | `UpdateModel()`/`UpdateMaxTokens()`/`UpdateAPIKey()` |
| טוקן Telegram | עורך הגדרות בלוח הבקרה | `channelmgr.UpdateConfigToken()` → הפעלה מחדש של מופע |
| הפעלה/השבתת מיומנות | לוח בקרה או צ'אט | `registry.EnableSkill()`/`DisableSkill()` |
| הגדרות מיומנות (מפתחות API) | עורך הגדרות בלוח הבקרה | `ConfigReloadable.OnConfigChanged()` |
| פרומפט מערכת | עורך הגדרות בלוח הבקרה | `asst.SetSystemPrompt()` |
| תקציב חשיבה | עורך הגדרות בלוח הבקרה | `asst.SetThinkingBudget()` |

### הגדרות שדורשות הפעלה מחדש

אלו דורשות הפעלה מחדש מלאה:
- `storage.path`
- `server.address`
- `proxy.url`
- `security.config_key_env`

## הצפנת AES-256-GCM

ערכי הגדרות סודיים ב-DB מוצפנים:

1. **מקור מפתח**: משתנה סביבה `IULITA_CONFIG_KEY` → keyring → קובץ שנוצר אוטומטית
2. **אלגוריתם**: AES-256-GCM (הצפנה מאומתת)
3. **פורמט**: `base64(12-byte-nonce ‖ ciphertext)`
4. **הצפנה אוטומטית**: מפתחות שהוכרזו כ-`secret_keys` ב-SKILL.md תמיד מוצפנים
5. **אף פעם לא דולפים**: API לוח הבקרה מחזיר ערכים ריקים למפתחות מוצפנים
