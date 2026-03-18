# לוח בקרה

לוח הבקרה הוא GoFiber REST API המגיש SPA Vue 3 מוטבע. הוא מספק ממשק אינטרנט לניהול כל ההיבטים של Iulita.

## ארכיטקטורה

```
GoFiber Server
    ├── /api/*          REST API (מאומת JWT)
    ├── /ws             רכזת WebSocket (עדכונים בזמן אמת)
    ├── /ws/chat        ערוץ WebChat (endpoint נפרד)
    └── /*              Vue 3 SPA (מוטבע, ניתוב בצד הלקוח)
```

SPA Vue מוטבע בקובץ הבינארי של Go דרך `//go:embed dist/*` ומוגש עם fallback ל-`index.html` עבור כל הנתיבים הלא מוכרים.

## אימות

| Endpoint | אימות | תיאור |
|----------|------|-------------|
| `POST /api/auth/login` | ציבורי | בדיקת אישורים bcrypt, מחזיר טוקני access + refresh |
| `POST /api/auth/refresh` | ציבורי | אימות טוקן refresh, החזרת טוקן access חדש |
| `POST /api/auth/change-password` | JWT | שינוי סיסמה עצמית |
| `GET /api/auth/me` | JWT | פרופיל משתמש נוכחי |
| `PATCH /api/auth/locale` | JWT | עדכון locale לכל הערוצים |

**פרטי JWT:**
- אלגוריתם: HMAC-SHA256
- TTL טוקן גישה: 24 שעות
- TTL טוקן רענון: 7 ימים
- Claims: `user_id`, `username`, `role`
- סוד: נוצר אוטומטית אם לא מוגדר

## REST API

### Endpoints ציבוריים

| שיטה | נתיב | תיאור |
|--------|------|-------------|
| GET | `/api/system` | מידע מערכת, גרסה, uptime, סטטוס אשף |

### Endpoints משתמש (נדרש JWT)

| שיטה | נתיב | תיאור |
|--------|------|-------------|
| GET | `/api/stats` | ספירות הודעות, עובדות, תובנות, תזכורות |
| GET | `/api/chats` | רשימת כל מזהי שיחות עם ספירות הודעות |
| GET | `/api/facts` | רשימה/חיפוש עובדות (לפי chat_id, user_id, query) |
| PUT | `/api/facts/:id` | עדכון תוכן עובדה |
| DELETE | `/api/facts/:id` | מחיקת עובדה |
| GET | `/api/facts/search` | חיפוש FTS של עובדות |
| GET | `/api/insights` | רשימת תובנות |
| GET | `/api/reminders` | רשימת תזכורות |
| GET | `/api/directives` | קבלת הנחיה לשיחה |
| GET | `/api/messages` | היסטוריית שיחה עם עימוד |
| GET | `/api/skills` | רשימת כל המיומנויות עם סטטוס הפעלה/הגדרות |
| PUT | `/api/skills/:name/toggle` | הפעלה/השבתת מיומנות בזמן ריצה |
| GET | `/api/skills/:name/config` | סכמת הגדרות מיומנות + ערכים נוכחיים |
| PUT | `/api/skills/:name/config/:key` | הגדרת מפתח הגדרות מיומנות (הצפנה אוטומטית לסודות) |
| GET | `/api/techfacts` | פרופיל התנהגותי מקובץ לפי קטגוריה |
| GET | `/api/usage/summary` | שימוש בטוקנים + הערכת עלות |
| GET | `/api/schedulers` | סטטוס משימות מתזמן |
| POST | `/api/schedulers/:name/trigger` | הפעלה ידנית של משימה |

### Endpoints משימות (נדרש JWT)

| שיטה | נתיב | תיאור |
|--------|------|-------------|
| GET | `/api/todos/providers` | רשימת ספקי משימות |
| GET | `/api/todos/today` | משימות להיום |
| GET | `/api/todos/overdue` | משימות באיחור |
| GET | `/api/todos/upcoming` | משימות קרובות (ברירת מחדל 7 ימים) |
| GET | `/api/todos/all` | כל המשימות הלא שלמות |
| GET | `/api/todos/counts` | ספירות היום + באיחור |
| POST | `/api/todos/` | יצירת משימה |
| POST | `/api/todos/sync` | הפעלת סנכרון todo ידני |
| POST | `/api/todos/:id/complete` | השלמת משימה |
| DELETE | `/api/todos/:id` | מחיקת משימה מובנית |

### Endpoints Google Workspace (נדרש JWT)

| שיטה | נתיב | תיאור |
|--------|------|-------------|
| GET | `/api/google/status` | סטטוס חשבון |
| POST | `/api/google/upload-credentials` | העלאת קובץ אישורי OAuth |
| GET | `/api/google/auth` | התחלת זרימת OAuth2 |
| GET | `/api/google/callback` | callback של OAuth2 |
| GET | `/api/google/accounts` | רשימת חשבונות |
| DELETE | `/api/google/accounts/:id` | מחיקת חשבון |
| PUT | `/api/google/accounts/:id` | עדכון חשבון |

### Endpoints מנהל (נדרש תפקיד מנהל)

| שיטה | נתיב | תיאור |
|--------|------|-------------|
| GET/PUT/DELETE | `/api/config/*` | דריסות הגדרות, סכמה, ניפוי |
| GET/POST/PUT/DELETE | `/api/users/*` | CRUD משתמשים + חיבורי ערוצים |
| GET/POST/PUT/DELETE | `/api/channels/*` | CRUD מופעי ערוצים |
| GET/POST/PUT/DELETE | `/api/agent-jobs/*` | CRUD משימות סוכן |
| GET/POST/DELETE | `/api/skills/external/*` | ניהול מיומנויות חיצוניות |
| GET/POST | `/api/wizard/*` | אשף התקנה |
| PUT | `/api/todos/default-provider` | הגדרת ספק משימות ברירת מחדל |

### Endpoints Worker (טוקן Bearer)

| שיטה | נתיב | תיאור |
|--------|------|-------------|
| GET | `/api/tasks/` | רשימת משימות מתזמן |
| GET | `/api/tasks/counts` | ספירות לפי סטטוס |
| POST | `/api/tasks/claim` | תביעת משימה (worker מרוחק) |
| POST | `/api/tasks/:id/start` | סימון משימה כ-running |
| POST | `/api/tasks/:id/complete` | השלמת משימה |
| POST | `/api/tasks/:id/fail` | כישלון משימה |

## רכזת WebSocket

רכזת ה-WebSocket ב-`/ws` מספקת עדכונים בזמן אמת ללקוחות לוח בקרה מחוברים.

### אירועים

| אירוע | מקור | נתונים |
|-------|--------|---------|
| `task.completed` | Worker | פרטי משימה |
| `task.failed` | Worker | משימה + שגיאה |
| `message.received` | עוזר | מטא-דאטה של הודעה |
| `response.sent` | עוזר | מטא-דאטה של תשובה |
| `fact.saved` | אחסון | פרטי עובדה |
| `insight.created` | אחסון | פרטי תובנה |
| `config.changed` | Config store | מפתח + ערך |

אירועים מפורסמים דרך אפיק האירועים באמצעות `SubscribeAsync` (לא חוסם).

### פרוטוקול

```json
// שרת → לקוח
{"type": "task.completed", "payload": {...}}
{"type": "fact.saved", "payload": {...}}
```

## Vue 3 SPA

### ערימה טכנולוגית

- **Vue 3** — Composition API
- **Naive UI** — ספריית רכיבים
- **UnoCSS** — CSS utility-first
- **vue-i18n** — בינלאומיות (6 שפות)
- **vue-router** — ניתוב בצד הלקוח

### תצוגות

| נתיב | רכיב | אימות | תיאור |
|------|-----------|------|-------------|
| `/` | Dashboard | JWT | סקירת סטטיסטיקות, סטטוס מתזמן |
| `/facts` | Facts | JWT | דפדפן עובדות עם חיפוש, עריכה, מחיקה |
| `/insights` | Insights | JWT | רשימת תובנות |
| `/reminders` | Reminders | JWT | רשימת תזכורות |
| `/profile` | TechFacts | JWT | מטא-דאטה פרופיל התנהגותי |
| `/settings` | Settings | JWT | ניהול מיומנויות, עורך הגדרות |
| `/tasks` | Tasks | JWT | לשוניות היום/באיחור/קרוב/הכל |
| `/chat` | Chat | JWT | צ'אט אינטרנט WebSocket |
| `/users` | Users | מנהל | CRUD משתמשים + חיבורי ערוצים |
| `/channels` | Channels | מנהל | CRUD מופעי ערוצים |
| `/agent-jobs` | AgentJobs | מנהל | CRUD משימות סוכן |
| `/skills` | ExternalSkills | מנהל | שוק + מיומנויות מותקנות |
| `/setup` | Setup | מנהל | אשף התקנה דפדפני |
| `/config-debug` | ConfigDebug | מנהל | צפייה גולמית בדריסות הגדרות |
| `/login` | Login | ציבורי | טופס התחברות |

### שומרי ניתוב

```javascript
router.beforeEach((to, from, next) => {
    if (to.meta.public) { next(); return }
    if (!isLoggedIn()) { next({ name: 'login' }); return }
    if (to.meta.admin && !isAdmin()) { next({ name: 'dashboard' }); return }
    next()
})
```

### Composables מרכזיים

- `useWebSocket` — WebSocket עם חיבור מחדש אוטומטי ואירועים מטויפים
- `useLocale` — מצב locale ריאקטיבי, זיהוי RTL, סנכרון עם שרת
- `useSkillStatus` — מנתב פריטי סרגל צדדי על סמך זמינות מיומנויות

### ממשק ניהול מיומנויות

תצוגת ההגדרות מספקת:

1. **מתג מיומנות** — הפעלה/השבתת כל מיומנות בזמן ריצה
2. **עורך הגדרות** — הגדרות לכל מיומנות עם:
   - שדות טופס מונחי סכמה
   - הגנה על מפתחות סודיים (ערכים לעולם לא נחשפים ב-API)
   - הצפנה אוטומטית לערכים רגישים
   - טעינה חמה בשמירה

### לוח משימות

תצוגת המשימות מאגדת משימות מכל הספקים:

- **לשונית היום** — משימות שמועדן היום
- **לשונית באיחור** — משימות שעבר מועדן
- **לשונית קרוב** — 7 הימים הבאים
- **לשונית הכל** — כל המשימות הלא שלמות
- **כפתור סנכרון** — מפעיל משימת מתזמן חד-פעמית
- **כפתור יצירה** — משימה חדשה עם בחירת ספק

## מטריקות Prometheus

כאשר מופעלות (`metrics.enabled = true`), מטריקות נחשפות על פורט נפרד:

| מטריקה | סוג | תוויות |
|--------|------|--------|
| `iulita_llm_requests_total` | Counter | provider, model, status |
| `iulita_llm_tokens_input_total` | Counter | provider |
| `iulita_llm_tokens_output_total` | Counter | provider |
| `iulita_llm_request_duration_seconds` | Histogram | provider |
| `iulita_llm_cost_usd_total` | Counter | — |
| `iulita_skill_executions_total` | Counter | skill, status |
| `iulita_task_total` | Counter | type, status |
| `iulita_messages_total` | Counter | direction |
| `iulita_cache_hits_total` | Counter | cache_type |
| `iulita_cache_misses_total` | Counter | cache_type |
| `iulita_active_sessions` | Gauge | — |

מטריקות מאוכלסות על ידי הרשמה לאפיק האירועים (לא חוסם).

## סטטיסטיקת שימוש בטוקנים

### Endpoints של API

| שיטה | נתיב | תיאור |
|--------|------|-------------|
| GET | `/api/usage/summary` | סיכום כולל: טוקנים, עלות, פגיעות מטמון |
| GET | `/api/usage/daily` | פירוט יומי ל-30 הימים האחרונים |
| GET | `/api/usage/by-model` | שימוש מפולח לפי מודל |

### עמוד לוח הבקרה

עמוד Token Usage מציג:

- **כרטיסי KPI** — סך טוקני קלט/פלט, הערכת עלות, אחוז פגיעות מטמון
- **טבלה לפי מודל** — שימוש בטוקנים ועלות עבור כל מודל (Claude Sonnet, Haiku, Ollama וכו')
- **פירוט יומי** — גרף שימוש ל-30 הימים האחרונים

### כישור בצ'אט

כישור `token_stats` זמין דרך הצ'אט (למנהלים בלבד). מאפשר לשאול על סטטיסטיקת שימוש בטוקנים ישירות בשיחה.
