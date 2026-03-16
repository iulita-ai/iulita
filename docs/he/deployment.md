# פריסה

## התקנה מקומית

### קובץ בינארי

```bash
# הורדה והתקנה
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-linux-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# התקנה
iulita init        # אשף אינטראקטיבי
iulita             # הפעלת TUI (ברירת מחדל)
iulita --server    # מצב שרת ללא ממשק
```

### בנייה מקוד מקור

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build         # ממשק קדמי + קובץ בינארי Go → ./bin/iulita
make build-go      # קובץ בינארי Go בלבד (דילוג על בניית ממשק קדמי)
```

**דרישות מוקדמות**: Go 1.25+, Node.js 22+, npm

## Docker

### docker-compose.yml

```yaml
services:
  iulita:
    image: ghcr.io/iulita-ai/iulita:latest
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/app/data
      - ./config.toml:/app/config.toml:ro
      - ./skills:/app/skills:ro
    restart: unless-stopped
```

### הפעלה ראשונה (אשף דפדפני)

ללא `config.toml`, השרת מתחיל ב**מצב התקנה**:

1. נווט ל-`http://localhost:8080`
2. השלם את אשף 5 השלבים:
   - ברוכים הבאים / ייבוא TOML קיים
   - בחירת ספק LLM
   - הגדרות (מפתחות API, מודל)
   - מתגי תכונות
   - השלמה
3. האשף שומר הגדרות למסד הנתונים
4. יוצר sentinel `db_managed` (משבית טעינת TOML)

### עם קובץ הגדרות

```bash
cp config.toml.example config.toml
# ערוך config.toml — הגדר לפחות claude.api_key
mkdir -p data
docker compose up -d
```

### Dockerfile (רב-שלבי)

```
שלב 1 (ui-builder): node:22-alpine
    → npm ci + npm run build

שלב 2 (go-builder): golang:1.25-alpine
    → CGO_ENABLED=1 (נדרש עבור SQLite)
    → מעתיק dist של UI לפני בניית Go

שלב 3 (runtime): alpine:3.21
    → ca-certificates + tzdata
    → משתמש לא-root "iulita" (UID 1000)
    → חושף פורט 8080
    → Entrypoint: iulita --server
```

**Volume**: `/app/data` למסד נתונים SQLite ומטמון מודל ONNX.

## משתני סביבה

כל מפתחות ההגדרה ממופים למשתני סביבה:

```bash
# נדרש
IULITA_CLAUDE_API_KEY=sk-ant-...

# אופציונלי
IULITA_TELEGRAM_TOKEN=123456:ABC...
IULITA_STORAGE_PATH=/app/data/iulita.db
IULITA_SERVER_ADDRESS=:8080
IULITA_PROXY_URL=socks5://proxy:1080
IULITA_JWT_SECRET=your-secret-here
IULITA_CLAUDE_MODEL=claude-sonnet-4-5-20250929
```

## Reverse Proxy

### nginx

```nginx
server {
    listen 443 ssl;
    server_name iulita.example.com;

    ssl_certificate /etc/ssl/certs/iulita.crt;
    ssl_certificate_key /etc/ssl/private/iulita.key;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # תמיכת WebSocket
    location /ws {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }

    location /ws/chat {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }
}
```

### Caddy

```txt
iulita.example.com {
    reverse_proxy localhost:8080
}
```

Caddy מטפל בשדרוג WebSocket באופן אוטומטי.

## בדיקות בריאות

### אבחון CLI

```bash
iulita --doctor
```

בודק:
- נגישות קובץ הגדרות
- קישוריות מסד נתונים
- נגישות ספק LLM
- זמינות keyring
- סטטוס מודל הטבעות

### ניטור בריאות Telegram

ערוץ ה-Telegram קורא `GetMe()` כל 60 שניות. כשלונות רצופים נרשמים. זה מזהה בעיות רשת וביטולי טוקנים.

## ניטור

### מטריקות Prometheus

הפעל בהגדרות:

```toml
[metrics]
enabled = true
address = ":9090"
```

מטריקות מרכזיות:
- `iulita_llm_requests_total` — נפח קריאות LLM לפי ספק/סטטוס
- `iulita_llm_cost_usd_total` — עלות מצטברת
- `iulita_skill_executions_total` — דפוסי שימוש במיומנויות
- `iulita_messages_total` — נפח הודעות (נכנסות/יוצאות)
- `iulita_cache_hits_total` — יעילות מטמון

### בקרת עלויות

```toml
[cost]
daily_limit_usd = 10.0  # הפסק קריאות LLM כשהעלות היומית מגיעה ל-$10
```

עלות נעקבת בזיכרון (מתאפסת יומית) ונשמרת לטבלת `usage_stats`.

## גיבוי

### מסד נתונים

מסד הנתונים SQLite הוא מקור האמת היחיד. גבה את הקובץ ב-`{DataDir}/iulita.db`:

```bash
# העתקה פשוטה (בטוח עם מצב WAL כשאין כתיבות)
cp ~/.local/share/iulita/iulita.db backup/

# באמצעות API גיבוי SQLite (בטוח במהלך כתיבות)
sqlite3 ~/.local/share/iulita/iulita.db ".backup backup/iulita.db"
```

### הגדרות

אם משתמשים בהגדרות מבוססות קובץ:
```bash
cp ~/.config/iulita/config.toml backup/
```

אם משתמשים בהגדרות מנוהלות DB (אשף Docker):
- ההגדרות מאוחסנות בטבלת `config_overrides` בתוך מסד הנתונים
- גיבוי ה-DB כולל את ההגדרות

### סודות

סודות ב-keyring **לא** כלולים בגיבויי קבצים. ייצא אותם:
```bash
export IULITA_CLAUDE_API_KEY=$(security find-generic-password -s iulita -a claude-api-key -w)  # macOS
```

## יעדי Makefile

| יעד | תיאור |
|--------|-------------|
| `make build` | בניית ממשק קדמי + קובץ בינארי Go |
| `make build-go` | קובץ בינארי Go בלבד |
| `make ui` | בניית Vue SPA בלבד |
| `make run` | בנייה + הפעלת console TUI |
| `make console` | הפעלת TUI (go run, ללא בנייה) |
| `make server` | בנייה + הפעלת שרת ללא ממשק |
| `make dev` | מצב פיתוח: שרת פיתוח Vue + שרת Go |
| `make test` | הפעלת כל הבדיקות (Go + ממשק קדמי) |
| `make test-go` | בדיקות Go בלבד |
| `make test-ui` | בדיקות ממשק קדמי בלבד |
| `make test-coverage` | כיסוי לשניהם |
| `make tidy` | go mod tidy |
| `make clean` | הסרת תוצרי בנייה |
| `make check-secrets` | הפעלת סריקת gitleaks |
| `make setup-hooks` | התקנת pre-commit hooks |
| `make release` | תיוג ודחיפת release |

## פיתוח

### פיתוח עם טעינה חמה

```bash
make dev
```

פעולה זו מפעילה:
1. שרת פיתוח Vue עם HMR על פורט 5173
2. שרת Go עם דגל `--server`

שרת הפיתוח של Vue מעביר קריאות API לשרת Go.

### הפעלת בדיקות

```bash
make test              # כל הבדיקות
make test-go           # בדיקות Go עם race detector
make test-ui           # Vitest
make test-coverage     # דוחות כיסוי
```

### Pre-commit Hooks

```bash
make setup-hooks
```

מתקין git pre-commit hook שמפעיל `gitleaks detect` כדי למנוע commit בטעות של סודות.
