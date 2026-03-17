# Architecture

## High-Level Overview

```
Console TUI ─┐
Telegram ────┤
Web Chat ────┼→ Channel Manager → Assistant → LLM Provider Chain
                     ↕                ↕
                 UserResolver      Storage (SQLite)
                                     ↕
                               Scheduler → Worker
                               (insights, analysis, reminders)
                                     ↕
                                Event Bus → Dashboard (WebSocket)
                                          → Prometheus Metrics
                                          → Push Notifications
                                          → Cost Tracker
```

## Core Design Principles

1. **Fact-based memory** — only verified user data is stored, never hallucinated knowledge
2. **Console-first** — TUI is the default mode; server mode is opt-in
3. **Clean architecture** — domain models → interfaces → implementations → orchestrator
4. **Multi-channel, single identity** — facts and insights are shared across all channels via user_id
5. **Zero-config local install** — works out of the box with just an API key
6. **Hot-reloadable** — skills, config, and even Telegram token can change at runtime without restart

## Component Map

| Component | Package | Description |
|-----------|---------|-------------|
| Entrypoint | `cmd/iulita/` | CLI parsing, DI wiring, graceful shutdown |
| Assistant | `internal/assistant/` | Orchestrator: LLM loop, memory, compression, approvals, streaming |
| Channels | `internal/channel/` | Input adapters: Console TUI, Telegram, WebChat |
| Channel Manager | `internal/channelmgr/` | Channel lifecycle, routing, hot-reload |
| LLM Providers | `internal/llm/` | Claude, Ollama, OpenAI, ONNX embeddings |
| Skills | `internal/skill/` | 30+ tool implementations |
| Skill Manager | `internal/skillmgr/` | External skills: ClawhHub marketplace, URL, local |
| Bookmark | `internal/bookmark/` | Quick-save assistant responses as facts + background refinement |
| Storage | `internal/storage/sqlite/` | SQLite with FTS5, vectors, WAL mode |
| Scheduler | `internal/scheduler/` | Task queue with cron/interval support |
| Dashboard | `internal/dashboard/` | GoFiber REST API + embedded Vue 3 SPA |
| Config | `internal/config/` | Layered config: defaults → TOML → env → keyring → DB |
| Auth | `internal/auth/` | JWT + bcrypt, middleware |
| i18n | `internal/i18n/` | 6 languages, TOML catalogs, context propagation |
| Web Search | `internal/web/` | Brave + DuckDuckGo fallback, SSRF protection |
| Domain | `internal/domain/` | Pure domain models |
| Memory | `internal/memory/` | TF-IDF clustering, memory export/import |
| Metrics | `internal/metrics/` | Prometheus counters and histograms |
| Agent | `internal/agent/` | Sub-agent runner, orchestrator, budget enforcement |
| Events | `internal/eventbus/` | Publish/subscribe event bus |
| Cost | `internal/cost/` | LLM cost tracking with daily limits |
| Rate Limit | `internal/ratelimit/` | Per-chat and global rate limiters |
| Frontend | `ui/` | Vue 3 + Naive UI + UnoCSS SPA |

## Startup Order

The startup sequence is strictly ordered to satisfy dependencies:

```
1. Parse CLI args, resolve XDG paths, ensure directories
2. Handle subcommands: init, --version, --doctor (early exit)
3. Load config: defaults → TOML → env → keyring
4. Create logger (console mode redirects to file)
5. Open SQLite, run migrations
6. Initialize i18n catalog (after migrations, before skills)
7. Bootstrap admin user (before backfill)
8. BackfillUserIDs (associate legacy data with users)
9. Create config store, load DB overrides
10. Check setup mode gate (no LLM + no wizard = setup-only)
11. Validate config
12. Create auth service
13. Bootstrap channel instances
14. Create ONNX embedding provider (optional)
15. Build LLM provider chain (Claude → retry → fallback → cache → router)
16. Register all skills (unconditionally — capability-gated)
17. Create assistant
18. Wire event bus (config reload, metrics, cost, notifications)
19. Replay DB config overrides (hot-reload for dashboard-set credentials)
20. Create channel manager, scheduler, worker
21. Start scheduler, worker, assistant run loop
22. Start dashboard server
23. Start all channels
24. Block on shutdown signal
```

## Graceful Shutdown (7 Phases)

```
1. Stop all channels (stop accepting new messages)
2. Wait for assistant background goroutines
3. Wait for embedding backfill
4. Close ONNX provider
5. Shutdown event bus (wait for async handlers)
6. Wait for scheduler/worker/dashboard (10s timeout)
7. Close SQLite connection (last)
```

## Message Flow

When a user sends a message, this is the complete execution path:

```
User types "remember that I love Go"
    │
    ▼
Channel (Telegram/WebChat/Console)
    │ constructs IncomingMessage with platform-specific fields
    │ sets ChannelCaps bitmask (streaming, markdown, etc.)
    ▼
UserResolver (Telegram/Console only)
    │ maps platform identity → iulita UUID
    │ auto-registers new users if allowed
    ▼
Channel Manager
    │ routes to Assistant.HandleMessage
    ▼
Assistant — Phase 1: Context Setup
    │ timeout, user role, locale, caps → context
    │ check pending approval → execute if approved
    ▼
Assistant — Phase 2: Enrichment
    │ save message to DB
    │ background: TechFactAnalyzer (Cyrillic/Latin, message length)
    │ send "processing" status event
    ▼
Assistant — Phase 3: History & Compression
    │ load last 50 messages
    │ if tokens > 80% context window → compress older half
    ▼
Assistant — Phase 4: Context Data
    │ load directive, recent facts, relevant insights
    │ hybrid search: FTS5 + ONNX vectors + MMR reranking
    │ load tech facts (user profile)
    │ resolve timezone
    ▼
Assistant — Phase 5: Prompt Construction
    │ static prompt = base + skill system prompts (cached by Claude)
    │ dynamic prompt = time + directives + profile + facts + insights + language
    ▼
Assistant — Phase 6: Force-Tool Detection
    │ "remember" keyword → ForceTool = "remember"
    ▼
Assistant — Phase 7: Agentic Loop (max 10 iterations)
    │ Call LLM (streaming if no tools, otherwise standard)
    │ On context overflow → force compress → retry once
    │ If tool calls:
    │   ├── check approval level
    │   ├── execute skill
    │   ├── accumulate in ToolExchanges
    │   └── next iteration
    │ If no tool calls → return response
    ▼
Skill Execution (e.g., RememberSkill)
    │ duplicate check via FTS search
    │ save to SQLite → FTS trigger fires
    │ background: ONNX embedding → fact_vectors
    ▼
Response sent back through channel to user
```

## Key Interfaces

### Provider (LLM)

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

### Skill

```go
type Skill interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage  // nil for text-only skills
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

Optional interfaces: `CapabilityAware`, `ConfigReloadable`, `ApprovalDeclarer`.

### Channel

```go
type InputChannel interface {
    Start(ctx context.Context, handler MessageHandler) error
}

type MessageHandler func(ctx context.Context, msg IncomingMessage) (string, error)

type StreamingSender interface {
    MessageSender
    StartStream(ctx context.Context, chatID string, replyTo int) (editFn, doneFn func(string), err error)
}

// Optional — channels implement this to add bookmark buttons to streamed responses
type BookmarkStreamingSender interface {
    StreamingSender
    StartStreamWithBookmark(ctx context.Context, chatID string, replyTo int, userID string) (editFn, doneFn func(string), err error)
}
```

### Storage

```go
type Repository interface {
    // Messages
    SaveMessage(ctx, msg) error
    GetHistory(ctx, chatID, limit) ([]ChatMessage, error)

    // Memory
    SaveFact(ctx, fact) error
    SearchFacts(ctx, chatID, query, limit) ([]Fact, error)
    SearchFactsByUser(ctx, userID, query, limit) ([]Fact, error)
    SearchFactsHybridByUser(ctx, userID, query, queryVec, limit) ([]Fact, error)

    // Tasks
    CreateTask(ctx, task) error
    ClaimTask(ctx, workerID, capabilities) (*Task, error)

    // ... 60+ methods total
}
```

## Event Bus

The event bus (`internal/eventbus/`) implements a typed publish/subscribe pattern. Events flow between components without direct coupling:

| Event | Publisher | Subscribers |
|-------|-----------|-------------|
| `MessageReceived` | Assistant | Metrics, WebSocket hub |
| `ResponseSent` | Assistant | Metrics, WebSocket hub |
| `LLMUsage` | Assistant | Metrics, Cost tracker |
| `SkillExecuted` | Assistant | Metrics |
| `TaskCompleted` | Worker | WebSocket hub |
| `TaskFailed` | Worker | WebSocket hub |
| `FactSaved` | Storage | WebSocket hub |
| `InsightCreated` | Storage | WebSocket hub |
| `ConfigChanged` | Config store | Config reload handler → skills |
| `AgentOrchestrationStarted` | Orchestrator | Metrics, WebSocket hub |
| `AgentOrchestrationDone` | Orchestrator | Metrics, WebSocket hub |

## LLM Provider Chain

Providers are composed as decorators:

```
Claude Provider
    └→ Retry Provider (3 attempts, exponential backoff, 429/5xx)
        └→ Fallback Provider (Claude → OpenAI)
            └→ Caching Provider (SHA-256 key, 60min TTL)
                └→ Routing Provider (RouteHint-based dispatch)
                    └→ Classifying Provider (Ollama classifier → route selection)
```

For providers that don't support native tool calling (Ollama, OpenAI), the `XMLToolProvider` wrapper injects tool definitions as XML in the system prompt and parses XML tool calls from the response.

## Data Scoping

All data is scoped by `user_id` for cross-channel sharing:

```
User (iulita UUID)
    ├── user_channels (Telegram binding, WebChat binding, ...)
    ├── chat_messages (from all channels)
    ├── facts (shared across channels)
    ├── insights (shared across channels)
    ├── directives (per user)
    ├── tech_facts (behavioral profile)
    ├── reminders
    └── todo_items
```

A user chatting on Telegram can recall facts they stored via the Console TUI, because both channels resolve to the same `user_id`.

## Project Structure

```
cmd/iulita/              # entrypoint, DI wiring, graceful shutdown
internal/
  assistant/             # orchestrator (LLM loop, memory, compression, approvals)
  channel/
    console/             # bubbletea TUI
    telegram/            # Telegram bot
    webchat/             # WebSocket web chat
  bookmark/              # quick-save assistant responses as facts
  channelmgr/            # channel lifecycle manager
  config/                # TOML + env + keyring config, setup wizard
  domain/                # domain models
  auth/                  # JWT auth + bcrypt
  i18n/                  # internationalization (6 languages, TOML catalogs)
  llm/                   # LLM providers (Claude, Ollama, OpenAI, ONNX)
  scheduler/             # task queue (scheduler + worker)
  agent/                 # multi-agent orchestration (runner, orchestrator, budget)
  skill/                 # skill implementations
  skillmgr/              # external skill manager (ClawhHub, URL, local)
  storage/sqlite/        # SQLite repository, FTS5, vectors, migrations
  dashboard/             # GoFiber REST API + Vue SPA
  web/                   # web search (Brave, DuckDuckGo, SSRF protection)
  transcription/         # audio/voice transcription
  doctor/                # diagnostic checks (--doctor flag)
  memory/                # TF-IDF clustering, export/import
  eventbus/              # publish/subscribe event bus
  cost/                  # LLM cost tracking
  metrics/               # Prometheus metrics
  ratelimit/             # rate limiting
  notify/                # push notifications
ui/                      # Vue 3 + Naive UI + UnoCSS frontend
skills/                  # text skill files (Markdown)
docs/                    # documentation
```
