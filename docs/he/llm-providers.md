# ספקי LLM

Iulita תומך במספר ספקי LLM דרך ארכיטקטורה מבוססת מעטרים (decorators). ניתן להרכיב ספקים לשרשראות עם שכבות retry, fallback, מטמון, ניתוב וסיווג.

## ממשק ספק

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

## בקשה / תשובה

### מבנה בקשה

```go
Request {
    StaticSystemPrompt  string          // נשמר במטמון Claude (פרומפטים בסיסיים + מיומנויות)
    SystemPrompt        string          // לכל הודעה (זמן, עובדות, הנחיות)
    History             []ChatMessage   // היסטוריית שיחה
    Message             string          // הודעת המשתמש הנוכחית
    Images              []ImageAttachment
    Documents           []DocumentAttachment
    Tools               []ToolDefinition
    ToolExchanges       []ToolExchange  // סבבי כלים מצטברים בתור זה
    ThinkingBudget      int64           // טוקני חשיבה מורחבת (0 = מושבת)
    ForceTool           string          // כפה קריאת כלי ספציפי
    RouteHint           string          // רמז לספק ניתוב
}
```

**עיצוב מפתח**: פרומפט המערכת מפוצל ל-`StaticSystemPrompt` (יציב, ניתן למטמון) ו-`SystemPrompt` (דינמי, לכל הודעה). ספקים שאינם Claude משתמשים ב-`FullSystemPrompt()` שמשרשר את שניהם.

### מבנה תשובה

```go
Response {
    Content    string
    ToolCalls  []ToolCall
    Usage      Usage {
        InputTokens              int
        OutputTokens             int
        CacheReadInputTokens     int
        CacheCreationInputTokens int
    }
}
```

## ספק Claude

הספק העיקרי, המשתמש ב-`anthropic-sdk-go` הרשמי.

### תכונות

- **מטמון פרומפטים**: `StaticSystemPrompt` מקבל `cache_control: ephemeral` — Claude שומר בלוק זה במטמון בין בקשות, מפחית עלויות טוקני קלט
- **סטרימינג**: `CompleteStream` משתמש ב-API סטרימינג עם עיבוד `ContentBlockDeltaEvent`
- **חשיבה מורחבת**: כש-`ThinkingBudget > 0`, הגדרות חשיבה מתווספות ומקסימום טוקנים עולה
- **ForceTool**: משתמש ב-`ToolChoiceParamOfTool(name)` לכפיית כלי ספציפי (משבית חשיבה — אילוץ API)
- **זיהוי גלישת הקשר**: בודק הודעות שגיאה עבור "prompt is too long" / "context_length_exceeded" ועוטף ב-sentinel `ErrContextTooLarge`
- **תמיכת מסמכים**: קבצי PDF דרך `Base64PDFSourceParam`, קבצי טקסט דרך `PlainTextSourceParam`
- **תמיכת תמונות**: תמונות מקודדות base64 עם סוג מדיה
- **טעינה חמה**: מודל, מקסימום טוקנים ומפתח API ניתנים לעדכון בזמן ריצה דרך `sync.RWMutex`

### מטמון פרומפטים

הפיצול סטטי/דינמי של הפרומפט הוא המפתח לשימוש יעיל ב-Claude:

```
Block 1: StaticSystemPrompt (cache_control: ephemeral)
  ├── פרומפט מערכת בסיסי (פרסונה, הוראות)
  └── פרומפטים של מיומנויות (מכל המיומנויות המופעלות)

Block 2: SystemPrompt (ללא cache control)
  ├── ## Current Time
  ├── ## User Directives
  ├── ## User Profile (tech facts)
  ├── ## Remembered Facts
  ├── ## Insights
  └── ## Language directive (אם לא אנגלית)
```

בלוק 1 נשמר במטמון Claude בין בקשות (עולה `cache_creation_input_tokens` בשימוש ראשון, `cache_read_input_tokens` בפגיעות עוקבות). בלוק 2 משתנה בכל הודעה ואף פעם לא נשמר במטמון.

### סטרימינג

סטרימינג משמש רק כאשר `len(req.Tools) == 0` (העוזר משבית סטרימינג במהלך לולאת שימוש בכלים סוכנית). לולאת אירועי הסטרימינג מעבדת:

- `ContentBlockDeltaEvent` עם `type == "text_delta"` → קורא `callback(chunk)` וצובר
- `MessageStartEvent` → לוכד טוקני קלט + מטריקות מטמון
- `MessageDeltaEvent` → לוכד טוקני פלט

### התאוששות מגלישת הקשר

כאשר Claude API מחזיר שגיאת גלישת הקשר:

1. `isContextOverflowError(err)` עוטף כ-`llm.ErrContextTooLarge`
2. הלולאה הסוכנית של העוזר תופסת דרך `llm.IsContextTooLarge(err)`
3. אם לא נדחס כבר בתור זה: דחיסת היסטוריה כפויה וניסיון חוזר (`i--`)
4. אם כבר נדחס: הפצת השגיאה

### הגדרות

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `claude.api_key` | — | מפתח API של Anthropic (נדרש) |
| `claude.model` | `claude-sonnet-4-5-20250929` | מזהה מודל |
| `claude.max_tokens` | 8192 | מקסימום טוקני פלט |
| `claude.base_url` | — | דריסת כתובת בסיס API |
| `claude.thinking` | 0 | תקציב חשיבה מורחבת (0 = מושבת) |

## ספק Ollama

ספק LLM מקומי לפיתוח ומשימות רקע.

### מגבלות

- **ללא תמיכת כלים** — מחזיר שגיאה אם `len(req.Tools) > 0`
- **ללא סטרימינג** — `CompleteStream` לא מיושם
- משתמש ב-`FullSystemPrompt()` (ללא יתרון מטמון)

### מקרי שימוש

- פיתוח מקומי ללא עלויות API
- משימות delegate ברקע (תרגומים, סיכומים)
- מסווג זול עבור `ClassifyingProvider`

### API

קורא `POST /api/chat` עם הודעות בפורמט תואם OpenAI. `ListModels()` פונה ל-`GET /api/tags` לגילוי מודלים.

### הגדרות

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `ollama.url` | `http://localhost:11434` | כתובת שרת Ollama |
| `ollama.model` | `llama3` | שם מודל |

## ספק OpenAI

לקוח REST תואם OpenAI. עובד עם כל שירות תואם OpenAI (Together AI, Azure וכו').

### מגבלות

- **ללא תמיכת כלים** — כמו Ollama
- משתמש ב-`FullSystemPrompt()`

### הגדרות

| מפתח | ברירת מחדל | תיאור |
|-----|---------|-------------|
| `openai.api_key` | — | מפתח API |
| `openai.model` | `gpt-4` | מזהה מודל |
| `openai.base_url` | `https://api.openai.com/v1` | כתובת בסיס API |

## ספק הטבעות ONNX

מודל הטבעות מקומי טהור ב-Go לחיפוש וקטורי.

- **מודל**: `KnightsAnalytics/all-MiniLM-L6-v2` (384 ממדים)
- **Runtime**: `knights-analytics/hugot` — ONNX טהור ב-Go (ללא CGo)
- **בטיחות תהליכונים**: `sync.Mutex` (צינור hugot אינו thread-safe)
- **מטמון**: מורד פעם אחת ל-`~/.local/share/iulita/models/`
- **נורמליזציה**: וקטורי פלט מנורמלים L2 (מוכנים לדמיון קוסינוס)

ראה [זיכרון ותובנות](memory-and-insights.md#הטבעות) לפרטים על אופן השימוש בהטבעות.

## מעטרי ספקים

### RetryProvider

עוטף כל ספק עם ניסיון חוזר ב-backoff אקספוננציאלי:

- **מקסימום ניסיונות**: 3
- **עיכוב בסיסי**: 500ms
- **עיכוב מקסימלי**: 8s
- **jitter**: מכפיל אקראי 0.5-1.5x
- **קודים שניתן לנסות שוב**: 429, 500, 502, 503, 529 (Anthropic עמוס)
- **לא ניתן לניסיון חוזר**: 4xx (מלבד 429), גלישת הקשר

### FallbackProvider

מנסה ספקים לפי סדר, מחזיר את ההצלחה הראשונה. שימושי לשרשראות חלופה `Claude → OpenAI`.

### CachingProvider

שומר במטמון תשובות LLM לפי hash קלט:

- **מפתח**: SHA-256 של `systemPrefix[:200] + "|" + message`
- **TTL**: 60 דקות (ניתן להגדרה)
- **מקסימום רשומות**: 1000 (פינוי LRU)
- **דילוג**: בקשות עם כלים או חילופי כלים (לא דטרמיניסטי)
- **אחסון**: טבלת SQLite `response_cache`

### CachedEmbeddingProvider

שומר במטמון הטבעות לכל טקסט:

- **מפתח**: SHA-256 של טקסט הקלט
- **מקסימום רשומות**: 10,000 (פינוי LRU)
- **אצווה**: החמצות מטמון מקובצות לקריאת ספק בודדת
- **אחסון**: טבלת SQLite `embedding_cache`

### RoutingProvider

מנתב לספקים ממוינים לפי `req.RouteHint`. מפרש גם קידומת `hint:<name> <message>` בהודעת המשתמש. מעביר `CompleteStream` לספק הנפתר אם הוא `StreamingProvider`.

### ClassifyingProvider

עוטף `RoutingProvider`. בכל בקשה:

1. שולח פרומפט סיווג לספק זול (Ollama): "Classify: simple/complex/creative"
2. מגדיר `RouteHint` על סמך הסיווג
3. מנתב לספק המתאים

נופל לברירת מחדל בשגיאת מסווג.

### XMLToolProvider

לספקים ללא קריאת כלים מקורית (Ollama, OpenAI):

1. מזריק בלוק XML של `<available_tools>` לפרומפט המערכת
2. מוסיף הוראות: "To use a tool, respond with `<tool_use name="..."><input>{...}</input></tool_use>`"
3. מסיר `Tools` מהבקשה
4. מפרש קריאות כלים XML מהתשובה באמצעות regex

## הרכבת שרשרת ספקים

השרשרת נבנית ב-`cmd/iulita/main.go`:

```
Claude Provider
    └→ Retry Provider
        └→ [אופציונלי] Fallback Provider (+ OpenAI)
            └→ [אופציונלי] Caching Provider
                └→ [אופציונלי] Routing Provider
                    └→ [אופציונלי] Classifying Provider (+ Ollama)
```

כל שכבה מתווספת על בסיס תנאי בהתאם להגדרות.
