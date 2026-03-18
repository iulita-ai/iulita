# LLM 提供商

Iulita 通过基于装饰器的架构支持多个 LLM 提供商。提供商可以组合成包含重试、回退、缓存、路由和分类层的链。

## 提供商接口

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

## 请求/响应

### 请求结构

```go
Request {
    StaticSystemPrompt  string          // 由 Claude 缓存（基础 + 技能提示词）
    SystemPrompt        string          // 每条消息（时间、事实、指令）
    History             []ChatMessage   // 对话历史
    Message             string          // 当前用户消息
    Images              []ImageAttachment
    Documents           []DocumentAttachment
    Tools               []ToolDefinition
    ToolExchanges       []ToolExchange  // 本轮累积的工具调用
    ThinkingBudget      int64           // 扩展思考令牌数（0 = 禁用）
    ForceTool           string          // 强制特定工具调用
    RouteHint           string          // 路由提供商的提示
}
```

**关键设计**：系统提示词分为 `StaticSystemPrompt`（稳定、可缓存）和 `SystemPrompt`（动态、每条消息）。非 Claude 提供商使用 `FullSystemPrompt()` 将两者拼接。

### 响应结构

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

## Claude 提供商

主要提供商，使用官方 `anthropic-sdk-go`。

### 功能特性

- **提示词缓存**：`StaticSystemPrompt` 获得 `cache_control: ephemeral` — Claude 跨请求缓存此块，减少输入令牌费用
- **流式传输**：`CompleteStream` 使用流式 API 处理 `ContentBlockDeltaEvent`
- **扩展思考**：当 `ThinkingBudget > 0` 时，添加思考配置并增加最大令牌数
- **ForceTool**：使用 `ToolChoiceParamOfTool(name)` 强制特定工具（禁用思考 — API 约束）
- **上下文溢出检测**：检查错误消息中的 "prompt is too long" / "context_length_exceeded"，并用 `ErrContextTooLarge` 哨兵包装
- **文档支持**：通过 `Base64PDFSourceParam` 支持 PDF 文件，通过 `PlainTextSourceParam` 支持文本文件
- **图片支持**：带媒体类型的 base64 编码图片
- **热重载**：模型、最大令牌数和 API 密钥可通过 `sync.RWMutex` 在运行时更新

### 提示词缓存

静态/动态提示词拆分是高效使用 Claude 的关键：

```
块 1：StaticSystemPrompt（cache_control: ephemeral）
  ├── 基础系统提示词（角色、指令）
  └── 技能系统提示词（来自所有已启用的技能）

块 2：SystemPrompt（无缓存控制）
  ├── ## Current Time
  ├── ## User Directives
  ├── ## User Profile（技术事实）
  ├── ## Remembered Facts
  ├── ## Insights
  └── ## Language directive（如果非英语）
```

块 1 由 Claude 跨请求缓存（首次使用时花费 `cache_creation_input_tokens`，后续命中花费 `cache_read_input_tokens`）。块 2 每条消息都不同，从不缓存。

### 流式传输

流式传输仅在 `len(req.Tools) == 0` 时使用（助手在代理工具使用循环期间禁用流式传输）。流式事件循环处理：

- `ContentBlockDeltaEvent`，`type == "text_delta"` → 调用 `callback(chunk)` 并累积
- `MessageStartEvent` → 捕获输入令牌数 + 缓存指标
- `MessageDeltaEvent` → 捕获输出令牌数

### 上下文溢出恢复

当 Claude API 返回上下文溢出错误时：

1. `isContextOverflowError(err)` 将其包装为 `llm.ErrContextTooLarge`
2. 助手的代理循环通过 `llm.IsContextTooLarge(err)` 捕获
3. 如果本轮尚未压缩：强制压缩历史并重试（`i--`）
4. 如果已经压缩：传播错误

### 配置

| 键 | 默认值 | 描述 |
|------|--------|------|
| `claude.api_key` | — | Anthropic API 密钥（必需） |
| `claude.model` | `claude-sonnet-4-5-20250929` | 模型 ID |
| `claude.max_tokens` | 8192 | 最大输出令牌数 |
| `claude.base_url` | — | 覆盖 API 基础 URL |
| `claude.thinking` | 0 | 扩展思考预算（0 = 禁用） |

## Ollama 提供商

用于开发和后台任务的本地 LLM 提供商。

### 限制

- **不支持工具** — 如果 `len(req.Tools) > 0` 则返回错误
- **不支持流式传输** — `CompleteStream` 未实现
- 使用 `FullSystemPrompt()`（无缓存收益）

### 使用场景

- 无 API 费用的本地开发
- 后台委派任务（翻译、摘要）
- `ClassifyingProvider` 的廉价分类器

### API

调用 `POST /api/chat`，消息格式兼容 OpenAI。`ListModels()` 调用 `GET /api/tags` 进行模型发现。

### 配置

| 键 | 默认值 | 描述 |
|------|--------|------|
| `ollama.url` | `http://localhost:11434` | Ollama 服务器 URL |
| `ollama.model` | `llama3` | 模型名称 |

## OpenAI 提供商

兼容 OpenAI 的 REST 客户端。适用于任何兼容 OpenAI 的服务（Together AI、Azure 等）。

### 限制

- **不支持工具** — 与 Ollama 相同
- 使用 `FullSystemPrompt()`

### 配置

| 键 | 默认值 | 描述 |
|------|--------|------|
| `openai.api_key` | — | API 密钥 |
| `openai.model` | `gpt-4` | 模型 ID |
| `openai.base_url` | `https://api.openai.com/v1` | API 基础 URL |

## ONNX 嵌入提供商

用于向量搜索的纯 Go 本地嵌入模型。

- **模型**：`KnightsAnalytics/all-MiniLM-L6-v2`（384 维）
- **运行时**：`knights-analytics/hugot` — 纯 Go ONNX（无 CGo）
- **线程安全**：`sync.Mutex`（hugot 管道不是线程安全的）
- **缓存**：首次下载到 `~/.local/share/iulita/models/`
- **归一化**：L2 归一化输出向量（可直接用于余弦相似度）

详情请参阅[记忆与洞察](memory-and-insights.md#嵌入)。

## 提供商装饰器

### RetryProvider

使用指数退避重试包装任何提供商：

- **最大尝试次数**：3
- **基础延迟**：500ms
- **最大延迟**：8s
- **抖动**：0.5-1.5 倍随机乘数
- **可重试状态码**：429、500、502、503、529（Anthropic 过载）
- **不可重试**：4xx（429 除外）、上下文溢出

### FallbackProvider

按顺序尝试提供商，返回第一个成功的。适用于 `Claude → OpenAI` 回退链。

### CachingProvider

按输入哈希缓存 LLM 响应：

- **键**：`systemPrefix[:200] + "|" + message` 的 SHA-256
- **TTL**：60 分钟（可配置）
- **最大条目**：1000（LRU 淘汰）
- **跳过**：带工具或工具交换的请求（非确定性）
- **存储**：SQLite `response_cache` 表

### CachedEmbeddingProvider

按文本缓存嵌入：

- **键**：输入文本的 SHA-256
- **最大条目**：10,000（LRU 淘汰）
- **批处理**：缓存未命中被分组进行单次提供商调用
- **存储**：SQLite `embedding_cache` 表

### RoutingProvider

通过 `req.RouteHint` 路由到命名提供商。也解析用户消息中的 `hint:<name> <message>` 前缀。如果解析到的提供商是 `StreamingProvider`，则将 `CompleteStream` 委派给它。

### ClassifyingProvider

包装 `RoutingProvider`。每次请求时：

1. 向廉价提供商（Ollama）发送分类提示词："分类：simple/complex/creative"
2. 根据分类设置 `RouteHint`
3. 路由到相应的提供商

分类器出错时回退到默认值。

### XMLToolProvider

用于不支持原生工具调用的提供商（Ollama、OpenAI）：

1. 将 `<available_tools>` XML 块注入系统提示词
2. 添加指令："要使用工具，请用 `<tool_use name="..."><input>{...}</input></tool_use>` 格式回复"
3. 从请求中移除 `Tools`
4. 使用正则表达式从响应中解析 XML 工具调用

## 提供商链组装

链在 `cmd/iulita/main.go` 中构建：

```
Claude Provider
    └→ Retry Provider
        └→ [可选] Fallback Provider（+ OpenAI）
            └→ [可选] Caching Provider
                └→ [可选] Routing Provider
                    └→ [可选] Classifying Provider（+ Ollama）
```

每一层根据配置有条件地添加。

## 智能模型路由

当配置了Claude API密钥时，Iulita会自动注册Claude Haiku作为廉价提供者。

### 后台任务自动路由

后台任务（上下文压缩、洞察生成、用户分析、书签精炼、心跳检查）通过 `RouteHint` 自动路由到Haiku。

### 技能级合成路由

技能可以通过 `SynthesisModelDeclarer` 接口声明其输出的合成可以使用更便宜的模型。支持廉价合成的技能：`datetime`、`exchange_rate`、`geolocation`、`recall`、`list_insights`、`websearch`。

### 子代理路由

| 代理类型 | 路由 | 原因 |
|---------|------|------|
| summarizer | claude-haiku | 纯摘要 |
| researcher | Sonnet（默认） | 需要推理 |
| analyst | Sonnet（默认） | 模式识别 |

### 模型定价（每百万token）

| 模型 | 输入 | 输出 |
|------|------|------|
| claude-opus-4-6 | $5.00 | $25.00 |
| claude-sonnet-4-6 | $3.00 | $15.00 |
| claude-haiku-4-5 | $1.00 | $5.00 |
