# Deployment

## Local Installation

### Binary

```bash
# Download and install
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-linux-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# Setup
iulita init        # interactive wizard
iulita             # launch TUI (default)
iulita --server    # headless server mode
```

### Build from Source

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build         # frontend + Go binary → ./bin/iulita
make build-go      # Go binary only (skip frontend rebuild)
```

**Prerequisites**: Go 1.25+, Node.js 22+, npm

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

### First Run (Web Wizard)

Without a `config.toml`, the server starts in **setup mode**:

1. Navigate to `http://localhost:8080`
2. Complete the 5-step wizard:
   - Welcome / Import existing TOML
   - LLM provider selection
   - Configuration (API keys, model)
   - Feature toggles
   - Complete
3. The wizard saves config to the database
4. Creates `db_managed` sentinel (disables TOML loading)

### With Config File

```bash
cp config.toml.example config.toml
# Edit config.toml — set claude.api_key at minimum
mkdir -p data
docker compose up -d
```

### Dockerfile (Multi-Stage)

```
Stage 1 (ui-builder): node:22-alpine
    → npm ci + npm run build

Stage 2 (go-builder): golang:1.25-alpine
    → CGO_ENABLED=1 (required for SQLite)
    → Copies UI dist before Go build

Stage 3 (runtime): alpine:3.21
    → ca-certificates + tzdata
    → Non-root user "iulita" (UID 1000)
    → Exposes port 8080
    → Entrypoint: iulita --server
```

**Volume**: `/app/data` for SQLite database and ONNX model cache.

## Environment Variables

All config keys map to environment variables:

```bash
# Required
IULITA_CLAUDE_API_KEY=sk-ant-...

# Optional
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

    # WebSocket support
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

Caddy handles WebSocket upgrade automatically.

## Health Checks

### CLI Diagnostics

```bash
iulita --doctor
```

Checks:
- Config file accessibility
- Database connectivity
- LLM provider reachability
- Keyring availability
- Embedding model status

### Telegram Health Monitor

The Telegram channel calls `GetMe()` every 60 seconds. Consecutive failures are logged. This detects network issues and token revocations.

## Monitoring

### Prometheus Metrics

Enable in config:

```toml
[metrics]
enabled = true
address = ":9090"
```

Key metrics:
- `iulita_llm_requests_total` — LLM call volume by provider/status
- `iulita_llm_cost_usd_total` — cumulative cost
- `iulita_skill_executions_total` — skill usage patterns
- `iulita_messages_total` — message volume (in/out)
- `iulita_cache_hits_total` — cache effectiveness

### Cost Controls

```toml
[cost]
daily_limit_usd = 10.0  # stop LLM calls when daily cost reaches $10
```

Cost is tracked in-memory (resets daily) and persisted to `usage_stats` table.

## Backup

### Database

The SQLite database is the single source of truth. Back up the file at `{DataDir}/iulita.db`:

```bash
# Simple copy (safe with WAL mode when no writes are happening)
cp ~/.local/share/iulita/iulita.db backup/

# Using SQLite backup API (safe during writes)
sqlite3 ~/.local/share/iulita/iulita.db ".backup backup/iulita.db"
```

### Config

If using file-based config:
```bash
cp ~/.config/iulita/config.toml backup/
```

If using DB-managed config (Docker wizard):
- Config is stored in `config_overrides` table within the database
- Backing up the DB includes the config

### Secrets

Secrets in the keyring are **not** included in file backups. Export them:
```bash
export IULITA_CLAUDE_API_KEY=$(security find-generic-password -s iulita -a claude-api-key -w)  # macOS
```

## Makefile Targets

| Target | Description |
|--------|-------------|
| `make build` | Build frontend + Go binary |
| `make build-go` | Go binary only |
| `make ui` | Build Vue SPA only |
| `make run` | Build + launch console TUI |
| `make console` | Run TUI (go run, no build) |
| `make server` | Build + run headless server |
| `make dev` | Dev mode: Vue dev server + Go server |
| `make test` | Run all tests (Go + frontend) |
| `make test-go` | Go tests only |
| `make test-ui` | Frontend tests only |
| `make test-coverage` | Coverage for both |
| `make tidy` | go mod tidy |
| `make clean` | Remove build artifacts |
| `make check-secrets` | Run gitleaks scan |
| `make setup-hooks` | Install pre-commit hooks |
| `make release` | Tag and push release |

## Development

### Hot Reload Development

```bash
make dev
```

This starts:
1. Vue dev server with HMR on port 5173
2. Go server with `--server` flag

The Vue dev server proxies API calls to the Go server.

### Running Tests

```bash
make test              # all tests
make test-go           # Go tests with race detector
make test-ui           # Vitest
make test-coverage     # coverage reports
```

### Pre-commit Hooks

```bash
make setup-hooks
```

Installs a git pre-commit hook that runs `gitleaks detect` to prevent accidental secret commits.
