# מתזמן

המתזמן הוא מערכת דו-רכיבית: **Coordinator** שמייצר משימות לפי לוח זמנים, ו-**Worker** שתובע ומבצע אותן. שניהם משתמשים ב-SQLite כתור משימות.

## ארכיטקטורה

```
Scheduler (Coordinator)
    │ סוקר כל 30 שניות
    │ בודק תזמון משימות מול scheduler_states
    │
    ├── InsightJob (24h) → משימות insight.generate
    ├── InsightCleanupJob (1h) → משימות insight.cleanup
    ├── TechFactsJob (6h) → משימות techfact.analyze
    ├── HeartbeatJob (6h) → משימות heartbeat.check
    ├── RemindersJob (30s) → משימות reminder.fire
    ├── AgentJobsJob (30s) → משימות agent.job
    └── TodoSyncJob (cron שעתי) → משימות todo.sync
           │
           ▼
    טבלת tasks (SQLite)
           │
           ▼
Worker
    │ סוקר כל 5 שניות
    │ תובע משימות באופן אטומי
    │ מעביר למטפלים רשומים
    │
    ├── InsightGenerateHandler
    ├── InsightCleanupHandler
    ├── TechFactAnalyzeHandler
    ├── HeartbeatHandler
    ├── ReminderFireHandler
    ├── AgentJobHandler
    └── TodoSyncHandler
```

## Coordinator

### הגדרת משימה

```go
type JobDefinition struct {
    Name        string
    Interval    time.Duration
    CronExpr    string           // cron סטנדרטי (5 שדות)
    Timezone    string           // אזור זמן IANA עבור cron
    Enabled     bool
    CreateTasks func(ctx) ([]domain.Task, error)
}
```

כל משימה מצהירה על `Interval` קבוע או `CronExpr`. Cron משתמש ב-`robfig/cron/v3` עם תמיכת אזורי זמן.

### לולאת תזמון

1. **חימום**: באתחול ראשון, `NextRun = now + 1 דקה` (תקופת חסד)
2. **טיק** כל 30 שניות:
   - תחזוקה: תביעה מחדש של משימות מיושנות (running > 5 דקות), מחיקת משימות ישנות (> 7 ימים)
   - לכל משימה מופעלת: אם `now >= state.NextRun`, קריאה ל-`CreateTasks`
   - הכנסת משימות דרך `CreateTaskIfNotExists` (אידמפוטנטי לפי `UniqueKey`)
   - עדכון מצב: `LastRun = now`, `NextRun = computeNextRun()`

### הפעלה ידנית

`TriggerJob(name)`:
- מוצא את המשימה לפי שם
- קורא `CreateTasks` עם `Priority = 1` (גבוה)
- מכניס משימות מיידית
- **לא** מעדכן את מצב לוח הזמנים (ההפעלה הרגילה הבאה עדיין מתרחשת)

זמין דרך לוח הבקרה: `POST /api/schedulers/:name/trigger`

## Worker

### תביעת משימות

```
כל 5 שניות:
    לכל חריץ מקביליות זמין:
        ClaimTask(ctx, workerID, capabilities)  // טרנזקציית SQLite אטומית
        אם משימה נתבעה:
            go executeTask(task)
        אחרת:
            break  // אין עוד משימות זמינות
```

`workerID = hostname-pid` (ייחודי לכל תהליך).

### ניתוב מבוסס יכולות

משימות מצהירות על יכולות נדרשות כמחרוזת מופרדת בפסיקים (למשל `"llm,storage"`). רשימת היכולות של ה-worker חייבת להיות על-קבוצה.

**יכולות worker מקומי**: `["storage", "llm", "telegram"]`

**Worker מרוחק**: כל סט יכולות, מאומת דרך `Scheduler.WorkerToken`.

### מחזור חיי משימה

```
pending → claimed (על ידי worker) → running → completed / failed
```

- `ClaimTask`: SELECT + UPDATE אטומי בטרנזקציה
- `StartTask`: הגדרת סטטוס ל-`running`, רישום זמן התחלה
- `CompleteTask`: אחסון תוצאה, פרסום אירוע `TaskCompleted`
- `FailTask`: אחסון שגיאה, פרסום אירוע `TaskFailed`

### API Worker מרוחק

לפריסות מבוזרות, לוח הבקרה חושף REST API:

| Endpoint | שיטה | תיאור |
|----------|--------|-------------|
| `/api/tasks/` | GET | רשימת משימות |
| `/api/tasks/counts` | GET | ספירות לפי סטטוס |
| `/api/tasks/claim` | POST | תביעת משימה |
| `/api/tasks/:id/start` | POST | סימון כ-running |
| `/api/tasks/:id/complete` | POST | השלמה עם תוצאה |
| `/api/tasks/:id/fail` | POST | כישלון עם שגיאה |

מאומת דרך טוקן bearer סטטי (`scheduler.worker_token`).

## משימות מובנות

### יצירת תובנות (`insights`)

- **מרווח**: 24 שעות (ניתן להגדרה דרך `skills.insights.interval`)
- **סוג משימה**: `insight.generate`
- **יכולות**: `llm,storage`
- **תנאי**: שיחה/משתמש חייבים להכיל >= `minFacts` (ברירת מחדל 20) עובדות

**צינור מטפל:**
1. טעינת כל העובדות עבור המשתמש
2. בניית וקטורי TF-IDF (טוקניזציה, bigramים, ניקודי TF-IDF)
3. אשכולות K-means++: `k = sqrt(numFacts / 3)`, מרחק קוסינוס, 20 איטרציות
4. דגימת עד 6 זוגות חוצי-אשכולות (דילוג על זוגות שכבר כוסו)
5. לכל זוג: LLM מייצר תובנה + מנקד איכות (1-5)
6. אחסון תובנות עם איכות >= סף

### ניקוי תובנות (`insight_cleanup`)

- **מרווח**: שעה
- **סוג משימה**: `insight.cleanup`
- **יכולות**: `storage`

מוחק תובנות כאשר `expires_at < now`. TTL ברירת מחדל הוא 30 יום.

### ניתוח Tech Facts (`techfacts`)

- **מרווח**: 6 שעות (ניתן להגדרה)
- **סוג משימה**: `techfact.analyze`
- **יכולות**: `llm,storage`
- **תנאי**: 10+ הודעות עם 5+ מהמשתמש

**מטפל**: שולח הודעות משתמש ל-LLM ומבקש JSON מובנה: `[{category, key, value, confidence}]`. קטגוריות כוללות נושאים, סגנון תקשורת ודפוסי התנהגות. Upsert לטבלת `tech_facts`.

### Heartbeat (`heartbeat`)

- **מרווח**: 6 שעות (ניתן להגדרה)
- **סוג משימה**: `heartbeat.check`
- **יכולות**: `llm,storage,telegram`

**מטפל**: אוסף עובדות אחרונות, תובנות ותזכורות ממתינות. שואל LLM אם הודעת בדיקה מוצדקת. אם התשובה אינה `HEARTBEAT_OK`, שולח את ההודעה למשתמש.

### תזכורות (`reminders`)

- **מרווח**: 30 שניות
- **סוג משימה**: `reminder.fire`
- **יכולות**: `telegram,storage`

**מטפל**: מעצב תזכורת עם שעה מקומית, שולח דרך `MessageSender`, מסמן כ-fired.

### Agent Jobs (`agent_jobs`)

- **מרווח**: 30 שניות
- **סוג משימה**: `agent.job`
- **יכולות**: `llm`

סוקר `GetDueAgentJobs(now)` למשימות LLM מתוזמנות מוגדרות משתמש. מעדכן `next_run` מיידית (לפני ביצוע) כדי למנוע כפילויות.

**מטפל**: קורא `provider.Complete` עם הפרומפט שהוגדר על ידי המשתמש. אופציונלית מעביר את התוצאה לצ'אט מוגדר.

### סנכרון Todo (`todo_sync`)

- **Cron**: `0 * * * *` (שעתי)
- **סוג משימה**: `todo.sync`
- **יכולות**: `storage`

**מטפל**: עובר על כל מופעי `TodoProvider` הזמינים (Todoist, Google Tasks, Craft). לכל אחד: `FetchAll` → upsert ל-`todo_items` → מחיקת רשומות מיושנות.

## Agent Jobs (מוגדרי משתמש)

משתמשים יכולים ליצור משימות LLM מתוזמנות דרך לוח הבקרה:

```json
{
  "name": "Daily Summary",
  "prompt": "Summarize my recent facts and insights",
  "cron_expr": "0 9 * * *",
  "delivery_chat_id": "123456789"
}
```

שדות:
- `name` — שם תצוגה
- `prompt` — פרומפט LLM לביצוע
- `cron_expr` או `interval` — תזמון
- `delivery_chat_id` — לאן לשלוח את התוצאה (אופציונלי)

ניהול דרך לוח הבקרה: `GET/POST/PUT/DELETE /api/agent-jobs/`
