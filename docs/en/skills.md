# Skills

Skills are tools that the assistant can invoke during conversations. Each skill exposes one or more tools to the LLM with a name, description, and JSON input schema.

## Skill Interface

```go
type Skill interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage  // nil for text-only skills
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

**Optional interfaces:**
- `CapabilityAware` — `RequiredCapabilities() []string`: skill excluded if any capability is absent
- `ConfigReloadable` — `OnConfigChanged(key, value string)`: called on runtime config change
- `ApprovalDeclarer` — `ApprovalLevel() ApprovalLevel`: approval requirement

## Approval Levels

| Level | Behavior | Used By |
|-------|----------|---------|
| `ApprovalAuto` | Execute immediately (default) | Most skills |
| `ApprovalPrompt` | User must confirm in chat | Docker executor |
| `ApprovalManual` | Admin must confirm | Shell exec |

The approval flow is **non-blocking**: the skill returns "awaiting approval" to the LLM. The next user message is checked against locale-aware approval vocabulary (yes/no in 6 languages).

## Built-in Skills

### Memory Group

| Tool | Input | Description |
|------|-------|-------------|
| `remember` | `content` | Store a fact. Checks for duplicates via FTS. Triggers auto-embedding. |
| `recall` | `query`, `limit` | Search facts via FTS5. Applies temporal decay + MMR reranking. Reinforces accessed facts. |
| `forget` | `id` | Delete a fact by ID. Cascades to FTS and vector tables. |

See [Memory and Insights](memory-and-insights.md) for full details.

### Insights Group

| Tool | Input | Description |
|------|-------|-------------|
| `list_insights` | `limit` | List recent insights with quality scores |
| `dismiss_insight` | `id` | Delete an insight |
| `promote_insight` | `id` | Extend or remove insight expiry |

### Web Search & Fetch

| Tool | Input | Description |
|------|-------|-------------|
| `websearch` | `query`, `count` | Web search via Brave API + DuckDuckGo fallback. 1-10 results. |
| `webfetch` | `url` | Fetch and summarize a web page. Uses go-readability for content extraction. SSRF-protected. |

The web search chain is `Brave → DuckDuckGo` via `FallbackSearcher`. DuckDuckGo needs no API key, so web search always works.

### Directives

| Tool | Input | Description |
|------|-------|-------------|
| `directives` | `action`, `content` | Manage persistent custom instructions (set/get/clear). Loaded into system prompt. |

### Reminders

| Tool | Input | Description |
|------|-------|-------------|
| `reminders` | `action`, `title`, `due_at`, `timezone`, `id` | Create/list/delete time-based reminders. Delivered by the scheduler. |

### Date/Time

| Tool | Input | Description |
|------|-------|-------------|
| `datetime` | `timezone` | Current date, time, timezone name, Unix timestamp. Zero external dependencies. |

### Weather

| Tool | Input | Description |
|------|-------|-------------|
| `weather` | `location`, `days` | Weather forecasts (1-16 days). Interactive location resolution. |

**Backend chain**: Open-Meteo (primary, free) → wttr.in (fallback, free) → OpenWeatherMap (optional, requires key).

Features:
- Interactive location resolution via channel prompts (Telegram inline keyboard, WebChat buttons, Console numbered options)
- Cyrillic geocoding support
- Multi-day forecasts with WMO weather code descriptions (translated in 6 languages)
- Output adapts to channel capabilities (markdown vs plain text)

### Geolocation

| Tool | Input | Description |
|------|-------|-------------|
| `geolocation` | `ip` | IP-based geolocation. Auto-detects public IP. |

Provider chain: ipinfo.io (with key) → ip-api.com → ipapi.co. Validates IP is public (blocks RFC1918, loopback, etc.).

### Exchange Rate

| Tool | Input | Description |
|------|-------|-------------|
| `exchange_rate` | `from`, `to`, `amount` | Currency exchange rates. 160+ currencies. No API key required. |

### Shell Exec

| Tool | Input | Description |
|------|-------|-------------|
| `shell_exec` | `command`, `args` | Sandboxed shell command execution. **ApprovalManual** (admin confirmation required). |

**Security measures:**
- Whitelist-only: only `AllowedBins` may execute
- `ForbiddenPaths` checked in arguments
- Rejects `..` path traversal
- Max 16KB output
- Default execution directory: `os.TempDir()`

### Delegate

| Tool | Input | Description |
|------|-------|-------------|
| `delegate` | `prompt`, `provider` | Route a sub-prompt to a secondary LLM provider (e.g., Ollama for cheap tasks). |

### PDF Reader

| Tool | Input | Description |
|------|-------|-------------|
| `pdf_read` | `url` | Fetch and read PDF documents. Validates `%PDF-` magic bytes. |

### Set Language

| Tool | Input | Description |
|------|-------|-------------|
| `set_language` | `language` | Switch interface language. Accepts BCP-47 codes or language names (English/Russian). |

Updates `user_channels.locale` in the database. The confirmation message is in the **new** language.

### Google Workspace

| Tool | Description |
|------|-------------|
| `google_auth` | OAuth2 flow initiation, account listing |
| `google_calendar` | List/create/update/delete events, free/busy |
| `google_contacts` | List contacts, birthday queries |
| `google_mail` | List/read/search Gmail (read-only) |
| `google_tasks` | CRUD on Google Tasks |

Requires OAuth2 setup via the dashboard. Multi-account support.

### Todoist

| Tool | Input | Description |
|------|-------|-------------|
| `todoist` | `action`, ... | Full Todoist task management. 34 actions. |

**Actions**: create, list, get, update, complete, reopen, delete, move, quick_add, filter, completed history. Supports priorities (P1-P4), deadlines, due dates/datetimes, recurring, labels, projects, sections, subtasks, comments.

Uses Unified API v1 (`api.todoist.com/api/v1`). API token auth.

### Unified Tasks

| Tool | Input | Description |
|------|-------|-------------|
| `tasks` | `action`, `provider`, ... | Aggregates Todoist + Google Tasks + Craft Tasks. |

**Actions**: `overview` (all providers), `list`, `create`, `complete`, `provider` (passthrough).

### Craft

| Tool | Description |
|------|-------------|
| `craft_read` | Read Craft documents |
| `craft_write` | Write Craft documents |
| `craft_tasks` | Manage Craft tasks |
| `craft_search` | Search Craft documents |

### Skills Management

| Tool | Input | Description |
|------|-------|-------------|
| `skills` | `action`, ... | List/enable/disable/get_config/set_config skills via chat. Admin-only for mutations. |

## Text Skills (System Prompt Injection)

Skills can be purely a system prompt injection — no `Execute` method, no tool definition for the LLM.

### SKILL.md Format

```yaml
---
name: my-skill
description: What this skill does
capabilities: [optional-cap]
config_keys: [skills.my-skill.setting]
secret_keys: [skills.my-skill.api_key]
force_triggers: [keyword1, keyword2]
---

Markdown instructions injected into the system prompt.
```

- Skills with `InputSchema() == nil` contribute only to `staticSystemPrompt()`
- The markdown body becomes part of the LLM's instructions
- `force_triggers` force specific tool calls when keywords match the user message

### Loading Paths

1. **Embedded in Go packages**: `//go:embed SKILL.md` + `LoadManifestFromFS()`
2. **External directory**: `LoadExternalManifests(dir)` from `~/.local/share/iulita/skills/`
3. **Installed external**: via ClawhHub marketplace or URL

## Skill Manager (External Skills)

### ClawhHub Marketplace

Install community skills from [ClawhHub](https://clawhub.ai):

```
# Via dashboard: Skills → External → Search
# Via chat: install from URL
```

The marketplace API (`clawhub.ai/api/v1`) supports:
- `Search(query)` — BM25 relevance-ranked results
- `Resolve(slug)` — get download URL and checksum
- `Download()` — fetch archive (max 50MB)

### Installation Flow

1. Check `MaxInstalled` limit
2. Resolve from source (ClawhHub, URL, or local directory)
3. Validate slug against `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`
4. Download and verify SHA-256 checksum
5. Parse `SKILL.md` with extended frontmatter
6. Validate isolation level against config
7. Scan for prompt injection patterns
8. Atomic move to installation directory
9. Register in skill registry

### Isolation Levels

| Level | Behavior | Approval |
|-------|----------|----------|
| `text_only` | System prompt injection only | Auto |
| `shell` | Shell execution via `ShellExecutor` | Manual |
| `docker` | Docker container execution | Prompt |
| `wasm` | WebAssembly runtime | Auto |

**Fallback chain**: If a skill requires shell but shell_exec is disabled, it falls back to `webfetchProxySkill` (extracts URLs from the prompt and fetches them), then to `text_only`.

### Security

- Slug validation prevents path traversal
- Checksum verification for remote downloads
- Isolation level validation against config (`AllowShell`, `AllowDocker`, `AllowWASM`)
- Code file detection: rejects skills with `.py`/`.js`/`.go`/etc. files unless properly isolated
- Prompt injection scanning: warns on suspicious patterns in skill body

## Skill Hot-Reload

Skills support runtime configuration changes without restart:

1. Skills call `RegisterKey()` at startup to declare their config keys
2. Dashboard config editor calls `Store.Set()` which publishes `ConfigChanged`
3. Event bus dispatches to `registry.DispatchConfigChanged()`
4. Skills implementing `ConfigReloadable` receive the new value

**Critical rule**: Skills MUST be registered unconditionally (not inside `if apiKey != ""`). Use capability-gating instead: `AddCapability("web")` when API key is present, `RemoveCapability("web")` when removed.

## Force Triggers

Skills can declare keywords that force tool invocation:

```yaml
force_triggers: [weather, погода, météo]
```

When the user's message contains a trigger keyword (case-insensitive substring match), `ForceTool` is set on the LLM request for iteration 0. This ensures the LLM always calls the tool rather than answering from training data.

Memory triggers (e.g., "remember", "запомни") are configured separately and force the `remember` tool.
