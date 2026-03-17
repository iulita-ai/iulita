# Multi-Agent Orchestration

Iulita supports parallel sub-agent execution for decomposing complex tasks. The LLM decides when to use orchestration based on the task complexity.

## Overview

The `orchestrate` skill launches multiple specialized sub-agents in parallel. Each sub-agent runs a simplified agentic loop with its own system prompt, tool subset, and optional LLM provider routing. Results are collected and returned to the parent assistant as a structured markdown report.

## Architecture

```
User message
    │
    ▼
Assistant (main agentic loop)
    │ decides orchestration is needed
    │ calls orchestrate tool with agent specs
    ▼
Orchestrate Skill
    │ validates depth (max 1)
    │ builds Budget from config + input overrides
    ▼
Orchestrator (parallel execution via errgroup)
    ├── Runner (agent_1: researcher) ──→ LLM ←→ Tools
    ├── Runner (agent_2: analyst)    ──→ LLM ←→ Tools
    └── Runner (agent_3: planner)    ──→ LLM ←→ Tools
         │
         │ shared atomic token budget
         │ per-agent timeout + context
         │ status events → channels
         ▼
    Collected AgentResults
    │ formatted as markdown
    ▼
Assistant continues with orchestration output
```

## Agent Types

Each agent type has a specialized system prompt and optional default tools.

| Type | System Prompt Focus | Default Tools | Route Hint |
|------|-------------------|---------------|------------|
| `researcher` | Gather information, search web, structured summaries | `web_search`, `webfetch` | — |
| `analyst` | Identify patterns, anomalies, key insights | all | — |
| `planner` | Decompose goals into ordered steps | `datetime` | — |
| `coder` | Write, review, debug code | all | — |
| `summarizer` | Condense input to essential points | all | `ollama` |
| `generic` | General purpose | all | — |

When `DefaultTools` is nil (analyst, coder, summarizer, generic), the agent has access to all registered tools minus security-filtered ones.

## How to Use

The LLM decides autonomously when to orchestrate. The `orchestrate` tool's SKILL.md is injected into the system prompt, telling the LLM when orchestration is appropriate:

- Tasks that benefit from parallel investigation (research + analysis)
- Multi-part tasks where different aspects can be worked on simultaneously
- Tasks requiring different expertise

**Example LLM tool call:**
```json
{
  "agents": [
    {"id": "research", "type": "researcher", "task": "Find the latest benchmarks for Go vs Rust web frameworks"},
    {"id": "analysis", "type": "analyst", "task": "Given the research findings, identify which framework is best for high-throughput APIs"}
  ],
  "timeout": "90s",
  "max_tokens": 50000
}
```

## Budget System

The budget controls resource consumption across all agents in an orchestration.

### Shared Token Budget

All agents share a single `atomic.Int64` token counter. Each agent's LLM call deducts from the shared pool:

```
sharedTokens.Store(budget.MaxTokens)  // e.g., 100000

// Each agent after each LLM call:
remaining := sharedTokens.Add(-turnTokens)
if remaining <= 0 {
    // Budget exhausted — use partial output, stop agent
}
```

**Soft cap by design**: Multiple agents may pass the pre-flight budget check concurrently before any deducts tokens, causing the budget to be overrun by up to `(N_agents - 1) * tokens_per_call`. This is intentional — a hard reservation pattern would add mutex contention without meaningful benefit at the typical 3-5 agent parallelism level.

### Per-Agent Limits

| Parameter | Default | Override |
|-----------|---------|----------|
| Max turns (LLM calls) | 10 | `Budget.MaxTurns` |
| Timeout (wall clock) | 60s | `Budget.Timeout` or input `timeout` |
| Max parallel agents | 5 | `Budget.MaxAgents` or config |
| Shared token budget | unlimited | `Budget.MaxTokens` or input `max_tokens` |

## Depth Enforcement

Sub-agents cannot spawn further sub-agents. This is enforced at two levels:

1. **Context depth key**: `WithDepth(ctx, DepthFrom(ctx)+1)` is set on each sub-agent context. The orchestrate skill checks `DepthFrom(ctx) >= MaxDepth` and returns an error string if exceeded.

2. **Tool filtering**: The `buildTools()` method always excludes the `orchestrate` tool from sub-agent tool lists, regardless of depth. This is a belt-and-suspenders approach.

`MaxDepth = 1` means: depth 0 = parent assistant, depth 1 = sub-agent (maximum).

## Security

### Approval Filtering

Sub-agents bypass the normal approval flow (they run in trusted context), so they must not have access to approval-gated skills:

```go
// In buildTools():
if ad, ok := s.(skill.ApprovalDeclarer); ok && ad.ApprovalLevel() > skill.ApprovalAuto {
    continue // skip ApprovalPrompt and ApprovalManual skills
}
```

This means sub-agents cannot access:
- `shell_exec` (ApprovalManual)
- Docker executor (ApprovalPrompt)
- Any custom skill declaring non-Auto approval

### Tool Allowlists

Each agent spec can optionally declare an explicit tool allowlist:

```json
{"id": "researcher", "type": "researcher", "task": "...", "tools": ["web_search", "webfetch"]}
```

If no explicit tools are provided, the agent type's `DefaultTools` are used. If those are also nil, the agent gets all registered tools (minus filtered ones).

## Status Events Protocol

The orchestrator emits status events through the existing `StatusNotifier` to all connected channels:

| Event | When | Data Fields |
|-------|------|-------------|
| `orchestration_started` | Before launching agents | `agent_count` |
| `agent_started` | Per agent, before run | `agent_id`, `agent_type` |
| `agent_progress` | Per agent, after each LLM turn | `agent_id`, `turn` |
| `agent_completed` | Per agent, on success | `agent_id`, `tokens`, `duration_ms` |
| `agent_failed` | Per agent, on error | `agent_id`, `error` |
| `orchestration_done` | After all agents finish | `success_count`, `total_tokens`, `duration_ms` |

### Event Bus Integration

Two event bus events are published for dashboard/metrics integration:

- `AgentOrchestrationStarted` — payload: `ChatID`, `AgentCount`
- `AgentOrchestrationDone` — payload: `ChatID`, `AgentCount`, `SuccessCount`, `TotalTokens`, `DurationMs`

### Event Delivery Guarantee

Post-agent completion events use `context.Background()` with a 5-second timeout to guarantee delivery even if the parent context's deadline has expired.

## LLM Provider Routing

Each agent type can have a `RouteHint` for provider selection:

1. **Spec-level override**: `AgentSpec.RouteHint` (from LLM input)
2. **Profile default**: `AgentTypeProfile.RouteHint` (e.g., `summarizer → "ollama"`)
3. **Empty**: use the default provider

The `RouteHint` is passed on the `llm.Request` and dispatched by the existing `RoutingProvider` in the LLM chain.

## Configuration

| Key | Default | Description |
|-----|---------|-------------|
| `skills.orchestrate.enabled` | true | Enable/disable the orchestrate skill |
| `skills.orchestrate.max_tokens` | 0 (unlimited) | Shared token budget cap |
| `skills.orchestrate.max_agents` | 5 | Maximum parallel agents |
| `skills.orchestrate.timeout` | 60s | Per-agent wall-clock timeout |
| `skills.orchestrate.request_timeout` | 1h | Overall orchestration deadline (max 4h) |

All config keys support hot-reload via `ConfigReloadable`. The skill uses `sync.RWMutex` to protect concurrent reads during orchestration and writes from config changes.

## Dynamic Timeout

### TimeoutDeclarer Interface

Skills can declare how much time they need via the `TimeoutDeclarer` interface. The orchestrate skill uses this to request extended deadlines beyond the default request timeout.

### DefaultDeadlineExtender

`DefaultDeadlineExtender` breaks the parent context deadline using `context.WithoutCancel`, then applies a new deadline based on the skill's declared timeout. This ensures that long-running orchestrations are not prematurely killed by the parent request's deadline.

### Agentic Loop Context Extension

The deadline extension happens at the agentic loop level (not just inside `executeSkill`). When a skill declares a timeout via `TimeoutDeclarer`, the entire loop iteration gets the extended context, allowing the skill to use its full declared time budget.

### Limits

- **Default**: 1 hour (`skills.orchestrate.request_timeout`)
- **Hard cap**: 4 hours (`maxRequestTimeout`) — any value above this is clamped
- **Configurable**: via `skills.orchestrate.request_timeout` in the dashboard or config store

## Frontend UI

The `AgentProgress.vue` component displays real-time agent status in the Chat view:

- Shows when orchestration is active with the total agent count
- Per-agent row with type icon, name, status icon, and progress info
- Completed agents show duration; failed agents show truncated error
- Agent type icons: researcher (magnifier), analyst (chart), planner (clipboard), coder (laptop), summarizer (memo), generic (robot)

### i18n Keys

6 frontend keys added to all locale JSON files:

| Key | English |
|-----|---------|
| `chat.orchestrationStarted` | "Launching {count} agents..." |
| `chat.agentStarted` | "{id} ({type}) started" |
| `chat.agentProgress` | "{id}: turn {turn}" |
| `chat.agentCompleted` | "{id} completed" |
| `chat.agentFailed` | "{id} failed" |
| `chat.orchestrationDone` | "All agents finished ({success}/{total} succeeded)" |

## Runner Details

The `Runner` is a simplified agentic loop, deliberately stripped down compared to the main assistant:

| Feature | Main Assistant | Sub-Agent Runner |
|---------|---------------|------------------|
| Storage | SQLite history | None |
| History | Last 50 messages | None (fresh context) |
| Compression | Auto at 80% | None |
| Approvals | Full flow | Skipped (filtered at tool level) |
| Streaming | Yes | No |
| Context overflow retry | Yes | No |
| Tool exchanges | Accumulated per turn | Accumulated per turn |

This simplicity is intentional — sub-agents are short-lived, single-purpose workers that do not need the overhead of persistence, streaming, or conversation management.
