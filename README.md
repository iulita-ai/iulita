[![CI](https://github.com/iulita-ai/iulita/actions/workflows/ci.yml/badge.svg)](https://github.com/iulita-ai/iulita/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/iulita-ai/iulita/graph/badge.svg)](https://codecov.io/gh/iulita-ai/iulita)
[![Go Report Card](https://goreportcard.com/badge/github.com/iulita-ai/iulita)](https://goreportcard.com/report/github.com/iulita-ai/iulita)
[![License](https://img.shields.io/github/license/iulita-ai/iulita)](LICENSE)
[![Release](https://img.shields.io/github/v/release/iulita-ai/iulita)](https://github.com/iulita-ai/iulita/releases/latest)

# Iulita

Personal AI assistant that learns from **your data, not hallucinations about you**.

Most AI assistants forget everything between sessions or hallucinate "memories" from training data. Iulita takes a different approach: it stores only verified facts you explicitly share, builds real insights from cross-referencing your actual data, and never invents things it doesn't know. Your memory is yours — structured, searchable, and under your control.

Console-first: launches a full-screen TUI chat by default. Also runs as a headless server with Telegram, Web Chat, and a web dashboard.

## Features

- **Fact-based memory** — stores only what you explicitly tell it to remember, no hallucinated "knowledge"
- **Cross-reference insights** — discovers patterns across your facts using clustering and LLM analysis
- **Console TUI** — full-screen chat with markdown rendering, streaming, slash commands
- **Multi-channel** — Telegram bot, Web Chat (WebSocket), Console TUI
- **Temporal decay** — older memories naturally lose relevance (configurable half-life)
- **Hybrid search** — FTS5 full-text + ONNX vector embeddings with MMR reranking
- **20+ skills** — web search, Google Workspace, Todoist, Craft, weather, shell exec, and more
- **Text skills** — extend with custom instructions via Markdown files
- **Context compression** — automatically summarizes old messages when context window fills up
- **Task scheduler** — background jobs for insight generation, profile analysis, reminders
- **Web dashboard** — Vue 3 SPA for managing facts, insights, tasks, channels, users, and settings
- **Multi-user** — JWT auth, user-scoped data, cross-channel fact sharing
- **i18n** — 6 languages (English, Russian, Chinese, Spanish, French, Hebrew + RTL)
- **ClawhHub marketplace** — install community skills from the marketplace or via URL
- **Zero-config local install** — XDG paths, keyring secrets, interactive setup wizard

## Quick Start

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build
./bin/iulita init    # interactive setup wizard
./bin/iulita         # launch TUI
```

This launches the interactive TUI. Type messages, use `/help` for commands, `Ctrl+C` to exit.

### Server Mode

For running as a background service with Telegram, Web Chat, and dashboard:

```bash
./bin/iulita --server
```

### Docker

```bash
cp config.toml.example config.toml
# Edit config.toml — set claude.api_key at minimum
mkdir -p data
docker compose up -d
```

On first run without config, the server starts in **setup mode** — a web wizard at http://localhost:8080 walks you through provider selection, feature configuration, and TOML import.

Pre-built image:

```yaml
services:
  iulita:
    image: ghcr.io/iulita-ai/iulita:latest
```

## How Memory Works

1. You say "remember that my dog's name is Max"
2. Iulita calls the `remember` tool and stores the fact in SQLite
3. Later, when relevant, it recalls the fact via hybrid search (FTS5 + vector similarity)
4. Over time, the scheduler cross-references facts and generates insights
5. Old facts decay in relevance unless you access them again

No training data. No hallucinations. Just your verified facts.

## Architecture

```
Console TUI ─┐
Telegram ────┤
Web Chat ────┼→ Channel → Assistant → LLM Provider
                   ↕          ↕
               Resolver    Storage (SQLite)
                              ↕
                        Scheduler → Worker
                        (insights, analysis, reminders)
```

| Component | Description |
|-----------|-------------|
| `channel/console` | Bubbletea TUI with markdown, streaming, slash commands |
| `channel/telegram` | Telegram bot with whitelist, debouncing, streaming edits |
| `channel/webchat` | WebSocket-based web chat with JWT auth |
| `assistant` | Orchestrator: history, memory, skills, compression, approvals |
| `llm/claude` | Claude API with prompt caching, streaming, context overflow recovery |
| `llm/ollama` | Ollama local LLM for dev/background tasks |
| `llm/openai` | OpenAI-compatible provider (fallback) |
| `storage/sqlite` | SQLite with FTS5 + ONNX vectors, WAL mode |
| `skill/*` | 20+ tool implementations |
| `scheduler` | Task queue with local + remote worker support |
| `dashboard` | GoFiber REST API + embedded Vue 3 SPA |
| `skillmgr` | External skill manager (ClawhHub, URL, local) |

### Skills

| Skill | Description |
|-------|-------------|
| `remember` / `recall` / `forget` | Persistent fact memory with hybrid search |
| `reminders` | Time-based reminders with delivery |
| `directives` | Persistent user preferences for the AI |
| `insights` | AI-generated cross-reference insights |
| `websearch` / `webfetch` | Web search (Brave + DuckDuckGo) and page summarization |
| `google` | Gmail, Calendar, Contacts, Tasks via OAuth2 |
| `todoist` | Full Todoist task management (CRUD, projects, labels, filters) |
| `tasks` | Unified task view across Todoist, Google Tasks, Craft |
| `craft` | Craft document search, read, write, tasks |
| `weather` | Weather forecasts (Open-Meteo, wttr.in, OpenWeatherMap) |
| `exchange` | Currency exchange rates |
| `geolocation` | IP-based geolocation |
| `shell_exec` | Sandboxed shell command execution |
| `delegate` | Multi-agent task delegation |
| `pdfreader` | PDF document reading |
| `set_language` | Switch interface language via chat |
| `skills` | List, enable, disable, configure skills at runtime |
| `datetime` | Current date/time in user's timezone |

## Configuration

All settings are in `config.toml`. Every option can be overridden via environment variables with the `IULITA_` prefix:

| Config key | Env variable | Description |
|------------|-------------|-------------|
| `claude.api_key` | `IULITA_CLAUDE_API_KEY` | Anthropic API key (required) |
| `telegram.token` | `IULITA_TELEGRAM_TOKEN` | Telegram bot token |
| `telegram.allowed_ids` | — | Telegram user IDs whitelist |
| `claude.model` | `IULITA_CLAUDE_MODEL` | Model ID |
| `storage.path` | `IULITA_STORAGE_PATH` | SQLite database path |
| `server.address` | `IULITA_SERVER_ADDRESS` | Dashboard listen address (`:8080`) |
| `proxy.url` | `IULITA_PROXY_URL` | HTTP/SOCKS5 proxy for all requests |

See [`config.toml.example`](config.toml.example) for the full reference with all skill configs.

## CLI

| Command / Flag | Description |
|----------------|-------------|
| `iulita` | Launch interactive console TUI (default) |
| `iulita --server` / `-d` | Run as headless server |
| `iulita init` | Interactive setup wizard |
| `iulita --doctor` | Run diagnostic checks |
| `iulita --version` / `-v` | Print version and exit |

## Development

```bash
make build          # build frontend + Go binary
make build-go       # build Go binary only (skip frontend)
make run            # build + launch console TUI
make console        # run console TUI (go run, no build)
make server         # run headless server mode
make dev            # dev mode with hot-reload (server + Vue dev server)
make test           # run all tests
make tidy           # go mod tidy
make clean          # remove build artifacts
make setup-hooks    # configure pre-commit security hooks
make check-secrets  # scan for leaked secrets
```

### Project Structure

```
cmd/iulita/          # entrypoint, DI wiring, graceful shutdown
internal/
  assistant/         # orchestrator (LLM loop, memory, compression, approvals)
  channel/
    console/         # bubbletea TUI
    telegram/        # Telegram bot
    webchat/         # WebSocket web chat
  channelmgr/        # channel lifecycle manager
  config/            # TOML + env + keyring config, setup wizard
  domain/            # domain models
  auth/              # JWT auth + bcrypt
  i18n/              # internationalization (6 languages, TOML catalogs)
  llm/               # LLM providers (Claude, Ollama, OpenAI, ONNX)
  scheduler/         # task queue (scheduler + worker)
  skill/             # skill implementations
  skillmgr/          # external skill manager (ClawhHub, URL, local)
  storage/sqlite/    # SQLite repository, FTS5, vectors, migrations
  dashboard/         # GoFiber REST API + Vue SPA
  web/               # web search (Brave, DuckDuckGo, SSRF protection)
ui/                  # Vue 3 + Naive UI + UnoCSS frontend
skills/              # text skill files (Markdown)
```

## Tech Stack

- **Go 1.25** with pure-Go SQLite ([modernc.org/sqlite](https://modernc.org/sqlite))
- **SQLite** (WAL mode) via [bun](https://bun.uptrace.dev/) ORM + FTS5 + ONNX vector search
- **anthropic-sdk-go** — Claude API with prompt caching
- **bubbletea + lipgloss + glamour** — console TUI
- **telegram-bot-api/v5** — Telegram
- **GoFiber** — dashboard HTTP server
- **Vue 3 + Naive UI + UnoCSS** — dashboard frontend
- **vue-i18n** — frontend internationalization
- **koanf** — configuration (TOML + env + keyring overlay)
- **zap** — structured logging
- **Prometheus** — metrics

## Security

- Pre-commit hook blocks secrets via [gitleaks](https://github.com/gitleaks/gitleaks)
- Telegram user whitelist (`allowed_ids`)
- JWT auth for dashboard and web chat
- AES-256-GCM encryption for DB-stored config overrides
- SSRF protection for web fetch/search (blocks private IPs)
- Tool approval levels (auto / prompt / manual) with locale-aware vocabulary
- Config validation on startup
- CodeQL and gitleaks in CI

## Documentation

Full documentation is available in the [`docs/`](docs/) directory:

- [Getting Started](docs/en/getting-started.md) — installation, first run, CLI reference
- [Architecture](docs/en/architecture.md) — system overview, message flow, key interfaces
- [Memory and Insights](docs/en/memory-and-insights.md) — fact storage, temporal decay, embeddings
- [Channels](docs/en/channels.md) — Console TUI, Telegram, WebChat
- [LLM Providers](docs/en/llm-providers.md) — Claude, Ollama, OpenAI, ONNX
- [Skills](docs/en/skills.md) — all 20+ tools, approval levels, marketplace
- [i18n / l10n](docs/en/i18n.md) — 6 languages, RTL support
- [Configuration](docs/en/configuration.md) — layered config, hot-reload
- [Storage](docs/en/storage.md) — SQLite, FTS5, vector search
- [Scheduler](docs/en/scheduler.md) — background jobs, agent tasks
- [Dashboard](docs/en/dashboard.md) — REST API, Vue 3 SPA
- [Security](docs/en/security.md) — JWT, SSRF, encryption
- [Deployment](docs/en/deployment.md) — Docker, monitoring, backup

Documentation is available in: [English](docs/en/) | [Русский](docs/ru/) | [中文](docs/zh/) | [Español](docs/es/) | [Français](docs/fr/) | [עברית](docs/he/)

## Contributing

Contributions are welcome. By opening a pull request, you agree to the [Contributor License Agreement](CLA.md).

## Dedication

This project is dedicated to my grandmother, who devoted her life to raising me. She was my teacher, my guide, and my biggest supporter. She loved her middle name — Iulita — and so this project carries it forward in her honor.

## License

[MIT](LICENSE) — Copyright (c) 2025 Stanislav Gumeniuk
