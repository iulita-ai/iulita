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

**Prerequisites**: Go 1.26+ (toolchain 1.27.4), Node.js 22+, npm

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

### Image security checks

The source Dockerfiles use Alpine 3.24.4 and Go toolchain 1.27.4. They pin base-image digests and upgrade installed APK packages during a fresh build. The release workflow pulls bases and disables build-layer reuse so a cached package-install step cannot hide new security patches. Both the app and SSH worker keep their existing non-root users and data paths. The separate `Dockerfile.mobile-gateway` refreshes the unprivileged nginx gateway and its OS packages.

Container Security checks run on pull requests, main, weekly and on demand. They exercise the app on amd64, and the worker and gateway on amd64 and arm64. Runtime fixtures cover SQLite writes/reads, offline preloaded embeddings, PDF extraction, OGG/Opus-to-MP3 conversion, worker TLS/HMAC with isolated SSH execution and durable replay rejection, and gateway TLS/HTTP2 routes. These checks use disposable data and keys.

A pinned Trivy scanner reads each exact image archive without a Docker socket or publication credentials. Full findings, CycloneDX SBOMs and database metadata are retained as workflow artifacts. Fixable HIGH/CRITICAL findings and scanner failures block publication; unfixed findings remain visible in the full report. A successful scan is not a claim that every reported vulnerability is exploitable, or that every possible vulnerability is known.

On a release tag, a separate publisher verifies all five archive receipts and checksums, then publishes those same images without rebuilding. Worker and gateway manifests are published before the app. Scheduled checks do not silently replace published version tags. Deployment-specific pins for the SSH worker or mobile gateway need separate updates; updating only the application does not update them. Host-kernel patches remain a separate operation.

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
Stage 1 (ui-builder): node:22-alpine3.24
    → npm ci + npm run build

Stage 2 (go-builder): golang:1.27.4-alpine3.24
    → CGO_ENABLED=1 (container build; SQLite itself is pure Go)
    → Copies UI dist before Go build

Stage 3 (runtime): alpine:3.24.4
    → ca-certificates + tzdata + ffmpeg + poppler-utils
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
IULITA_CLAUDE_MODEL=claude-sonnet-4-6
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


## Upgrade and optional remote API

Review [the release guide](release-notes.md), including the v0.39 database
rollback requirement. Retain a consistent SQLite backup and configuration key.
The runtime image preloads ONNX models under `/opt/iulita/models`, outside the
writable data volume, and includes ffmpeg for voice format conversion.

The mobile/CLI/service backend is available from v0.48.0 and disabled by default.
It requires its own TLS listeners, realm keys, OAuth store and explicit offline
initialization. UI/Vision flags remain blocked by the native acceptance gate.
See [the API operator guide](mobile-api.md). Do not route it through legacy
WebChat or reuse dashboard/worker tokens. Keep mobile DBs and realm keys in the
backup plan; restoration requires an explicit signing-epoch invalidation.


## Telegram upgrade and readiness

Builds with the new Telegram integration enable `day_widgets` and `day_actions` by default even when an old configuration omits them; explicit false is retained. Back up the database, upgrade the binary/image and restart after changing these switches. Do not rename topic routes or infer a new bot from an alias: old unpinned routes need explicit `/topic bind` before automatic destructive cleanup.

The source-only `go run ./cmd/telegram-readiness` command reads the token from an environment variable and reports public identity, active usernames and topic capability. `-token-env` selects a secret variable; `-test-environment` requires separate Telegram test-server credentials. It neither polls updates nor sends/edits messages, and does not prove client rendering or task mutation permissions. Only one polling instance should consume a bot's updates. See [Telegram operation](telegram.md#operation-and-troubleshooting).

Manual-deletion reconciliation is eventual and uses bounded known-route checks, including narrowly constrained validation probes for observed Bot API responses; Telegram does not expose a documented topic-deletion notification or get-topic probe. Unknown/changed detection behavior does not authorize new local cleanup and may disable the probe. This differs from an explicitly confirmed delete, whose local cleanup is committed before native deletion and remains committed if the native result is uncertain. Test compatibility before relying on synchronization, and keep explicit bot binding and rollback backups.
