# Configuration

Iulita uses a layered configuration system that supports zero-config local installation while allowing full customization for advanced deployments.

## Configuration Layers

Configuration is loaded in order, with later layers overriding earlier ones:

```
1. Compiled defaults (always present)
2. TOML file (~/.config/iulita/config.toml, optional)
3. Environment variables (IULITA_* prefix)
4. Keyring secrets (macOS Keychain, Linux SecretService)
5. DB overrides (config_overrides table, runtime-editable)
```

### Layer 1: Compiled Defaults

`DefaultConfig()` provides a working configuration with no external files. All model IDs, timeouts, memory settings, and feature flags have sensible defaults. The system works out of the box with just an API key.

### Layer 2: TOML File

Optional. Located at `~/.config/iulita/config.toml` (or `$IULITA_HOME/config.toml`).

The TOML file is **skipped** if:
- No file exists at the config path
- The `db_managed` sentinel file exists (web wizard mode)

See `config.toml.example` for the full reference.

### Layer 3: Environment Variables

All settings can be overridden via `IULITA_*` environment variables:

```
IULITA_CLAUDE_API_KEY      → claude.api_key
IULITA_TELEGRAM_TOKEN      → telegram.token
IULITA_CLAUDE_MODEL        → claude.model
IULITA_STORAGE_PATH        → storage.path
IULITA_SERVER_ADDRESS      → server.address
IULITA_PROXY_URL           → proxy.url
```

**Mapping rule**: strip `IULITA_` prefix, lowercase, replace `_` with `.`.

### Layer 4: Keyring Secrets

Secrets are stored securely in the OS keyring:

| Secret | Env Variable | Keyring Account |
|--------|-------------|-----------------|
| Claude API key | `IULITA_CLAUDE_API_KEY` | `claude-api-key` |
| Telegram token | `IULITA_TELEGRAM_TOKEN` | `telegram-token` |
| JWT secret | `IULITA_JWT_SECRET` | `jwt-secret` |
| Config encryption key | `IULITA_CONFIG_KEY` | `config-encryption-key` |

**Fallback chain** for each secret: env variable → keyring → file (for encryption key only) → auto-generate (for JWT only).

The keyring uses `zalando/go-keyring`:
- **macOS**: Keychain
- **Linux**: SecretService (GNOME Keyring, KDE Wallet)
- **Fallback**: encrypted file at `~/.config/iulita/encryption.key`

### Layer 5: DB Overrides (Config Store)

Runtime-editable configuration stored in the `config_overrides` SQLite table. Managed via:
- Dashboard config editor
- Chat-based `skills` tool (`set_config` action)
- Web setup wizard

**Features:**
- AES-256-GCM encryption for secret values
- Immediate hot-reload via event bus
- Audit logging (who changed what, when)
- Immutable restart-only keys protected

## XDG-Compliant Paths

| Platform | Config | Data | Cache | State |
|----------|--------|------|-------|-------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

**Override**: set `IULITA_HOME` to use a custom root with `data/`, `cache/`, `state/` subdirs.

### Derived Paths

| Path | Location |
|------|----------|
| Config file | `{ConfigDir}/config.toml` |
| Database | `{DataDir}/iulita.db` |
| ONNX models | `{DataDir}/models/` |
| Skills | `{DataDir}/skills/` |
| External skills | `{DataDir}/external-skills/` |
| Log file | `{StateDir}/iulita.log` |
| Encryption key | `{ConfigDir}/encryption.key` |

## Config Sections

### App

| Key | Default | Description |
|-----|---------|-------------|
| `app.system_prompt` | (built-in) | Base system prompt for the assistant |
| `app.context_window` | 200000 | Token budget for context |
| `app.request_timeout` | 120s | Per-message timeout |

### Claude (Primary LLM)

| Key | Default | Description |
|-----|---------|-------------|
| `claude.api_key` | — | Anthropic API key (required) |
| `claude.model` | `claude-sonnet-4-5-20250929` | Model ID |
| `claude.max_tokens` | 8192 | Max output tokens |
| `claude.base_url` | — | Override API base URL |
| `claude.thinking` | 0 | Extended thinking budget (0 = disabled) |

### Ollama (Local LLM)

| Key | Default | Description |
|-----|---------|-------------|
| `ollama.url` | `http://localhost:11434` | Ollama server URL |
| `ollama.model` | `llama3` | Model name |

### OpenAI (Compatibility)

| Key | Default | Description |
|-----|---------|-------------|
| `openai.api_key` | — | API key |
| `openai.model` | `gpt-4` | Model ID |
| `openai.base_url` | `https://api.openai.com/v1` | API base URL |

### Telegram

| Key | Default | Description |
|-----|---------|-------------|
| `telegram.token` | — | Bot token (hot-reloadable) |
| `telegram.allowed_ids` | `[]` | User ID whitelist (empty = all) |
| `telegram.debounce_window` | 2s | Message coalescing window |

### Storage

| Key | Default | Description |
|-----|---------|-------------|
| `storage.path` | `{DataDir}/iulita.db` | SQLite database path (restart-only) |

### Server

| Key | Default | Description |
|-----|---------|-------------|
| `server.enabled` | true | Enable dashboard server |
| `server.address` | `:8080` | Listen address (restart-only) |

### Auth

| Key | Default | Description |
|-----|---------|-------------|
| `auth.jwt_secret` | (auto-generated) | JWT signing key |
| `auth.token_ttl` | 24h | Access token TTL |
| `auth.refresh_ttl` | 7d | Refresh token TTL |

### Proxy

| Key | Default | Description |
|-----|---------|-------------|
| `proxy.url` | — | HTTP/SOCKS5 proxy (restart-only) |

### Memory

| Key | Default | Description |
|-----|---------|-------------|
| `skills.memory.half_life_days` | 30 | Temporal decay half-life |
| `skills.memory.mmr_lambda` | 0 | MMR diversity (0.7 recommended) |
| `skills.memory.vector_weight` | 0 | Hybrid search weight |
| `skills.memory.triggers` | `[]` | Memory trigger keywords |

### Insights

| Key | Default | Description |
|-----|---------|-------------|
| `skills.insights.min_facts` | 20 | Minimum facts for generation |
| `skills.insights.max_pairs` | 6 | Max cluster pairs per run |
| `skills.insights.ttl` | 720h | Insight expiry (30 days) |
| `skills.insights.interval` | 24h | Generation frequency |
| `skills.insights.quality_threshold` | 0 | Min quality score |

### Embedding

| Key | Default | Description |
|-----|---------|-------------|
| `embedding.enabled` | true | Enable ONNX embeddings |
| `embedding.model` | `KnightsAnalytics/all-MiniLM-L6-v2` | Model name |

### Scheduler

| Key | Default | Description |
|-----|---------|-------------|
| `scheduler.enabled` | true | Enable task scheduler |
| `scheduler.worker_token` | — | Bearer token for remote workers |

### Cost

| Key | Default | Description |
|-----|---------|-------------|
| `cost.daily_limit_usd` | 0 | Daily cost cap (0 = unlimited) |

### Cache

| Key | Default | Description |
|-----|---------|-------------|
| `cache.enabled` | false | Enable response caching |
| `cache.ttl` | 60m | Cache TTL |
| `cache.max_items` | 1000 | Max cached responses |

### Metrics

| Key | Default | Description |
|-----|---------|-------------|
| `metrics.enabled` | false | Enable Prometheus metrics |
| `metrics.address` | `:9090` | Metrics server address |

## Setup Wizard

### CLI Wizard (`iulita init`)

Interactive setup that guides through:
1. LLM provider selection (Claude/OpenAI/Ollama, multi-select)
2. API key entry (stored in keyring)
3. Optional integrations (Telegram, proxy, embedding)
4. Model selection (dynamic fetch from provider)

Secrets go to keyring; non-secrets go to `config.toml`.

### Web Setup Wizard (Docker)

For Docker deployments without terminal access:

1. Server starts in **setup mode** when no LLM is configured and wizard not completed
2. Dashboard-only mode (no skills, scheduler, or channels)
3. 5-step wizard: Welcome/Import → Provider → Config → Features → Complete
4. TOML import support (paste existing config)
5. Creates `db_managed` sentinel file (disables TOML loading)
6. Sets `_system.wizard_completed` in config_overrides

## Hot-Reload

These settings can change at runtime without restart:

| Setting | Trigger | Mechanism |
|---------|---------|-----------|
| Claude model/tokens/key | Dashboard config editor | `UpdateModel()`/`UpdateMaxTokens()`/`UpdateAPIKey()` |
| Telegram token | Dashboard config editor | `channelmgr.UpdateConfigToken()` → restart instance |
| Skill enable/disable | Dashboard or chat | `registry.EnableSkill()`/`DisableSkill()` |
| Skill config (API keys) | Dashboard config editor | `ConfigReloadable.OnConfigChanged()` |
| System prompt | Dashboard config editor | `asst.SetSystemPrompt()` |
| Thinking budget | Dashboard config editor | `asst.SetThinkingBudget()` |

### Restart-Only Settings

These require a full restart:
- `storage.path`
- `server.address`
- `proxy.url`
- `security.config_key_env`

## AES-256-GCM Encryption

Secret config values in the DB are encrypted:

1. **Key source**: `IULITA_CONFIG_KEY` env → keyring → auto-generated file
2. **Algorithm**: AES-256-GCM (authenticated encryption)
3. **Format**: `base64(12-byte-nonce ‖ ciphertext)`
4. **Auto-encrypt**: keys declared as `secret_keys` in SKILL.md are always encrypted
5. **Never leaked**: dashboard API returns empty values for encrypted keys
