# 架构

## 高层概述

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

## 核心设计原则

1. **基于事实的记忆** — 只存储经过验证的用户数据，从不存储幻觉知识
2. **控制台优先** — TUI 是默认模式；服务器模式是可选的
3. **整洁架构** — 领域模型 → 接口 → 实现 → 编排器
4. **多通道，单一身份** — 事实和洞察通过 user_id 在所有通道间共享
5. **零配置本地安装** — 只需 API 密钥即可开箱即用
6. **热重载** — 技能、配置甚至 Telegram 令牌都可以在运行时更改而无需重启

## 组件映射

| 组件 | 包 | 描述 |
|------|------|------|
| 入口点 | `cmd/iulita/` | CLI 解析、依赖注入、优雅关停 |
| 助手 | `internal/assistant/` | 编排器：LLM 循环、记忆、压缩、审批、流式传输 |
| 通道 | `internal/channel/` | 输入适配器：控制台 TUI、Telegram、WebChat |
| 通道管理器 | `internal/channelmgr/` | 通道生命周期、路由、热重载 |
| LLM 提供商 | `internal/llm/` | Claude、Ollama、OpenAI、ONNX 嵌入 |
| 技能 | `internal/skill/` | 30 多个工具实现 |
| 技能管理器 | `internal/skillmgr/` | 外部技能：ClawhHub 市场、URL、本地 |
| 书签 | `internal/bookmark/` | 快速保存助手回复为事实 + 后台精炼 |
| 存储 | `internal/storage/sqlite/` | SQLite，支持 FTS5、向量、WAL 模式 |
| 调度器 | `internal/scheduler/` | 支持 cron/间隔的任务队列 |
| 仪表盘 | `internal/dashboard/` | GoFiber REST API + 嵌入式 Vue 3 SPA |
| 配置 | `internal/config/` | 分层配置：默认值 → TOML → 环境变量 → 密钥链 → 数据库 |
| 认证 | `internal/auth/` | JWT + bcrypt，中间件 |
| 国际化 | `internal/i18n/` | 6 种语言，TOML 目录，上下文传播 |
| Web 搜索 | `internal/web/` | Brave + DuckDuckGo 回退，SSRF 防护 |
| 领域 | `internal/domain/` | 纯领域模型 |
| 记忆 | `internal/memory/` | TF-IDF 聚类，记忆导出/导入 |
| 指标 | `internal/metrics/` | Prometheus 计数器和直方图 |
| 智能体 | `internal/agent/` | 子智能体运行器、编排器、预算控制 |
| 事件 | `internal/eventbus/` | 发布/订阅事件总线 |
| 费用 | `internal/cost/` | LLM 费用跟踪，含每日限额 |
| 限流 | `internal/ratelimit/` | 速率限制 |
| 前端 | `ui/` | Vue 3 + Naive UI + UnoCSS SPA |

## 启动顺序

启动序列严格有序以满足依赖关系：

```
1. 解析 CLI 参数，解析 XDG 路径，确保目录存在
2. 处理子命令：init、--version、--doctor（提前退出）
3. 加载配置：默认值 → TOML → 环境变量 → 密钥链
4. 创建日志记录器（控制台模式重定向到文件）
5. 打开 SQLite，运行迁移
6. 初始化 i18n 目录（在迁移之后，技能之前）
7. 引导管理员用户（在回填之前）
8. BackfillUserIDs（将旧数据关联到用户）
9. 创建配置存储，加载数据库覆盖
10. 检查设置模式门控（无 LLM + 未完成向导 = 仅设置模式）
11. 验证配置
12. 创建认证服务
13. 引导通道实例
14. 创建 ONNX 嵌入提供商（可选）
15. 构建 LLM 提供商链（Claude → 重试 → 回退 → 缓存 → 路由）
16. 注册所有技能（无条件注册 — 通过能力门控）
17. 创建助手
18. 连接事件总线（配置重载、指标、费用、通知）
19. 重放数据库配置覆盖（用于仪表盘设置的凭证热重载）
20. 创建通道管理器、调度器、工作器
21. 启动调度器、工作器、助手运行循环
22. 启动仪表盘服务器
23. 启动所有通道
24. 阻塞等待关停信号
```

## 优雅关停（7 个阶段）

```
1. 停止所有通道（停止接受新消息）
2. 等待助手后台协程
3. 等待嵌入回填
4. 关闭 ONNX 提供商
5. 关闭事件总线（等待异步处理器）
6. 等待调度器/工作器/仪表盘（10 秒超时）
7. 关闭 SQLite 连接（最后执行）
```

## 消息流程

当用户发送消息时，完整的执行路径如下：

```
用户输入 "remember that I love Go"
    │
    ▼
通道（Telegram/WebChat/Console）
    │ 构造 IncomingMessage，附带平台特定字段
    │ 设置 ChannelCaps 位掩码（流式、markdown 等）
    ▼
UserResolver（仅 Telegram/Console）
    │ 将平台身份映射到 iulita UUID
    │ 如允许则自动注册新用户
    ▼
通道管理器
    │ 路由到 Assistant.HandleMessage
    ▼
助手 — 阶段 1：上下文设置
    │ 超时、用户角色、区域设置、能力 → 上下文
    │ 检查待审批 → 如已批准则执行
    ▼
助手 — 阶段 2：丰富
    │ 将消息保存到数据库
    │ 后台：TechFactAnalyzer（Cyrillic/Latin、消息长度）
    │ 发送 "processing" 状态事件
    ▼
助手 — 阶段 3：历史与压缩
    │ 加载最近 50 条消息
    │ 如果令牌数 > 上下文窗口的 80% → 压缩较旧的一半
    ▼
助手 — 阶段 4：上下文数据
    │ 加载指令、最近事实、相关洞察
    │ 混合搜索：FTS5 + ONNX 向量 + MMR 重排序
    │ 加载技术事实（用户画像）
    │ 解析时区
    ▼
助手 — 阶段 5：提示词构建
    │ 静态提示词 = 基础 + 技能系统提示词（由 Claude 缓存）
    │ 动态提示词 = 时间 + 指令 + 画像 + 事实 + 洞察 + 语言
    ▼
助手 — 阶段 6：强制工具检测
    │ "remember" 关键词 → ForceTool = "remember"
    ▼
助手 — 阶段 7：代理循环（最多 10 次迭代）
    │ 调用 LLM（无工具时流式传输，否则标准调用）
    │ 上下文溢出 → 强制压缩 → 重试一次
    │ 如果有工具调用：
    │   ├── 检查审批级别
    │   ├── 执行技能
    │   ├── 累积到 ToolExchanges
    │   └── 下一次迭代
    │ 如果没有工具调用 → 返回响应
    ▼
技能执行（例如 RememberSkill）
    │ 通过 FTS 搜索进行重复检查
    │ 保存到 SQLite → FTS 触发器触发
    │ 后台：ONNX 嵌入 → fact_vectors
    ▼
响应通过通道发送回用户
```

## 关键接口

### Provider（LLM）

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
    InputSchema() json.RawMessage  // nil 表示纯文本技能
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

可选接口：`CapabilityAware`、`ConfigReloadable`、`ApprovalDeclarer`。

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

// 可选 — 通道实现此接口以在流式回复中添加书签按钮
type BookmarkStreamingSender interface {
    StreamingSender
    StartStreamWithBookmark(ctx context.Context, chatID string, replyTo int, userID string) (editFn, doneFn func(string), err error)
}
```

### Storage

```go
type Repository interface {
    // 消息
    SaveMessage(ctx, msg) error
    GetHistory(ctx, chatID, limit) ([]ChatMessage, error)

    // 记忆
    SaveFact(ctx, fact) error
    SearchFacts(ctx, chatID, query, limit) ([]Fact, error)
    SearchFactsByUser(ctx, userID, query, limit) ([]Fact, error)
    SearchFactsHybridByUser(ctx, userID, query, queryVec, limit) ([]Fact, error)

    // 任务
    CreateTask(ctx, task) error
    ClaimTask(ctx, workerID, capabilities) (*Task, error)

    // ... 共 60 多个方法
}
```

## 事件总线

事件总线（`internal/eventbus/`）实现了类型化的发布/订阅模式。事件在组件之间流转而无需直接耦合：

| 事件 | 发布者 | 订阅者 |
|------|--------|--------|
| `MessageReceived` | 助手 | 指标、WebSocket hub |
| `ResponseSent` | 助手 | 指标、WebSocket hub |
| `LLMUsage` | 助手 | 指标、费用跟踪器 |
| `SkillExecuted` | 助手 | 指标 |
| `TaskCompleted` | 工作器 | WebSocket hub |
| `TaskFailed` | 工作器 | WebSocket hub |
| `FactSaved` | 存储 | WebSocket hub |
| `InsightCreated` | 存储 | WebSocket hub |
| `ConfigChanged` | 配置存储 | 配置重载处理器 → 技能 |
| `AgentOrchestrationStarted` | 编排器 | 指标、WebSocket hub |
| `AgentOrchestrationDone` | 编排器 | 指标、WebSocket hub |

## LLM 提供商链

提供商以装饰器模式组合：

```
Claude Provider
    └→ Retry Provider（3 次尝试，指数退避，429/5xx）
        └→ Fallback Provider（Claude → OpenAI）
            └→ Caching Provider（SHA-256 键，60 分钟 TTL）
                └→ Routing Provider（基于 RouteHint 的分发）
                    └→ Classifying Provider（Ollama 分类器 → 路由选择）
```

对于不支持原生工具调用的提供商（Ollama、OpenAI），`XMLToolProvider` 包装器将工具定义作为 XML 注入系统提示词，并从响应中解析 XML 工具调用。

## 数据范围

所有数据通过 `user_id` 进行范围限定以实现跨通道共享：

```
User（iulita UUID）
    ├── user_channels（Telegram 绑定、WebChat 绑定、...）
    ├── chat_messages（来自所有通道）
    ├── facts（跨通道共享）
    ├── insights（跨通道共享）
    ├── directives（按用户）
    ├── tech_facts（行为画像）
    ├── reminders
    └── todo_items
```

用户在 Telegram 上聊天时可以回忆通过控制台 TUI 存储的事实，因为两个通道解析到相同的 `user_id`。

## 项目结构

```
cmd/iulita/              # 入口点、依赖注入、优雅关停
internal/
  assistant/             # 编排器（LLM 循环、记忆、压缩、审批）
  channel/
    console/             # bubbletea TUI
    telegram/            # Telegram 机器人
    webchat/             # WebSocket Web 聊天
  bookmark/              # 快速保存助手回复为事实
  channelmgr/            # 通道生命周期管理器
  config/                # TOML + 环境变量 + 密钥链配置，设置向导
  domain/                # 领域模型
  auth/                  # JWT 认证 + bcrypt
  i18n/                  # 国际化（6 种语言，TOML 目录）
  llm/                   # LLM 提供商（Claude、Ollama、OpenAI、ONNX）
  scheduler/             # 任务队列（调度器 + 工作器）
  agent/                 # 多智能体编排（运行器、编排器、预算）
  skill/                 # 技能实现
  skillmgr/              # 外部技能管理器（ClawhHub、URL、本地）
  storage/sqlite/        # SQLite 仓库、FTS5、向量、迁移
  dashboard/             # GoFiber REST API + Vue SPA
  web/                   # Web 搜索（Brave、DuckDuckGo、SSRF 防护）
  transcription/         # 音频/语音转录
  doctor/                # 诊断检查（--doctor 标志）
  memory/                # TF-IDF 聚类、导出/导入
  eventbus/              # 发布/订阅事件总线
  cost/                  # LLM 费用跟踪
  metrics/               # Prometheus 指标
  ratelimit/             # 速率限制
  notify/                # 推送通知
ui/                      # Vue 3 + Naive UI + UnoCSS 前端
skills/                  # 文本技能文件（Markdown）
docs/                    # 文档
```
