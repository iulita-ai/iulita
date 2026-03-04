# Dashboard

The dashboard is a GoFiber REST API serving an embedded Vue 3 SPA. It provides a web interface for managing all aspects of Iulita.

## Architecture

```
GoFiber Server
    ├── /api/*          REST API (JWT-authenticated)
    ├── /ws             WebSocket hub (real-time updates)
    ├── /ws/chat        WebChat channel (separate endpoint)
    └── /*              Vue 3 SPA (embedded, client-side routing)
```

The Vue SPA is embedded in the Go binary via `//go:embed dist/*` and served with `index.html` fallback for all unknown paths.

## Authentication

| Endpoint | Auth | Description |
|----------|------|-------------|
| `POST /api/auth/login` | Public | bcrypt credential check, returns access + refresh tokens |
| `POST /api/auth/refresh` | Public | Validate refresh token, return new access token |
| `POST /api/auth/change-password` | JWT | Change own password |
| `GET /api/auth/me` | JWT | Current user profile |
| `PATCH /api/auth/locale` | JWT | Update locale for all channels |

**JWT details:**
- Algorithm: HMAC-SHA256
- Access token TTL: 24 hours
- Refresh token TTL: 7 days
- Claims: `user_id`, `username`, `role`
- Secret: auto-generated if not configured

## REST API

### Public Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/system` | System info, version, uptime, wizard status |

### User Endpoints (JWT Required)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/stats` | Message, fact, insight, reminder counts |
| GET | `/api/chats` | List all chat IDs with message counts |
| GET | `/api/facts` | List/search facts (by chat_id, user_id, query) |
| PUT | `/api/facts/:id` | Update fact content |
| DELETE | `/api/facts/:id` | Delete fact |
| GET | `/api/facts/search` | FTS fact search |
| GET | `/api/insights` | List insights |
| GET | `/api/reminders` | List reminders |
| GET | `/api/directives` | Get directive for a chat |
| GET | `/api/messages` | Chat history with pagination |
| GET | `/api/skills` | List all skills with enabled/config status |
| PUT | `/api/skills/:name/toggle` | Enable/disable skill at runtime |
| GET | `/api/skills/:name/config` | Skill config schema + current values |
| PUT | `/api/skills/:name/config/:key` | Set skill config key (auto-encrypts secrets) |
| GET | `/api/techfacts` | Behavioral profile grouped by category |
| GET | `/api/usage/summary` | Token usage + cost estimate |
| GET | `/api/schedulers` | Scheduler job status |
| POST | `/api/schedulers/:name/trigger` | Manual job trigger |

### Task Endpoints (JWT Required)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/todos/providers` | List task providers |
| GET | `/api/todos/today` | Today's tasks |
| GET | `/api/todos/overdue` | Overdue tasks |
| GET | `/api/todos/upcoming` | Upcoming tasks (default 7 days) |
| GET | `/api/todos/all` | All incomplete tasks |
| GET | `/api/todos/counts` | Today + overdue counts |
| POST | `/api/todos/` | Create task |
| POST | `/api/todos/sync` | Trigger manual todo sync |
| POST | `/api/todos/:id/complete` | Complete task |
| DELETE | `/api/todos/:id` | Delete builtin task |

### Google Workspace Endpoints (JWT Required)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/google/status` | Account status |
| POST | `/api/google/upload-credentials` | Upload OAuth credentials file |
| GET | `/api/google/auth` | Start OAuth2 flow |
| GET | `/api/google/callback` | OAuth2 callback |
| GET | `/api/google/accounts` | List accounts |
| DELETE | `/api/google/accounts/:id` | Delete account |
| PUT | `/api/google/accounts/:id` | Update account |

### Admin Endpoints (Admin Role Required)

| Method | Path | Description |
|--------|------|-------------|
| GET/PUT/DELETE | `/api/config/*` | Config overrides, schema, debug |
| GET/POST/PUT/DELETE | `/api/users/*` | User CRUD + channel bindings |
| GET/POST/PUT/DELETE | `/api/channels/*` | Channel instance CRUD |
| GET/POST/PUT/DELETE | `/api/agent-jobs/*` | Agent job CRUD |
| GET/POST/DELETE | `/api/skills/external/*` | External skill management |
| GET/POST | `/api/wizard/*` | Setup wizard |
| PUT | `/api/todos/default-provider` | Set default task provider |

### Worker Endpoints (Bearer Token)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/tasks/` | List scheduler tasks |
| GET | `/api/tasks/counts` | Counts by status |
| POST | `/api/tasks/claim` | Claim a task (remote worker) |
| POST | `/api/tasks/:id/start` | Mark task as running |
| POST | `/api/tasks/:id/complete` | Complete task |
| POST | `/api/tasks/:id/fail` | Fail task |

## WebSocket Hub

The WebSocket hub at `/ws` provides real-time updates to connected dashboard clients.

### Events

| Event | Source | Payload |
|-------|--------|---------|
| `task.completed` | Worker | Task details |
| `task.failed` | Worker | Task + error |
| `message.received` | Assistant | Message metadata |
| `response.sent` | Assistant | Response metadata |
| `fact.saved` | Storage | Fact details |
| `insight.created` | Storage | Insight details |
| `config.changed` | Config store | Key + value |

Events are published via the event bus using `SubscribeAsync` (non-blocking).

### Protocol

```json
// Server → Client
{"type": "task.completed", "payload": {...}}
{"type": "fact.saved", "payload": {...}}
```

## Vue 3 SPA

### Tech Stack

- **Vue 3** — Composition API
- **Naive UI** — component library
- **UnoCSS** — utility-first CSS
- **vue-i18n** — internationalization (6 languages)
- **vue-router** — client-side routing

### Views

| Path | Component | Auth | Description |
|------|-----------|------|-------------|
| `/` | Dashboard | JWT | Stats overview, scheduler status |
| `/facts` | Facts | JWT | Fact browser with search, edit, delete |
| `/insights` | Insights | JWT | Insight list |
| `/reminders` | Reminders | JWT | Reminder list |
| `/profile` | TechFacts | JWT | Behavioral profile metadata |
| `/settings` | Settings | JWT | Skill management, config editor |
| `/tasks` | Tasks | JWT | Today/Overdue/Upcoming/All tabs |
| `/chat` | Chat | JWT | WebSocket web chat |
| `/users` | Users | Admin | User CRUD + channel bindings |
| `/channels` | Channels | Admin | Channel instance CRUD |
| `/agent-jobs` | AgentJobs | Admin | Agent job CRUD |
| `/skills` | ExternalSkills | Admin | Marketplace + installed skills |
| `/setup` | Setup | Admin | Web setup wizard |
| `/config-debug` | ConfigDebug | Admin | Raw config override viewer |
| `/login` | Login | Public | Login form |

### Router Guards

```javascript
router.beforeEach((to, from, next) => {
    if (to.meta.public) { next(); return }
    if (!isLoggedIn()) { next({ name: 'login' }); return }
    if (to.meta.admin && !isAdmin()) { next({ name: 'dashboard' }); return }
    next()
})
```

### Key Composables

- `useWebSocket` — auto-reconnect WebSocket with typed events
- `useLocale` — reactive locale state, RTL detection, backend sync
- `useSkillStatus` — gates sidebar items based on skill availability

### Skill Management UI

The Settings view provides:

1. **Skill toggle** — enable/disable each skill at runtime
2. **Config editor** — per-skill configuration with:
   - Schema-driven form fields
   - Secret key protection (values never leaked in API)
   - Auto-encryption for sensitive values
   - Hot-reload on save

### Tasks Dashboard

The Tasks view aggregates tasks from all providers:

- **Today tab** — tasks due today
- **Overdue tab** — past-due tasks
- **Upcoming tab** — next 7 days
- **All tab** — all incomplete tasks
- **Sync button** — triggers one-shot scheduler task
- **Create button** — new task with provider selection

## Prometheus Metrics

When enabled (`metrics.enabled = true`), metrics are exposed on a separate port:

| Metric | Type | Labels |
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

Metrics are populated by subscribing to the event bus (non-blocking).
