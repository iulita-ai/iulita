# ארכיטקטורה

## סקירה ברמה גבוהה

```
Console TUI ─┐
Telegram ────┤
Web Chat ────┼→ Channel Manager → Assistant → LLM Provider Chain
                     ↕                ↕
                 UserResolver      Storage (SQLite)
                                     ↕
                               Scheduler → Worker
                               (insights, analysis, reminders)
                                     ↕
                                Event Bus → Dashboard (WebSocket)
                                          → Prometheus Metrics
                                          → Push Notifications
                                          → Cost Tracker
```

## עקרונות עיצוב מרכזיים

1. **זיכרון מבוסס עובדות** — רק נתוני משתמש מאומתים נשמרים, לעולם לא ידע שנוצר מהזיות
2. **קונסולה תחילה** — TUI הוא מצב ברירת המחדל; מצב שרת הוא אופציונלי
3. **ארכיטקטורה נקייה** — מודלי דומיין ‫→ ממשקים → מימושים → תזמורן
4. **ריבוי ערוצים, זהות אחת** — עובדות ותובנות משותפות בין כל הערוצים דרך user_id
5. **התקנה מקומית ללא הגדרות** — עובד מהקופסה עם מפתח API בלבד
6. **טעינה חמה** — מיומנויות, הגדרות ואפילו טוקן Telegram יכולים להשתנות בזמן ריצה ללא הפעלה מחדש

## מפת רכיבים

| רכיב | חבילה | תיאור |
|-----------|---------|-------------|
| נקודת כניסה | `cmd/iulita/` | ניתוח CLI, חיווט DI, כיבוי מסודר |
| עוזר | `internal/assistant/` | תזמורן: לולאת LLM, זיכרון, דחיסה, אישורים, סטרימינג |
| ערוצים | `internal/channel/` | מתאמי קלט: Console TUI, Telegram, WebChat |
| מנהל ערוצים | `internal/channelmgr/` | מחזור חיי ערוצים, ניתוב, טעינה חמה |
| ספקי LLM | `internal/llm/` | Claude, Ollama, OpenAI, הטבעות ONNX |
| מיומנויות | `internal/skill/` | 20+ מימושי כלים |
| מנהל מיומנויות | `internal/skillmgr/` | מיומנויות חיצוניות: שוק ClawhHub, URL, מקומי |
| אחסון | `internal/storage/sqlite/` | SQLite עם FTS5, וקטורים, מצב WAL |
| מתזמן | `internal/scheduler/` | תור משימות עם תמיכה ב-cron/interval |
| לוח בקרה | `internal/dashboard/` | GoFiber REST API + SPA Vue 3 מוטבע |
| הגדרות | `internal/config/` | הגדרות שכבתיות: ברירות מחדל → TOML → env → keyring → DB |
| אימות | `internal/auth/` | JWT + bcrypt, middleware |
| i18n | `internal/i18n/` | 6 שפות, קטלוגי TOML, הפצת הקשר |
| חיפוש אינטרנט | `internal/web/` | Brave + גיבוי DuckDuckGo, הגנת SSRF |
| דומיין | `internal/domain/` | מודלי דומיין טהורים |
| זיכרון | `internal/memory/` | אשכולות TF-IDF, ייצוא/ייבוא |
| מטריקות | `internal/metrics/` | מוני Prometheus והיסטוגרמות |
| אירועים | `internal/eventbus/` | אפיק אירועים publish/subscribe |
| עלויות | `internal/cost/` | מעקב עלויות LLM עם מגבלות יומיות |
| הגבלת קצב | `internal/ratelimit/` | מגבילי קצב לכל שיחה וגלובליים |
| ממשק קדמי | `ui/` | Vue 3 + Naive UI + UnoCSS SPA |

## סדר הפעלה

רצף ההפעלה מסודר באופן קפדני כדי לעמוד בתלויות:

```
1. ניתוח ארגומנטי CLI, פתרון נתיבי XDG, יצירת תיקיות
2. טיפול בפקודות משנה: init, --version, --doctor (יציאה מוקדמת)
3. טעינת הגדרות: ברירות מחדל → TOML → env → keyring
4. יצירת logger (מצב קונסולה מפנה לקובץ)
5. פתיחת SQLite, הרצת מיגרציות
6. אתחול קטלוג i18n (אחרי מיגרציות, לפני מיומנויות)
7. אתחול משתמש מנהל (לפני backfill)
8. BackfillUserIDs (שיוך נתונים ישנים למשתמשים)
9. יצירת config store, טעינת דריסות DB
10. בדיקת שער מצב התקנה (אין LLM + אין אשף = התקנה בלבד)
11. אימות הגדרות
12. יצירת שירות אימות
13. אתחול מופעי ערוצים
14. יצירת ספק הטבעות ONNX (אופציונלי)
15. בניית שרשרת ספקי LLM (Claude → retry → fallback → cache → router)
16. רישום כל המיומנויות (ללא תנאי — מוגבל ביכולות)
17. יצירת עוזר
18. חיווט אפיק אירועים (טעינת הגדרות, מטריקות, עלויות, התראות)
19. שידור חוזר של דריסות DB (טעינה חמה עבור הרשאות שהוגדרו בלוח הבקרה)
20. יצירת מנהל ערוצים, מתזמן, worker
21. הפעלת מתזמן, worker, לולאת הרצת עוזר
22. הפעלת שרת לוח בקרה
23. הפעלת כל הערוצים
24. המתנה לאות כיבוי
```

## כיבוי מסודר (7 שלבים)

```
1. עצירת כל הערוצים (הפסקת קבלת הודעות חדשות)
2. המתנה ל-goroutines רקע של העוזר
3. המתנה ל-backfill הטבעות
4. סגירת ספק ONNX
5. כיבוי אפיק אירועים (המתנה ל-handlers אסינכרוניים)
6. המתנה למתזמן/worker/לוח בקרה (timeout של 10 שניות)
7. סגירת חיבור SQLite (אחרון)
```

## זרימת הודעות

כאשר משתמש שולח הודעה, זהו נתיב הביצוע המלא:

```
המשתמש מקליד "remember that I love Go"
    │
    ▼
ערוץ (Telegram/WebChat/Console)
    │ בונה IncomingMessage עם שדות ספציפיים לפלטפורמה
    │ מגדיר מסכת ביטים ChannelCaps (סטרימינג, markdown וכו')
    ▼
UserResolver (Telegram/Console בלבד)
    │ ממפה זהות פלטפורמה → UUID של iulita
    │ רושם משתמשים חדשים אוטומטית אם מותר
    ▼
מנהל ערוצים
    │ מנתב ל-Assistant.HandleMessage
    ▼
עוזר — שלב 1: הגדרת הקשר
    │ timeout, תפקיד משתמש, locale, caps → הקשר
    │ בדיקת אישור ממתין → ביצוע אם אושר
    ▼
עוזר — שלב 2: העשרה
    │ שמירת הודעה ל-DB
    │ רקע: TechFactAnalyzer (קירילית/לטינית, אורך הודעה)
    │ שליחת אירוע סטטוס "מעבד"
    ▼
עוזר — שלב 3: היסטוריה ודחיסה
    │ טעינת 50 הודעות אחרונות
    │ אם טוקנים > 80% חלון הקשר → דחיסת החצי הישן
    ▼
עוזר — שלב 4: נתוני הקשר
    │ טעינת הנחיה, עובדות אחרונות, תובנות רלוונטיות
    │ חיפוש היברידי: FTS5 + וקטורי ONNX + דירוג MMR
    │ טעינת tech facts (פרופיל משתמש)
    │ פתרון אזור זמן
    ▼
עוזר — שלב 5: בניית פרומפט
    │ פרומפט סטטי = בסיס + פרומפטים של מיומנויות (נשמר במטמון Claude)
    │ פרומפט דינמי = זמן + הנחיות + פרופיל + עובדות + תובנות + שפה
    ▼
עוזר — שלב 6: זיהוי כלי כפוי
    │ מילת מפתח "remember" → ForceTool = "remember"
    ▼
עוזר — שלב 7: לולאה סוכנית (מקסימום 10 איטרציות)
    │ קריאה ל-LLM (סטרימינג אם אין כלים, אחרת רגיל)
    │ בגלישת הקשר → דחיסה כפויה → ניסיון חוזר פעם אחת
    │ אם קריאות כלים:
    │   ├── בדיקת רמת אישור
    │   ├── הפעלת מיומנות
    │   ├── צבירה ב-ToolExchanges
    │   └── איטרציה הבאה
    │ אם אין קריאות כלים → החזרת תשובה
    ▼
ביצוע מיומנות (למשל RememberSkill)
    │ בדיקת כפילויות דרך חיפוש FTS
    │ שמירה ל-SQLite → טריגר FTS מופעל
    │ רקע: הטבעת ONNX → fact_vectors
    ▼
התשובה נשלחת חזרה דרך הערוץ למשתמש
```

## ממשקים מרכזיים

### Provider (LLM)

```go
type Provider interface {
    Complete(ctx context.Context, req Request) (Response, error)
}

type StreamingProvider interface {
    Provider
    CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error)
}

type EmbeddingProvider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimensions() int
}
```

### Skill

```go
type Skill interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage  // nil for text-only skills
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

ממשקים אופציונליים: `CapabilityAware`, `ConfigReloadable`, `ApprovalDeclarer`.

### Channel

```go
type InputChannel interface {
    Start(ctx context.Context, handler MessageHandler) error
}

type MessageHandler func(ctx context.Context, msg IncomingMessage) (string, error)

type StreamingSender interface {
    MessageSender
    StartStream(ctx context.Context, chatID string, replyTo int) (editFn, doneFn func(string), err error)
}
```

### Storage

```go
type Repository interface {
    // הודעות
    SaveMessage(ctx, msg) error
    GetHistory(ctx, chatID, limit) ([]ChatMessage, error)

    // זיכרון
    SaveFact(ctx, fact) error
    SearchFacts(ctx, chatID, query, limit) ([]Fact, error)
    SearchFactsByUser(ctx, userID, query, limit) ([]Fact, error)
    SearchFactsHybridByUser(ctx, userID, query, queryVec, limit) ([]Fact, error)

    // משימות
    CreateTask(ctx, task) error
    ClaimTask(ctx, workerID, capabilities) (*Task, error)

    // ... 60+ שיטות בסך הכל
}
```

## אפיק אירועים

אפיק האירועים (`internal/eventbus/`) מממש תבנית publish/subscribe מטויפת. אירועים זורמים בין רכיבים ללא צימוד ישיר:

| אירוע | מפרסם | מנויים |
|-------|-----------|-------------|
| `MessageReceived` | עוזר | מטריקות, רכזת WebSocket |
| `ResponseSent` | עוזר | מטריקות, רכזת WebSocket |
| `LLMUsage` | עוזר | מטריקות, מעקב עלויות |
| `SkillExecuted` | עוזר | מטריקות |
| `TaskCompleted` | Worker | רכזת WebSocket |
| `TaskFailed` | Worker | רכזת WebSocket |
| `FactSaved` | אחסון | רכזת WebSocket |
| `InsightCreated` | אחסון | רכזת WebSocket |
| `ConfigChanged` | Config store | מטפל טעינת הגדרות → מיומנויות |

## שרשרת ספקי LLM

ספקים מורכבים כמעטרים (decorators):

```
Claude Provider
    └→ Retry Provider (3 ניסיונות, backoff אקספוננציאלי, 429/5xx)
        └→ Fallback Provider (Claude → OpenAI)
            └→ Caching Provider (מפתח SHA-256, TTL 60 דקות)
                └→ Routing Provider (ניתוב מבוסס RouteHint)
                    └→ Classifying Provider (מסווג Ollama → בחירת מסלול)
```

עבור ספקים שלא תומכים בקריאת כלים מקורית (Ollama, OpenAI), העטיפה `XMLToolProvider` מזריקה הגדרות כלים כ-XML בפרומפט המערכת ומפרשת קריאות כלים מ-XML בתשובה.

## היקף נתונים

כל הנתונים מוגבלים לפי `user_id` לשיתוף בין ערוצים:

```
User (iulita UUID)
    ├── user_channels (חיבור Telegram, חיבור WebChat, ...)
    ├── chat_messages (מכל הערוצים)
    ├── facts (משותפות בין ערוצים)
    ├── insights (משותפות בין ערוצים)
    ├── directives (לכל משתמש)
    ├── tech_facts (פרופיל התנהגותי)
    ├── reminders
    └── todo_items
```

משתמש שמשוחח ב-Telegram יכול להיזכר בעובדות שאחסן דרך ה-Console TUI, מכיוון ששני הערוצים מפנים לאותו `user_id`.

## מבנה הפרויקט

```
cmd/iulita/              # נקודת כניסה, חיווט DI, כיבוי מסודר
internal/
  assistant/             # תזמורן (לולאת LLM, זיכרון, דחיסה, אישורים)
  channel/
    console/             # bubbletea TUI
    telegram/            # בוט Telegram
    webchat/             # צ'אט אינטרנט WebSocket
  channelmgr/            # מנהל מחזור חיי ערוצים
  config/                # הגדרות TOML + env + keyring, אשף התקנה
  domain/                # מודלי דומיין
  auth/                  # אימות JWT + bcrypt
  i18n/                  # בינלאומיות (6 שפות, קטלוגי TOML)
  llm/                   # ספקי LLM (Claude, Ollama, OpenAI, ONNX)
  scheduler/             # תור משימות (מתזמן + worker)
  skill/                 # מימושי מיומנויות
  skillmgr/              # מנהל מיומנויות חיצוניות (ClawhHub, URL, מקומי)
  storage/sqlite/        # מאגר SQLite, FTS5, וקטורים, מיגרציות
  dashboard/             # GoFiber REST API + Vue SPA
  web/                   # חיפוש אינטרנט (Brave, DuckDuckGo, הגנת SSRF)
  memory/                # אשכולות TF-IDF, ייצוא/ייבוא
  eventbus/              # אפיק אירועים publish/subscribe
  cost/                  # מעקב עלויות LLM
  metrics/               # מטריקות Prometheus
  ratelimit/             # הגבלת קצב
  notify/                # התראות push
ui/                      # ממשק Vue 3 + Naive UI + UnoCSS
skills/                  # קבצי מיומנויות טקסט (Markdown)
docs/                    # תיעוד
```
