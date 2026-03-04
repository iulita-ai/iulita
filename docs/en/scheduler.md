# Scheduler

The scheduler is a two-component system: a **Coordinator** that produces tasks on schedule, and a **Worker** that claims and executes them. Both use SQLite as the task queue.

## Architecture

```
Scheduler (Coordinator)
    │ polls every 30s
    │ checks job timing against scheduler_states
    │
    ├── InsightJob (24h) → insight.generate tasks
    ├── InsightCleanupJob (1h) → insight.cleanup tasks
    ├── TechFactsJob (6h) → techfact.analyze tasks
    ├── HeartbeatJob (6h) → heartbeat.check tasks
    ├── RemindersJob (30s) → reminder.fire tasks
    ├── AgentJobsJob (30s) → agent.job tasks
    └── TodoSyncJob (hourly cron) → todo.sync tasks
           │
           ▼
    tasks table (SQLite)
           │
           ▼
Worker
    │ polls every 5s
    │ claims tasks atomically
    │ dispatches to registered handlers
    │
    ├── InsightGenerateHandler
    ├── InsightCleanupHandler
    ├── TechFactAnalyzeHandler
    ├── HeartbeatHandler
    ├── ReminderFireHandler
    ├── AgentJobHandler
    └── TodoSyncHandler
```

## Coordinator

### Job Definition

```go
type JobDefinition struct {
    Name        string
    Interval    time.Duration
    CronExpr    string           // standard cron (5-field)
    Timezone    string           // IANA timezone for cron
    Enabled     bool
    CreateTasks func(ctx) ([]domain.Task, error)
}
```

Each job declares either a fixed `Interval` or a `CronExpr`. Cron uses `robfig/cron/v3` with timezone support.

### Scheduling Loop

1. **Warm-up**: on first boot, `NextRun = now + 1 minute` (grace period)
2. **Tick** every 30 seconds:
   - Maintenance: reclaim stale tasks (running > 5 min), delete old tasks (> 7 days)
   - For each enabled job: if `now >= state.NextRun`, call `CreateTasks`
   - Insert tasks via `CreateTaskIfNotExists` (idempotent by `UniqueKey`)
   - Update state: `LastRun = now`, `NextRun = computeNextRun()`

### Manual Trigger

`TriggerJob(name)`:
- Finds the named job
- Calls `CreateTasks` with `Priority = 1` (high)
- Inserts tasks immediately
- Does NOT update the schedule state (next regular run still happens)

Available via dashboard: `POST /api/schedulers/:name/trigger`

## Worker

### Task Claiming

```
Every 5 seconds:
    for each available concurrency slot:
        ClaimTask(ctx, workerID, capabilities)  // atomic SQLite transaction
        if task claimed:
            go executeTask(task)
        else:
            break  // no more tasks available
```

`workerID = hostname-pid` (unique per process).

### Capability-Based Routing

Tasks declare required capabilities as a comma-separated string (e.g., `"llm,storage"`). The worker's capability list must be a superset.

**Local worker capabilities**: `["storage", "llm", "telegram"]`

**Remote worker**: any set of capabilities, authenticated via `Scheduler.WorkerToken`.

### Task Lifecycle

```
pending → claimed (by worker) → running → completed / failed
```

- `ClaimTask`: atomic SELECT + UPDATE in a transaction
- `StartTask`: set status to `running`, record start time
- `CompleteTask`: store result, publish `TaskCompleted` event
- `FailTask`: store error, publish `TaskFailed` event

### Remote Worker API

For distributed deployments, the dashboard exposes a REST API:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/tasks/` | GET | List tasks |
| `/api/tasks/counts` | GET | Counts by status |
| `/api/tasks/claim` | POST | Claim a task |
| `/api/tasks/:id/start` | POST | Mark as running |
| `/api/tasks/:id/complete` | POST | Complete with result |
| `/api/tasks/:id/fail` | POST | Fail with error |

Authenticated via static bearer token (`scheduler.worker_token`).

## Built-in Jobs

### Insight Generation (`insights`)

- **Interval**: 24 hours (configurable via `skills.insights.interval`)
- **Task type**: `insight.generate`
- **Capabilities**: `llm,storage`
- **Condition**: chat/user must have >= `minFacts` (default 20) facts

**Handler pipeline:**
1. Load all facts for the user
2. Build TF-IDF vectors (tokenize, bigrams, TF-IDF scores)
3. K-means++ clustering: `k = sqrt(numFacts / 3)`, cosine distance, 20 iterations
4. Sample up to 6 cross-cluster pairs (skip already-covered pairs)
5. For each pair: LLM generates insight + scores quality (1-5)
6. Store insights with quality >= threshold

### Insight Cleanup (`insight_cleanup`)

- **Interval**: 1 hour
- **Task type**: `insight.cleanup`
- **Capabilities**: `storage`

Deletes insights where `expires_at < now`. Default TTL is 30 days.

### Tech Fact Analysis (`techfacts`)

- **Interval**: 6 hours (configurable)
- **Task type**: `techfact.analyze`
- **Capabilities**: `llm,storage`
- **Condition**: 10+ messages with 5+ from user

**Handler**: Sends user messages to LLM requesting structured JSON: `[{category, key, value, confidence}]`. Categories include topics, communication style, and behavioral patterns. Upserts into `tech_facts` table.

### Heartbeat (`heartbeat`)

- **Interval**: 6 hours (configurable)
- **Task type**: `heartbeat.check`
- **Capabilities**: `llm,storage,telegram`

**Handler**: Gathers recent facts, insights, and pending reminders. Asks LLM if a check-in message is warranted. If response is not `HEARTBEAT_OK`, sends the message to the user.

### Reminders (`reminders`)

- **Interval**: 30 seconds
- **Task type**: `reminder.fire`
- **Capabilities**: `telegram,storage`

**Handler**: Formats reminder with local time, sends via `MessageSender`, marks as fired.

### Agent Jobs (`agent_jobs`)

- **Interval**: 30 seconds
- **Task type**: `agent.job`
- **Capabilities**: `llm`

Polls `GetDueAgentJobs(now)` for user-defined scheduled LLM tasks. Updates `next_run` immediately (before execution) to prevent duplicates.

**Handler**: Calls `provider.Complete` with the user-defined prompt. Optionally delivers the result to a configured chat.

### Todo Sync (`todo_sync`)

- **Cron**: `0 * * * *` (hourly)
- **Task type**: `todo.sync`
- **Capabilities**: `storage`

**Handler**: Iterates all available `TodoProvider` instances (Todoist, Google Tasks, Craft). For each: `FetchAll` → upsert into `todo_items` → delete stale entries.

## Agent Jobs (User-Defined)

Users can create scheduled LLM tasks via the dashboard:

```json
{
  "name": "Daily Summary",
  "prompt": "Summarize my recent facts and insights",
  "cron_expr": "0 9 * * *",
  "delivery_chat_id": "123456789"
}
```

Fields:
- `name` — display name
- `prompt` — LLM prompt to execute
- `cron_expr` or `interval` — scheduling
- `delivery_chat_id` — where to send the result (optional)

Managed via dashboard: `GET/POST/PUT/DELETE /api/agent-jobs/`
