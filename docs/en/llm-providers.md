# LLM Providers

Iulita supports multiple LLM providers through a decorator-based architecture. Providers can be composed into chains with retry, fallback, caching, routing, and classification layers.


## Profiles and task roles

**Settings → Models** manages encrypted connections and independent profiles for
Claude, OpenAI-compatible services, Ollama, DeepSeek and Z.ai. Everyday, complex,
vision and background roles use a versioned policy. New profiles require explicit
text/tool checks and image checks where appropriate. Paid classification and
backup profiles need their own evidence and activation. These checks incur usage.
See [model profiles and routing](../model-routing.md) for presets, eligibility,
revocation, migration and accounting. Catalog and price tables are snapshots;
account-specific probe results determine availability.

Legacy adapters/decorators below explain individual components, not the complete
managed routing policy. Preserved legacy Claude routing can still use Haiku;
new explicit profiles stay pinned within a tool loop.

## Provider Interface

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

## Request / Response

### Request Structure

```go
Request {
    StaticSystemPrompt  string          // cached by Claude (base + skill prompts)
    SystemPrompt        string          // per-message (time, facts, directives)
    History             []ChatMessage   // conversation history
    Message             string          // current user message
    Images              []ImageAttachment
    Documents           []DocumentAttachment
    Tools               []ToolDefinition
    ToolExchanges       []ToolExchange  // accumulated tool rounds this turn
    ThinkingBudget      int64           // extended thinking tokens (0 = disabled)
    ForceTool           string          // force a specific tool call
    RouteHint           string          // hint for routing provider
}
```

**Key design**: the system prompt is split into `StaticSystemPrompt` (stable, cacheable) and `SystemPrompt` (dynamic, per-message). Non-Claude providers use `FullSystemPrompt()` which concatenates both.

### Response Structure

```go
Response {
    Content    string
    ToolCalls  []ToolCall
    Usage      Usage {
        InputTokens              int
        OutputTokens             int
        CacheReadInputTokens     int
        CacheCreationInputTokens int
    }
}
```

## Claude Provider

The primary provider, using the official `anthropic-sdk-go`.

### Features

- **Prompt caching**: `StaticSystemPrompt` gets `cache_control: ephemeral` — Claude caches this block across requests, reducing input token costs
- **Streaming**: `CompleteStream` uses the streaming API with `ContentBlockDeltaEvent` processing
- **Extended thinking**: when `ThinkingBudget > 0`, thinking config is added and max tokens are bumped
- **ForceTool**: uses `ToolChoiceParamOfTool(name)` to force a specific tool (disables thinking — API constraint)
- **Context overflow detection**: checks error messages for "prompt is too long" / "context_length_exceeded" and wraps with `ErrContextTooLarge` sentinel
- **Document support**: PDF files via `Base64PDFSourceParam`, text files via `PlainTextSourceParam`
- **Image support**: base64-encoded images with media type
- **Hot-reloadable**: model, max tokens, and API key can be updated at runtime via `sync.RWMutex`

### Prompt Caching

The static/dynamic prompt split is the key to efficient Claude usage:

```
Block 1: StaticSystemPrompt (cache_control: ephemeral)
  ├── Base system prompt (persona, instructions)
  └── Skill system prompts (from all enabled skills)

Block 2: SystemPrompt (no cache control)
  ├── ## Current Time
  ├── ## User Directives
  ├── ## User Profile (tech facts)
  ├── ## Remembered Facts
  ├── ## Insights
  └── ## Language directive (if non-English)
```

Block 1 is cached by Claude across requests (costs `cache_creation_input_tokens` on first use, `cache_read_input_tokens` on subsequent hits). Block 2 changes every message and is never cached.

### Streaming

Streaming is used only when `len(req.Tools) == 0` (the assistant disables streaming during the agentic tool-use loop). The streaming event loop processes:

- `ContentBlockDeltaEvent` with `type == "text_delta"` → calls `callback(chunk)` and accumulates
- `MessageStartEvent` → captures input tokens + cache metrics
- `MessageDeltaEvent` → captures output tokens

### Context Overflow Recovery

When the Claude API returns a context overflow error:

1. `isContextOverflowError(err)` wraps it as `llm.ErrContextTooLarge`
2. The assistant's agentic loop catches it via `llm.IsContextTooLarge(err)`
3. If not already compressed this turn: force-compress history and retry (`i--`)
4. If already compressed: propagate the error

### Configuration

| Key | Default | Description |
|-----|---------|-------------|
| `claude.api_key` | — | Anthropic API key (required) |
| `claude.model` | `claude-sonnet-4-6` | Model ID |
| `claude.max_tokens` | 4096 | Max output tokens |
| `claude.base_url` | — | Override API base URL |
| `claude.thinking` | `off` | Thinking mode: `off`, `low`, `medium`, `high` |

## Ollama Provider

Local LLM provider for development and background tasks.

### Limitations

- **No tool support** — returns an error if `len(req.Tools) > 0`
- **No streaming** — `CompleteStream` is not implemented
- Uses `FullSystemPrompt()` (no caching benefit)

### Use Cases

- Local development without API costs
- Background delegate tasks (translations, summaries)
- Cheap classifier for the `ClassifyingProvider`

### API

Calls `POST /api/chat` with messages in OpenAI-compatible format. `ListModels()` hits `GET /api/tags` for model discovery.

### Configuration

| Key | Default | Description |
|-----|---------|-------------|
| `ollama.url` | `http://localhost:11434` | Ollama server URL |
| `ollama.model` | — | Model name |

## OpenAI Provider

OpenAI-compatible REST client. Works with any OpenAI-compatible service (Together AI, Azure, etc.).

### Limitations

- **No tool support** — same as Ollama
- Uses `FullSystemPrompt()`

### Configuration

| Key | Default | Description |
|-----|---------|-------------|
| `openai.api_key` | — | API key |
| `openai.model` | — | Model ID |
| `openai.base_url` | `https://api.openai.com/v1` | API base URL |

## ONNX Embedding Provider

Pure-Go local embedding model for vector search.

- **Model**: `KnightsAnalytics/all-MiniLM-L6-v2` (384 dimensions)
- **Runtime**: `knights-analytics/hugot` — pure Go ONNX (no CGo)
- **Thread safety**: `sync.Mutex` (hugot pipeline is not thread-safe)
- **Caching**: Downloaded once to `~/.local/share/iulita/models/`
- **Normalization**: L2-normalized output vectors (ready for cosine similarity)

See [Memory and Insights](memory-and-insights.md#embeddings) for details on how embeddings are used.

## Provider Decorators

### RetryProvider

Wraps any provider with exponential backoff retry:

- **Max attempts**: 3
- **Base delay**: 500ms
- **Max delay**: 8s
- **Jitter**: 0.5-1.5x random multiplier
- **Retryable codes**: 429, 500, 502, 503, 529 (Anthropic overloaded)
- **Non-retryable**: 4xx (except 429), context overflow

### FallbackProvider

Tries providers in order, returns first success. Useful for `Claude → OpenAI` fallback chains.

### CachingProvider

Response caching is opt-in for stateless requests with a complete identity.
Keys include model/policy identity and request content; conversations, attachments,
tools and opaque reasoning bypass response caching. A cache hit creates no new
provider attempt. Phone private processing bypasses ordinary persistent caches.

### CachedEmbeddingProvider

Caches embeddings per text:

- **Key**: SHA-256 of input text
- **Max entries**: 10,000 (LRU eviction)
- **Batching**: cache misses are grouped for a single provider call
- **Storage**: SQLite `embedding_cache` table

### RoutingProvider

Routes to named providers by `req.RouteHint`. Also parses `hint:<name> <message>` prefix in the user message. Delegates `CompleteStream` to the resolved provider if it's a `StreamingProvider`.

### ClassifyingProvider

Wraps a `RoutingProvider`. On each request:

1. Send a classification prompt to a cheap provider (Ollama): "Classify: simple/complex/creative"
2. Set `RouteHint` based on classification
3. Route to the appropriate provider

Falls back to default on classifier error.

### XMLToolProvider

For providers without native tool calling (Ollama, OpenAI):

1. Injects `<available_tools>` XML block into the system prompt
2. Adds instructions: "To use a tool, respond with `<tool_use name="..."><input>{...}</input></tool_use>`"
3. Strips `Tools` from the request
4. Parses XML tool calls from the response using regex

## Managed routing and failure handling

The shared router resolves explicit profiles and roles, pins a policy snapshot
for the tool loop and preserves provider/profile identity for accounting.
Planner work uses the complex role; background work, compression and delegation
use the configured background policy. The opt-in classifier only sees bounded
ambiguous current text, without history or tools. Its quality gate and ordered
backup behavior are described in [the routing guide](../model-routing.md#paid-selection-and-backup-profiles).

Credential admission applies to pinned snapshots too. Emergency revoke blocks
new calls and attempts cancellation of admitted calls. Authentication, balance,
input and configuration errors are not availability failover triggers. Visible
output or started tool rounds prevent switching profiles. Private phone sends
use their exact consent-bound profile/endpoint and do not retry or fail over.
Claude SDK retries are disabled; outer routing owns retry policy.

## Legacy chain assembly

Legacy setup may compose adapter → retry → fallback → cache → routing/classifier.
That diagram does not describe managed profile admission or private phone work.
Ollama and the basic OpenAI-compatible adapter are text-only at the raw adapter
boundary; XML tool emulation is a separate wrapper, not native tool support.

Prices are estimates from the configured catalog, not current provider invoices.
Unknown usage and prices remain unknown. See [model usage](../model-routing.md#usage-and-operations).
