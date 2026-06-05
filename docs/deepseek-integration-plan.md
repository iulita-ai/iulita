# Implementation Plan — DeepSeek Provider Support for iulita

**Module:** `github.com/iulita-ai/iulita` · **Branch:** `feat/deepseek` · **Date context:** 2026-06-05
**Note:** DeepSeek legacy aliases (`deepseek-chat`, `deepseek-reasoner`) shut down **2026-07-24 15:59 UTC**.

**Strategy:** Dedicated `internal/llm/deepseek` package mirroring the **Claude** provider contract (NOT the OpenAI one — it errors on tools and has no streaming). Shared-contract mutations (`reasoning_content`, cache-aware cost) are isolated into Phase 2. All review CRITICAL/HIGH findings are folded into Phase 1; deferred items are listed in §8.

---

## 1. Goal & Scope

- **Phase 1 (ships independently):** non-thinking DeepSeek chat model end-to-end — tool-use, SSE streaming, cost tracking, route-hint routing, typed retry, config/schema/wizard/dashboard integration, delegate target. **Zero edits** to shared `llm.Request`/`llm.Response`/`llm.ToolExchange`.
- **Phase 2 (separate PR):** `deepseek-reasoner` / thinking-mode `reasoning_content` multi-turn threading + cache hit/miss cost split. These mutate the shared `llm` contract and `cost` code.

**Out of scope:** vision/images (chat models have none — dropped with WARN, never error), beta `/beta` strict-tools, FIM/prefix.

**Default model decision (review HIGH):** default `deepseek.model` to **`deepseek-v4-flash`** (live, non-deprecated), NOT `deepseek-chat` — a fresh install after 2026-07-24 would otherwise default to a dead model. Legacy aliases stay priced for back-compat.

---

## 2. Chosen Approach + Why

A **dedicated package** is the only correct option:
- `internal/llm/openai/openai.go` is unusable: errors when `len(Tools)>0` (`openai.go:113-116`), no `CompleteStream`, drops `Model`/`Provider`/cache/`tool_calls`, logs status-only.
- `internal/llm/claude/claude.go` is the structural template: `Complete`/`CompleteStream`, `getParams`/`Update*` via `sync.RWMutex`, `FullSystemPrompt()`, `ErrContextTooLarge` wrapping, `Model`/`Provider` population.
- DeepSeek is OpenAI-wire-compatible → reuse OpenAI JSON shapes and existing `openaillm.ListModels` for the `/v1/models` dropdown (only credential resolution differs).

**Mandatory:** typed `apiError` implementing `StatusCode() int` (the `llm.HTTPStatusError` interface, `retry.go:91-104`) — without it DeepSeek's 429s/503s silently never retry.

---

## 3. Phase 1 — DeepSeek chat end-to-end

### Step 1.1 — New provider package
**File (new):** `internal/llm/deepseek/deepseek.go` · package `deepseek`

```go
type Provider struct {
    apiKey, model, baseURL string
    maxTokens              int
    httpClient             *http.Client
    mu                     sync.RWMutex
}
func New(apiKey, model string, maxTokens int, baseURL string, httpClient *http.Client) *Provider
```
- `baseURL` default → `https://api.deepseek.com/v1` when empty.
- **httpClient default (MEDIUM):** when nil, build from shared transport pattern, NOT `http.DefaultClient` (proxy-blind/timeout-less). DI passes `llmHTTPClient`.
- `UpdateModel`, `UpdateMaxTokens`, `getParams()` mirroring `claude.go:44-63` (RWMutex-guarded).

**JSON types** (OpenAI shape; DeepSeek fields `omitempty`):
```go
type chatMessage struct {
    Role       string     `json:"role"`
    Content    *string    `json:"content,omitempty"` // POINTER: distinguish "" from absent
    ToolCalls  []toolCall `json:"tool_calls,omitempty"`
    ToolCallID string     `json:"tool_call_id,omitempty"`
}
type toolCall struct {
    ID       string `json:"id"`
    Type     string `json:"type"` // "function"
    Function struct {
        Name      string `json:"name"`
        Arguments string `json:"arguments"` // JSON STRING, not object
    } `json:"function"`
}
type toolDef struct {
    Type     string `json:"type"`
    Function struct {
        Name        string          `json:"name"`
        Description string          `json:"description,omitempty"`
        Parameters  json.RawMessage `json:"parameters"`
    } `json:"function"`
}
type streamOptions struct{ IncludeUsage bool `json:"include_usage"` }
type chatRequest struct {
    Model         string         `json:"model"`
    Messages      []chatMessage  `json:"messages"`
    MaxTokens     int            `json:"max_tokens,omitempty"`
    Tools         []toolDef      `json:"tools,omitempty"`
    ToolChoice    any            `json:"tool_choice,omitempty"`
    Stream        bool           `json:"stream,omitempty"`
    StreamOptions *streamOptions `json:"stream_options,omitempty"` // nil for Complete
}
type chatUsage struct {
    PromptTokens          int64 `json:"prompt_tokens"`
    CompletionTokens      int64 `json:"completion_tokens"`
    PromptCacheHitTokens  int64 `json:"prompt_cache_hit_tokens"`  // parsed P1, mapped P2
    PromptCacheMissTokens int64 `json:"prompt_cache_miss_tokens"`
}
```

**Pure (network-free) helpers** for unit tests: `buildMessages`, `buildToolDefs`, `buildToolChoice`, `accumulateStreamToolCalls`, `mapUsage`, `isContextOverflowError`.

**`buildMessages`:**
1. System from `req.FullSystemPrompt()` (no `cache_control` split — DeepSeek caches automatically).
2. History: `RoleAssistant`→`assistant`, else `user`; **skip empty content** (mirror `claude.go:69`).
3. Current `req.Message` as `user`.
4. **Images/Documents (MEDIUM):** when present, structured **WARN** (once/request) and skip — do NOT error (would break the agentic loop).
5. Replay `req.ToolExchanges` (mirror `claude.go:113-144` in OpenAI shape): assistant msg `{Content: strPtr(text), ToolCalls:[...]}` with `Arguments = string(tc.Input)`; then one `{Role:"tool", ToolCallID, Content: strPtr(tr.Content)}` per result (empty → `strPtr("")`).

**`buildToolDefs` (LOW):** `Parameters = t.InputSchema` verbatim, **but** if empty/`null` → substitute `{"type":"object","properties":{}}` (DeepSeek/OpenAI reject `"parameters": null` with 400).

**`buildToolChoice`:** `ForceTool != ""` → `{type:function,function:{name}}`; else nil (= auto).

**`Complete`:** build `chatRequest{Stream:false, StreamOptions:nil}`; POST `/chat/completions`; headers `Content-Type`, `Authorization: Bearer`. **`defer resp.Body.Close()` right after Do() err-check.** Non-200 → read with `io.LimitReader(resp.Body, 8<<10)`; if overflow → wrap `llm.ErrContextTooLarge`, else `&apiError{status, body}`. Decode `choices[0].message.content`/`tool_calls`; `Usage = mapUsage`; set `Model`, `Provider="deepseek"`. **P1:** `InputTokens=PromptTokens`, cache fields zero.

**`CompleteStream`:** `Stream:true`, `StreamOptions:{IncludeUsage:true}`. `defer Body.Close()` after Do(). SSE via `bufio.Scanner` with **enlarged buffer** `Buffer(make([]byte,0,64K),1M)`; skip blanks + `:`-comments; strip `data: `; stop on `[DONE]`. Per `delta.content` → `callback` + accumulate. **Never deliver `reasoning_content` to callback** (Phase 2). Accumulate `tool_calls` by `Index` (concat Arguments). Final `usage` chunk → `mapUsage`.

### Step 1.2 — Typed retryable HTTP error (CRITICAL)
```go
type apiError struct { status int; body string } // body ≤8KB
func (e *apiError) Error() string     { return fmt.Sprintf("deepseek returned status %d: %s", e.status, e.body) }
func (e *apiError) StatusCode() int   { return e.status }
```
Satisfies `llm.HTTPStatusError` → `RetryProvider` retries 429/500/502/503/529. **Log-leak (HIGH):** body bounded via `io.LimitReader(8KB)`, prefer parsing `error.message`/`error.code`. Test: 100KB body → bounded error string.

**`isContextOverflowError` (MEDIUM — tighten):** prefer `error.code == "context_length_exceeded"`; substring fallbacks only `"context length"`, `"maximum context length"`, `"context_length_exceeded"`. **Drop bare `"too long"`** (misclassifies unrelated 400s). Test asserts a 400 with `"too long"` is NOT overflow.

### Step 1.3 — Config struct (`internal/config/config.go`)
- Add `DeepSeek DeepSeekConfig \`koanf:"deepseek"\`` to root config.
- After `OpenAIConfig`:
```go
type DeepSeekConfig struct {
    APIKey    string `koanf:"api_key"`
    Model     string `koanf:"model"`
    MaxTokens int    `koanf:"max_tokens"`
    BaseURL   string `koanf:"base_url"`
    Fallback  bool   `koanf:"fallback"`
}
```
- `HasAnyLLMProvider`: add `if c.DeepSeek.APIKey != "" && c.DeepSeek.Model != "" { return true }`.
- `Validate` error string: `(Claude, OpenAI, DeepSeek, or Ollama)`.
- `structToMap`: add **ONLY** `m["deepseek.max_tokens"]` (mirror OpenAI exactly — adding model/base_url with empty strings risks shadowing keyring/env layers via koanf merge).

### Step 1.4 — Defaults (`internal/config/defaults.go`)
```go
DeepSeek: DeepSeekConfig{ Model: "deepseek-v4-flash", MaxTokens: 4096 }, // BaseURL empty → provider default
```
`defaultModelPrices()` (P1 bills all input at cache-MISS rate — over-estimates hits, safe direction):
```go
"deepseek-v4-flash": {InputPerMillion: 0.14, OutputPerMillion: 0.28},
"deepseek-chat":     {InputPerMillion: 0.14, OutputPerMillion: 0.28}, // deprecated alias
"deepseek-reasoner": {InputPerMillion: 0.14, OutputPerMillion: 0.28}, // deprecated alias
```
Do **NOT** add `deepseek-v4-pro` (pricing is a research open question). **Startup WARN (MEDIUM)** in main.go when `cost.enabled` and configured model has no price entry (silent $0 billing footgun).

### Step 1.5 — Schema (`internal/config/schema.go`)
- Add enum `ModelSourceDeepSeek ModelSource = "deepseek"` (CRITICAL — `ModelSourceOpenAI` would route dropdown to OpenAI creds).
- Add `deepseek` section (`WizardOrder:3`, `Optional:true`) with fields: `api_key` (`FieldSecret`, `Secret:true`), `model` (default `deepseek-v4-flash`, `ModelSource: ModelSourceDeepSeek`), `max_tokens` (4096), `base_url` (`FieldURL`), `fallback` (`FieldBool`, help: "disables streaming for the session").
- `routing.default_provider` Options: `["claude","openai","deepseek","ollama"]`.
- `Secret:true` auto-includes `deepseek.api_key` in `SchemaSecretKeys()` → encrypted at rest. No i18n entries (matches openai/ollama — English-only).

### Step 1.6 — Dashboard models dropdown (CRITICAL, not optional)
- `internal/dashboard/handlers.go` `handleListModels`: add `deepseek` case resolving DeepSeek creds, default baseURL `https://api.deepseek.com/v1`, call `openaillm.ListModels(...)` with the proxy-aware client.
- `ui/src/api.ts`: widen union `model_source?: '' | 'openai' | 'ollama' | 'deepseek'`. Settings.vue renders dynamically — no further Vue edits.

### Step 1.7 — Console wizard (`internal/config/setup.go`)
- `llmProviders` slice: add `{"deepseek","deepseek","DeepSeek"}`.
- `isLLMSection` (both sites): add `|| section.Name == "deepseek"`.
- `fetchModelsForWizard`: add `case ModelSourceDeepSeek`.
- `keyringAccountForKey`: add `case "deepseek.api_key": return "deepseek-api-key"`.

### Step 1.8 — Web wizard (`internal/dashboard/wizard_handlers.go`)
- `sectionProviders`: add `"deepseek"`.
- `hasLLM` block + `handleWizardComplete` gate: add DeepSeek check (so a DeepSeek-only setup can complete the wizard).

---

## 4. DI Wiring (`cmd/iulita/main.go`)

- **(a) Import** `deepseekllm "github.com/iulita-ai/iulita/internal/llm/deepseek"`.
- **(b) Env mapping** `credStore.RegisterEnvMapping("deepseek.api_key", "DEEPSEEK_API_KEY")`.
- **(c) overrideKeys** append `{"deepseek.api_key", &cfg.DeepSeek.APIKey}`, `{"deepseek.model", &cfg.DeepSeek.Model}`.
- **(d) Provider build** right after the OpenAI block, **before** the response-cache wrap. Use `llmHTTPClient` (no client timeout — context-driven for long streaming), NOT the 30s `httpClient`. Capture `deepseekIsPrimary` BEFORE cache-wrap:
```go
var rawDeepSeek *deepseekllm.Provider
deepseekIsPrimary := false
if cfg.DeepSeek.APIKey != "" && cfg.DeepSeek.Model != "" {
    mt := cfg.DeepSeek.MaxTokens; if mt <= 0 { mt = 4096 }
    rawDeepSeek = deepseekllm.New(cfg.DeepSeek.APIKey, cfg.DeepSeek.Model, mt, cfg.DeepSeek.BaseURL, llmHTTPClient)
    deepseekProvider = llm.NewRetryProvider(rawDeepSeek, llm.DefaultRetryConfig())
    if llmProvider == nil { llmProvider = deepseekProvider; deepseekIsPrimary = true }
    else if cfg.DeepSeek.Fallback { llmProvider = llm.NewFallbackProvider(llmProvider, deepseekProvider) /* WARN: streaming disabled */ }
}
```
> **Streaming-fallback trap (HIGH):** `FallbackProvider` implements only `Complete` (confirmed — no `CompleteStream`). When wired as routing default, `.(StreamingProvider)` assertions fail → silent non-streaming for the whole session. **Phase 1: document + WARN.** Clean fix (add `CompleteStream` to `FallbackProvider`) deferred to a follow-up.

- **(e) Routing:** use identity-stable `deepseekIsPrimary` for `hasSecondaryProviders` (after cache-wrap, `llmProvider != deepseekProvider` is always true → false-positive). Register `providerMap["deepseek"]`/`routes["deepseek"]`; if `deepseekIsPrimary`, alias the (cache-wrapped) primary so `hint:deepseek` resolves. No `router.go` change.
- **(f) Delegate skill:** add `delegateProviders["deepseek"]` using `llmHTTPClient`; set `defaultDelegate` if empty.
- **(g) Hot-reload:** DEFERRED to Phase 2 (`Update*` methods exist, trivial later). Until then model/key changes need restart (same as `claude.api_key` today).

---

## 5. Config Keys & Cost Entries

| Config key | Type | Default | Secret |
|---|---|---|---|
| `deepseek.api_key` | secret | — | yes (encrypted); env `DEEPSEEK_API_KEY`; keyring `deepseek-api-key` |
| `deepseek.model` | string | `deepseek-v4-flash` | no (dynamic dropdown) |
| `deepseek.max_tokens` | int | `4096` | no (only key in `structToMap`) |
| `deepseek.base_url` | url | `""` → `https://api.deepseek.com/v1` | no |
| `deepseek.fallback` | bool | `false` | no (disables session streaming) |

| Price key | Input $/1M | Output $/1M |
|---|---|---|
| `deepseek-v4-flash` | 0.14 | 0.28 (cache-MISS rate, P1) |
| `deepseek-chat` / `deepseek-reasoner` | 0.14 | 0.28 (deprecated aliases) |
| `deepseek-v4-pro` | — | — (unpriced → $0 + startup WARN) |

Phase 2 adds `CacheHitPerMillion: 0.0028`.

---

## 6. Phase 2 (separate PR) — reasoner & caching

- **2.1 `reasoning_content` threading:** add `ReasoningContent` to `ToolExchange` + `Response` (purely additive). Capture `message.reasoning_content`/`delta.reasoning_content` separately (never to callback). **CRITICAL 400 rule:** legacy reasoner 400s if `reasoning_content` present on a no-tool turn, and 400s if absent on a tool-call turn — strip/retain logic lives **entirely inside `deepseek.buildMessages`** (keeps provider quirk out of shared code). Don't send `temperature`/`top_p`/`logprobs`. **Verify tool-support empirically** (docs conflict).
- **2.2 Cache hit/miss cost split:** add `CacheHitPerMillion` to `ModelPrice`; rewrite `tracker.go` `Calculate` to bill `CacheReadInputTokens` at hit rate (fall back to input rate when zero → Claude/OpenAI unaffected). `mapUsage` mutually-exclusive branch (invariant `InputTokens + CacheReadInputTokens + CacheCreationInputTokens == prompt_tokens`).
- **2.3 Hot-reload parity:** extend `registerConfigReload` for `*deepseekllm.Provider`; add `case "deepseek.model"`/`"deepseek.max_tokens"`.

---

## 7. Test Plan

**New:** `internal/llm/deepseek/deepseek_test.go` (white-box, table-driven, `-race`), mirroring `claude_test.go`.

**Pure helpers:** `buildToolDefs` verbatim passthrough + empty/`null`→`{}` object; `buildMessages`/ToolExchange replay (arguments as JSON string, tool-result `content:""` present not dropped, empty history skipped); `buildToolChoice`; SSE tool-call accumulation by index; `mapUsage` P1 fallback; `apiError.StatusCode()` + `errors.As` to `llm.HTTPStatusError` + 100KB-body bounding; `isContextOverflowError` (`context_length_exceeded` → overflow, unrelated `"too long"` → not).

**HTTP (`httptest.Server`, `New(key,model,mt,srv.URL,srv.Client())`):** `Complete` (assert Bearer/model/messages, Provider=="deepseek", no `stream`/`stream_options`); `CompleteStream` (keep-alive comment ignored, content order, tool reassembly, usage, `include_usage:true`); retry 429→200 via `NewRetryProvider`; overflow 400 → `errors.Is(ErrContextTooLarge)`; ctx-cancel mid-stream → prompt return, no goroutine leak.

**Cross-package:** config (`HasAnyLLMProvider`, `Validate`, `structToMap` has only `deepseek.max_tokens`, schema section, `SchemaSecretKeys`, `ModelSourceDeepSeek`); cost (`Calculate("deepseek-v4-flash")` non-zero, `deepseek-v4-pro`==0); routing (DeepSeek-primary resolves `hint:deepseek`).

**Gates:** `go build ./...`, `go vet ./...`, `gofmt -l` empty, `go test -race ./internal/llm/deepseek/... ./internal/config/... ./internal/cost/... ./internal/dashboard/... ./cmd/...`, `cd ui && npm run test`.

---

## 8. Risks & Deferred

**Risks (P1):** retry silently off without `StatusCode()` (Step 1.2); tool args are JSON strings at both boundaries; SSE fragmentation by index + 1MB buffer; legacy deprecation 2026-07-24 (default to live model); cost over-estimate P1 (safe) + unpriced models bill $0 (WARN); fallback disables streaming (WARN+doc); error-body log-leak bounded (8KB); `http.DefaultClient` proxy-blindness avoided via DI.

**Deferred:** hot-reload for model/max_tokens (2.3); `FallbackProvider.CompleteStream` (follow-up, benefits OpenAI too); `reasoning_content`/thinking (2.1); cache-aware cost split (2.2); `deepseek-v4-pro` price; beta `/beta` features.

---

## 9. Commit / PR Breakdown

**PR 1 — feat: add DeepSeek provider (chat, streaming, tools, cost)** (Phase 1, ships independently)
1. `feat(llm): add deepseek provider package` — `internal/llm/deepseek/{deepseek.go,deepseek_test.go}`
2. `feat(config): add DeepSeek config, schema, defaults, prices` — `config.go`, `defaults.go`, `schema.go` (incl. `ModelSourceDeepSeek`)
3. `feat(dashboard): wire DeepSeek model dropdown + web wizard gating` — `handlers.go`, `wizard_handlers.go`, `ui/src/api.ts`
4. `feat(config): console wizard + keyring for DeepSeek` — `setup.go`
5. `feat(cmd): DI wiring for DeepSeek provider, routing, delegate` — `main.go` (+ startup price WARN + fallback WARN)

**PR 2 — feat: DeepSeek reasoner (thinking) + cache-aware cost** (Phase 2)
1. `feat(llm): add ReasoningContent to contract + deepseek thinking mode`
2. `feat(cost): cache hit/miss split for DeepSeek`
3. `feat(config): DeepSeek hot-reload parity`

**Follow-up (independent):** `feat(llm): add CompleteStream to FallbackProvider`.

**Files — New:** `internal/llm/deepseek/deepseek.go`, `internal/llm/deepseek/deepseek_test.go`.
**Edit:** `internal/config/{config.go,defaults.go,schema.go,setup.go}`, `internal/dashboard/{handlers.go,wizard_handlers.go}`, `cmd/iulita/main.go`, `ui/src/api.ts`. **Phase 2:** `internal/llm/llm.go`, `internal/cost/tracker.go`, `internal/assistant/*`.
