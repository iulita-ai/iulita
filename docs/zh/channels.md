# 通道

Iulita 支持多种通信通道。每个通道将平台特定的消息转换为统一的 `IncomingMessage` 格式，并通过助手进行路由。

## 通道能力

每个通道通过每条消息上的位掩码声明其能力：

| 能力 | Console | Telegram | WebChat |
|------|---------|----------|---------|
| 流式传输 | 通过 bubbletea | 缓冲后发送最终回复 | 是（WebSocket） |
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

[Telegram 完整指南（英文）](../en/telegram.md)介绍尚未发布的源代码功能：私聊话题、日程与天气卡片、附加用户名、引用和个人回调回复。

配置令牌及 `telegram.allowed_ids`，并在 BotFather 中启用话题。根聊天中的普通消息创建新话题；`/topic` 列出已知话题。`/clear` 与 `/compact` 仅处理当前上下文。`/topic pause` 暂停 Iulita，不关闭 Telegram 话题；`/topic delete` 经确认清理关联本地工作，不删除外部任务。

使用 `/day`、`/day text`、`/weather London 4` 或 `/weather text London 4`。`telegram.day_widgets` 与 `telegram.day_actions` 默认启用，旧配置缺少这些键时升级后也启用；显式 false 保留。修改后需要重启，操作需要所有者确认。

回复缓冲至最终隐私检查，不发布未经检查的流式片段。敏感内容私密发送失败时，不会退回普通消息发送。消息合并隔离用户和话题。生产环境仅支持私聊组件；inline/shared 需要 Telegram 测试服务器。语音、Rich 引用、配置、可用性与恢复详见英文指南。

## WebChat

嵌入仪表盘中的基于 WebSocket 的 Web 聊天。

### 协议

**连接**：WebSocket 位于 `/ws/chat?token=<access-token>`

**传入消息**（客户端 → 服务器）：
```json
{
  "text": "user message",
  "chat_id": "web:abc123",
  "prompt_id": "prompt_123_1",       // 仅用于提示响应
  "prompt_answer": "option_id",      // 仅用于提示响应
  "remember_message_id": "nano_ts"   // 仅用于书签请求
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
| `remember_ack` | 书签确认 | `remember_ack.message_id`、`remember_ack.fact_id`、`remember_ack.status` |

### 书签协议

书签功能允许用户通过 UI 按钮将助手回复保存为事实。

**流程：**
1. 服务器发送 `message` 或 `stream_done`，附带 `message_id`（Unix 纳秒时间戳）
2. 服务器以 `(message_id, chatID)` 为键缓存内容 10 分钟
3. 前端在助手消息悬停时显示 💾 图标
4. 用户点击 → 发送 `{"remember_message_id": "<message_id>"}`
5. 服务器验证所有权（`chatID` 必须与缓存条目匹配），保存为 `source_type="bookmark"` 的事实，入队后台 LLM 精炼
6. 服务器发送 `{"type": "remember_ack", "remember_ack": {"message_id": "...", "status": "saved", "fact_id": 42}}`
7. 前端将图标更新为 ✅

**status 值**：`saved`、`error`、`expired`（消息不再在缓存中）

### 认证

服务器在升级连接前验证访问令牌和当前账户，身份和 `web:<user_id>` 由服务器确定，不能信任客户端传入的身份。提供的 Origin 必须匹配主机和协议。令牌过期或连接被替换时，会取消待处理工作。WebChat 每位用户只有一个聊天槽；多会话 API 使用独立 OAuth。

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
| `orchestration_started` | 启动子智能体之前 | 显示智能体数量 |
| `agent_started` | 每个智能体运行前 | 显示智能体名称 + 类型 |
| `agent_progress` | 每个智能体每轮 LLM 后 | 更新轮次计数器 |
| `agent_completed` | 每个智能体成功时 | 显示耗时 + 令牌数 |
| `agent_failed` | 每个智能体失败时 | 显示错误消息 |
| `orchestration_done` | 所有智能体完成后 | 显示汇总统计 |
