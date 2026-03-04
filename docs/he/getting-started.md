# תחילת העבודה

## סקירה כללית

Iulita הוא עוזר AI אישי שלומד מהנתונים האמיתיים שלך, לא מהזיות. הוא שומר רק עובדות מאומתות שאתה משתף במפורש, בונה תובנות על ידי הצלבת הנתונים בפועל, ולעולם אינו ממציא דברים שהוא לא יודע.

**קונסולה תחילה**: מפעיל ממשק TUI במסך מלא כברירת מחדל. פועל גם כשרת ללא ממשק גרפי עם Telegram, Web Chat ולוח בקרה מבוסס דפדפן.

## התקנה

### אפשרות 1: הורדת קובץ בינארי מוכן

הורד את הגרסה האחרונה מ-[GitHub Releases](https://github.com/iulita-ai/iulita/releases/latest):

```bash
# macOS (Apple Silicon)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-darwin-arm64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-darwin-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# Linux (x86_64)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-linux-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/
```

### אפשרות 2: בנייה מקוד מקור

**דרישות מוקדמות**: Go 1.25+, Node.js 22+, npm

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build
```

פעולה זו בונה את ממשק Vue 3 ואת הקובץ הבינארי של Go. הפלט נמצא ב-`./bin/iulita`.

לבניית הקובץ הבינארי של Go בלבד (דילוג על הממשק הקדמי):

```bash
make build-go
```

### אפשרות 3: Docker

```bash
cp config.toml.example config.toml
# ערוך את config.toml — הגדר לפחות claude.api_key
mkdir -p data
docker compose up -d
```

תמונה מוכנה מראש:

```yaml
services:
  iulita:
    image: ghcr.io/iulita-ai/iulita:latest
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/app/data
      - ./config.toml:/app/config.toml:ro
```

בהפעלה ראשונה ללא הגדרות, השרת מתחיל ב**מצב התקנה** — אשף דפדפני ב-`http://localhost:8080` ילווה אותך דרך בחירת ספק, הגדרת תכונות וייבוא TOML.

## הפעלה ראשונה

### אשף התקנה אינטראקטיבי

```bash
iulita init
```

האשף מדריך אותך דרך:
1. **בחירת ספק LLM** — Claude (מומלץ), OpenAI או Ollama
2. **הזנת מפתח API** — נשמר בצורה מאובטחת במחזיק המפתחות של המערכת (macOS Keychain, Linux SecretService)
3. **אינטגרציות אופציונליות** — טוקן בוט Telegram, הגדרות פרוקסי, ספק הטבעות (embeddings)
4. **בחירת מודל** — שולף באופן דינמי את המודלים הזמינים מהספק שנבחר

סודות נשמרים במחזיק המפתחות של מערכת ההפעלה כאשר זמין, עם נפילה לקובץ מוצפן ב-`~/.config/iulita/encryption.key`.

### הפעלת ממשק TUI (מצב ברירת מחדל)

```bash
iulita
```

פעולה זו מפעילה את ממשק ה-TUI האינטראקטיבי במסך מלא. הקלד הודעות, השתמש ב-`/help` לפקודות זמינות.

**פקודות קונסולה:**
| פקודה | תיאור |
|---------|-------------|
| `/help` | הצג פקודות זמינות |
| `/status` | הצג מספר מיומנויות, עלות יומית, טוקנים בסשן |
| `/compact` | דחיסה ידנית של היסטוריית השיחה |
| `/clear` | נקה היסטוריית שיחה בזיכרון |
| `/quit` / `/exit` | צא מהאפליקציה |

**קיצורי מקלדת:**
- `Enter` — שלח הודעה
- `Ctrl+C` — יציאה
- `Shift+Enter` — שורה חדשה בהודעה

### הפעלת מצב שרת

להרצה כשירות רקע עם Telegram, Web Chat ולוח בקרה:

```bash
iulita --server
```

או באופן שקול:
```bash
iulita -d
```

לוח הבקרה נגיש ב-`http://localhost:8080` (ניתן להגדרה דרך `server.address`).

## הגדרות

כל ההגדרות נמצאות ב-`config.toml` (אופציונלי — התקנה מקומית ללא הגדרות עובדת עם מפתח API בלבד במחזיק המפתחות). ניתן לדרוס כל אפשרות דרך משתני סביבה עם הקידומת `IULITA_`.

### מיקומי קבצים (תואם XDG)

| פלטפורמה | הגדרות | נתונים | מטמון | לוגים |
|----------|--------|------|-------|------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

דרוס את כל הנתיבים באמצעות משתנה הסביבה `IULITA_HOME`.

### משתני סביבה עיקריים

| משתנה | תיאור |
|----------|-------------|
| `IULITA_CLAUDE_API_KEY` | מפתח API של Anthropic (נדרש עבור Claude) |
| `IULITA_TELEGRAM_TOKEN` | טוקן בוט Telegram |
| `IULITA_CLAUDE_MODEL` | מזהה מודל Claude |
| `IULITA_STORAGE_PATH` | נתיב מסד נתונים SQLite |
| `IULITA_SERVER_ADDRESS` | כתובת האזנה של לוח הבקרה (`:8080`) |
| `IULITA_PROXY_URL` | פרוקסי HTTP/SOCKS5 לכל הבקשות |
| `IULITA_JWT_SECRET` | מפתח חתימת JWT (נוצר אוטומטית אם לא מוגדר) |
| `IULITA_HOME` | דרוס את כל נתיבי XDG |

ראה [`config.toml.example`](../../config.toml.example) לעיון מלא עם כל הגדרות המיומנויות.

## הפניית CLI

| פקודה / דגל | תיאור |
|----------------|-------------|
| `iulita` | הפעל ממשק TUI אינטראקטיבי (ברירת מחדל) |
| `iulita --server` / `-d` | הפעל כשרת ללא ממשק |
| `iulita init` | אשף התקנה אינטראקטיבי |
| `iulita init --print-defaults` | הדפס config.toml ברירת מחדל |
| `iulita --doctor` | הפעל בדיקות אבחון |
| `iulita --version` / `-v` | הדפס גרסה וצא |

## אימות מהיר

לאחר ההתקנה, ודא שהכל עובד:

```bash
# בדוק אבחון
iulita --doctor

# הפעל TUI
iulita

# הקלד: "remember that my favorite color is blue"
# ואז: "what is my favorite color?"
```

אם העוזר מזכיר נכון "blue", הזיכרון עובד מקצה לקצה.

## צעדים הבאים

- [ארכיטקטורה](architecture.md) — הבן כיצד המערכת בנויה
- [זיכרון ותובנות](memory-and-insights.md) — כיצד אחסון עובדות והצלבות עובד
- [ערוצים](channels.md) — הגדר Telegram, Web Chat או התאם את ה-TUI
- [מיומנויות](skills.md) — חקור את 20+ הכלים הזמינים
- [הגדרות](configuration.md) — צלילה עמוקה לכל אפשרויות ההגדרה
- [פריסה](deployment.md) — Docker, Kubernetes והגדרת סביבת ייצור
