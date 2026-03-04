# 通道

Iulita 支持多种通信通道。每个通道将平台特定的消息转换为统一的 `IncomingMessage` 格式，并通过助手进行路由。

## 通道能力

每个通道通过每条消息上的位掩码声明其能力：

| 能力 | Console | Telegram | WebChat |
|------|---------|----------|---------|
| 流式传输 | 通过 bubbletea | 是（基于编辑） | 是（WebSocket） |
| Markdown | 通过 glamour | 是 | HTML |
| 表情反应 | 否 | 否 | 否 |
| 按钮 | 否 | 是（内联键盘） | 是 |
| 输入指示器 | 是 | 是 | 否 |
| HTML | 否 | 否 | 是 |

能力是按消息（而非按通道）设置的，当多个通道共享同一个助手时允许混合行为。技能可以通过 `channel.CapsFrom(ctx)` 检查能力以调整输出格式。

## 控制台 TUI

默认模式 — 由 [bubbletea](https://github.com/charmbracelet/bubbletea) 驱动的全屏终端聊天。

### 功能特性

- **全屏布局**：视窗（聊天历史）+ 分隔线 + 文本区域（输入）+ 状态栏
- **Markdown 渲染**：通过 [glamour](https://github.com/charmbracelet/glamour) 实现自适应换行
- **流式传输**：实时文本显示，带加载指示器
- **斜杠命令**：`/help`、`/status`、`/compact`、`/clear`、`/quit`
- **交互式提示**：用于技能交互的编号选项（如天气位置选择）
- **背景色检测**：在 bubbletea 启动前自适应渲染

### 架构

```
tuiModel (bubbletea)
    ├── viewport.Model（可滚动的聊天历史）
    ├── textarea.Model（用户输入）
    ├── statusBar（技能名称、令牌数、费用）
    └── streamBuf（实时流式文本）
```

`console.Channel` 结构体持有一个由 `sync.RWMutex` 保护的 `*tea.Program`。bubbletea 程序在自己的协程中运行（阻塞 `Start()`），而 `StartStream`、`SendMessage` 和 `NotifyStatus` 从助手协程并发调用。

### 流式传输桥接

当助手流式传输响应时：

1. `StartStream()` 返回 `editFn` 和 `doneFn` 闭包
2. `editFn(text)` 向 bubbletea 发送 `streamChunkMsg`（累积的完整文本）
3. `doneFn(text)` 向 bubbletea 发送 `streamDoneMsg`（完成并追加到历史）
4. 所有消息通过 bubbletea 的 `p.Send()` 实现线程安全

### 斜杠命令

| 命令 | 描述 |
|------|------|
| `/help` | 显示所有命令及描述 |
| `/status` | 技能数量、每日费用、会话令牌数、消息计数 |
| `/compact` | 手动触发历史压缩（异步） |
| `/clear` | 清除内存中的聊天历史（仅 TUI） |
| `/quit` / `/exit` | 退出应用程序 |

### 服务器模式共存

在控制台模式下，服务器在后台运行：
- 日志重定向到 `iulita.log`（而非 stderr，以避免 TUI 显示损坏）
- 仪表盘仍可通过配置的地址访问
- Telegram 和其他通道与 TUI 并行运行

## Telegram

功能完整的 Telegram 机器人，支持流式传输、消息合并和交互式提示。

### 设置

1. 通过 [@BotFather](https://t.me/BotFather) 创建机器人
2. 设置令牌：`iulita init`（密钥链）或 `IULITA_TELEGRAM_TOKEN` 环境变量
3. 可选：设置 `telegram.allowed_ids` 以白名单限制特定的 Telegram 用户 ID

### 功能特性

- **用户白名单**：`allowed_ids` 限制谁可以与机器人聊天。为空 = 允许所有人（记录警告）
- **消息合并**：同一聊天的快速消息会被合并（可配置时间窗口）
- **流式编辑**：响应通过 `EditMessageText` 逐步显示（限速为每 1.5 秒 1 次编辑）
- **消息分块**：超过 4000 字符的消息在段落/行/词边界处分割，保留代码块
- **回复线程**：第一个分块回复用户消息；后续分块独立发送
- **输入指示器**：处理期间每 4 秒发送 `ChatTyping` 动作
- **健康监控**：每 60 秒调用 `GetMe()` 检测连接问题
- **交互式提示**：用于技能交互的内联键盘（天气位置等）
- **媒体支持**：照片（最大尺寸）、文档（30MB 限制）、语音/音频（含转录）
- **内置命令**：`/clear`（清除历史）、自定义注册命令

### 消息处理管道

```
传入的 Telegram 更新
    │
    ├── 回调查询？→ 路由到提示处理器
    ├── 不是消息？→ 跳过
    ├── 用户不在白名单？→ 拒绝
    ├── /clear 命令？→ 直接处理
    ├── 已注册命令？→ 路由到处理器
    ├── 活跃的提示？→ 将文本路由到提示
    │
    ▼
构造 IncomingMessage
    │ Caps = Streaming | Markdown | Typing | Buttons
    │
    ├── 解析用户（平台 → iulita UUID）
    ├── 从数据库查找区域设置
    ├── 下载媒体（照片/文档/语音）
    ├── 检查速率限制
    │
    ▼
消息合并
    │ 合并快速消息（文本用 \n 连接）
    │ 每条新消息重置计时器
    │
    ▼
处理器（Assistant.HandleMessage）
```

### 消息合并器

消息合并器将同一聊天的快速消息合并以防止多次 LLM 调用：

- 每个 `chatID` 有一个带 `time.AfterFunc` 计时器的缓冲区
- 添加消息会重置计时器
- 计时器触发时，所有缓冲消息被合并：
  - 文本用 `"\n"` 连接
  - 图片和文档拼接
  - 保留第一条消息的元数据
- 如果 `debounce_window = 0`，消息立即处理（非阻塞）
- 关停期间 `flushAll()` 处理剩余缓冲

### 消息分块

长响应被分割为 Telegram 兼容的分块（最大 4000 字符）：

1. 尝试在段落边界（`\n\n`）分割
2. 尝试在行边界（`\n`）分割
3. 尝试在词边界（` `）分割
4. 硬分割作为最后手段
5. **代码块感知**：如果在 ``` 块内分割，用 ``` 关闭并在下一个分块中重新打开

### 配置

| 键 | 默认值 | 描述 |
|------|--------|------|
| `telegram.token` | — | 机器人令牌（支持热重载） |
| `telegram.allowed_ids` | `[]` | 用户 ID 白名单（空 = 允许所有） |
| `telegram.debounce_window` | 2s | 消息合并时间窗口 |

## WebChat

嵌入仪表盘中的基于 WebSocket 的 Web 聊天。

### 协议

**连接**：WebSocket 位于 `/ws/chat?user_id=<uuid>&username=<name>&chat_id=<optional>`

**传入消息**（客户端 → 服务器）：
```json
{
  "text": "user message",
  "chat_id": "web:abc123",
  "prompt_id": "prompt_123_1",       // 仅用于提示响应
  "prompt_answer": "option_id"       // 仅用于提示响应
}
```

**传出消息**（服务器 → 客户端）：

| 类型 | 用途 | 关键字段 |
|------|------|----------|
| `message` | 普通响应 | `text`、`timestamp` |
| `stream_edit` | 流式更新 | `text`、`message_id`、`timestamp` |
| `stream_done` | 流式完成 | `text`、`message_id`、`timestamp` |
| `status` | 处理事件 | `status`、`skill_name`、`success`、`duration_ms` |
| `prompt` | 交互式问题 | `text`、`prompt_id`、`options[]` |

### 认证

WebChat **不使用** UserResolver。前端通过 `/api/auth/login` 获取 JWT 令牌，从载荷中提取 `user_id`，并作为 WebSocket 查询参数传递。通道直接信任此 `user_id`。

### 写入序列化

所有 WebSocket 写入通过每连接的 `sync.Mutex` 以防止并发写入崩溃。每个连接在 `clients[chatID]` 映射中被跟踪。

### 交互式提示

提示使用基于原子计数器的 ID：`prompt_<timestamp>_<counter>`。服务器发送带选项的 `prompt` 消息；客户端以 `prompt_id` 和 `prompt_answer` 响应。待处理的提示存储在带超时的 `sync.Map` 中。

## 通道管理器

`channelmgr.Manager` 在运行时编排所有通道实例。

### 生命周期

- **StartAll**：从数据库加载所有通道实例，每个在单独的协程中启动
- **StopInstance**：取消上下文，等待完成通道（5 秒超时）
- **AddInstance / UpdateInstance**：用于仪表盘创建/修改的实例
- **热重载**：`UpdateConfigToken(token)` 重启配置来源的 Telegram 实例

### 消息路由

当助手需要发送主动消息（提醒、心跳）时：

1. 通过数据库查找哪个通道实例拥有该 `chatID`
2. 如果找到且正在运行，使用该通道的发送器
3. 回退：使用第一个运行中的通道

### 支持的通道类型

| 类型 | 来源 | 热重载 |
|------|------|--------|
| Telegram | 配置或数据库 | 令牌热重载 |
| WebChat | 数据库（引导） | — |
| Console | 仅控制台模式 | — |

## 用户解析

`DBUserResolver` 将平台身份映射到 iulita UUID：

1. 通过 `(channel_type, channel_user_id)` 查找 `user_channels`
2. 如果找到 → 返回现有 `user.ID`
3. 如果未找到且启用自动注册：
   - 创建新的 `User`，随机密码且 `MustChangePass: true`
   - 将通道绑定到用户
   - 返回新 UUID
4. 如果未找到且禁用自动注册 → 拒绝

**每通道区域设置**：解析后，调用 `GetChannelLocale(ctx, channelType, channelUserID)` 从数据库存储的偏好设置 `msg.Locale`。

## 状态事件

通道接收 `StatusEvent` 通知用于用户体验反馈：

| 类型 | 时机 | 用途 |
|------|------|------|
| `processing` | 收到消息，LLM 调用前 | 显示"思考中..." |
| `skill_start` | 技能执行前 | 显示技能名称 |
| `skill_done` | 技能执行后 | 显示耗时、成功/失败 |
| `stream_start` | 流式传输开始前 | 准备流式 UI |
| `error` | 出错时 | 显示错误消息 |
| `locale_changed` | set_language 技能之后 | 更新 UI 区域设置 |
