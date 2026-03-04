# 仪表盘

仪表盘是一个 GoFiber REST API，服务于嵌入式 Vue 3 SPA。它提供了管理 Iulita 所有方面的 Web 界面。

## 架构

```
GoFiber Server
    ├── /api/*          REST API（JWT 认证）
    ├── /ws             WebSocket hub（实时更新）
    ├── /ws/chat        WebChat 通道（独立端点）
    └── /*              Vue 3 SPA（嵌入式，客户端路由）
```

Vue SPA 通过 `//go:embed dist/*` 嵌入 Go 二进制文件中，并为所有未知路径提供 `index.html` 回退。

## 认证

| 端点 | 认证 | 描述 |
|------|------|------|
| `POST /api/auth/login` | 公开 | bcrypt 凭证验证，返回访问令牌 + 刷新令牌 |
| `POST /api/auth/refresh` | 公开 | 验证刷新令牌，返回新的访问令牌 |
| `POST /api/auth/change-password` | JWT | 修改自己的密码 |
| `GET /api/auth/me` | JWT | 当前用户配置文件 |
| `PATCH /api/auth/locale` | JWT | 更新所有通道的区域设置 |

**JWT 详情：**
- 算法：HMAC-SHA256
- 访问令牌 TTL：24 小时
- 刷新令牌 TTL：7 天
- Claims：`user_id`、`username`、`role`
- 密钥：未配置时自动生成

## REST API

### 公开端点

| 方法 | 路径 | 描述 |
|------|------|------|
| GET | `/api/system` | 系统信息、版本、运行时间、向导状态 |

### 用户端点（需要 JWT）

| 方法 | 路径 | 描述 |
|------|------|------|
| GET | `/api/stats` | 消息、事实、洞察、提醒计数 |
| GET | `/api/chats` | 列出所有聊天 ID 及消息计数 |
| GET | `/api/facts` | 列出/搜索事实（按 chat_id、user_id、query） |
| PUT | `/api/facts/:id` | 更新事实内容 |
| DELETE | `/api/facts/:id` | 删除事实 |
| GET | `/api/facts/search` | FTS 事实搜索 |
| GET | `/api/insights` | 列出洞察 |
| GET | `/api/reminders` | 列出提醒 |
| GET | `/api/directives` | 获取聊天的指令 |
| GET | `/api/messages` | 带分页的聊天历史 |
| GET | `/api/skills` | 列出所有技能及启用/配置状态 |
| PUT | `/api/skills/:name/toggle` | 运行时启用/禁用技能 |
| GET | `/api/skills/:name/config` | 技能配置模式 + 当前值 |
| PUT | `/api/skills/:name/config/:key` | 设置技能配置键（自动加密密钥） |
| GET | `/api/techfacts` | 按类别分组的行为画像 |
| GET | `/api/usage/summary` | 令牌使用量 + 费用估算 |
| GET | `/api/schedulers` | 调度器任务状态 |
| POST | `/api/schedulers/:name/trigger` | 手动任务触发 |

### 任务端点（需要 JWT）

| 方法 | 路径 | 描述 |
|------|------|------|
| GET | `/api/todos/providers` | 列出任务提供商 |
| GET | `/api/todos/today` | 今天的任务 |
| GET | `/api/todos/overdue` | 逾期任务 |
| GET | `/api/todos/upcoming` | 即将到来的任务（默认 7 天） |
| GET | `/api/todos/all` | 所有未完成的任务 |
| GET | `/api/todos/counts` | 今天 + 逾期计数 |
| POST | `/api/todos/` | 创建任务 |
| POST | `/api/todos/sync` | 触发手动待办同步 |
| POST | `/api/todos/:id/complete` | 完成任务 |
| DELETE | `/api/todos/:id` | 删除内置任务 |

### Google Workspace 端点（需要 JWT）

| 方法 | 路径 | 描述 |
|------|------|------|
| GET | `/api/google/status` | 账户状态 |
| POST | `/api/google/upload-credentials` | 上传 OAuth 凭证文件 |
| GET | `/api/google/auth` | 发起 OAuth2 流程 |
| GET | `/api/google/callback` | OAuth2 回调 |
| GET | `/api/google/accounts` | 列出账户 |
| DELETE | `/api/google/accounts/:id` | 删除账户 |
| PUT | `/api/google/accounts/:id` | 更新账户 |

### 管理员端点（需要管理员角色）

| 方法 | 路径 | 描述 |
|------|------|------|
| GET/PUT/DELETE | `/api/config/*` | 配置覆盖、模式、调试 |
| GET/POST/PUT/DELETE | `/api/users/*` | 用户 CRUD + 通道绑定 |
| GET/POST/PUT/DELETE | `/api/channels/*` | 通道实例 CRUD |
| GET/POST/PUT/DELETE | `/api/agent-jobs/*` | 代理任务 CRUD |
| GET/POST/DELETE | `/api/skills/external/*` | 外部技能管理 |
| GET/POST | `/api/wizard/*` | 设置向导 |
| PUT | `/api/todos/default-provider` | 设置默认任务提供商 |

### 工作器端点（Bearer 令牌）

| 方法 | 路径 | 描述 |
|------|------|------|
| GET | `/api/tasks/` | 列出调度器任务 |
| GET | `/api/tasks/counts` | 按状态计数 |
| POST | `/api/tasks/claim` | 认领任务（远程工作器） |
| POST | `/api/tasks/:id/start` | 标记任务为运行中 |
| POST | `/api/tasks/:id/complete` | 完成任务 |
| POST | `/api/tasks/:id/fail` | 任务失败 |

## WebSocket Hub

位于 `/ws` 的 WebSocket hub 为已连接的仪表盘客户端提供实时更新。

### 事件

| 事件 | 来源 | 载荷 |
|------|------|------|
| `task.completed` | 工作器 | 任务详情 |
| `task.failed` | 工作器 | 任务 + 错误 |
| `message.received` | 助手 | 消息元数据 |
| `response.sent` | 助手 | 响应元数据 |
| `fact.saved` | 存储 | 事实详情 |
| `insight.created` | 存储 | 洞察详情 |
| `config.changed` | 配置存储 | 键 + 值 |

事件通过事件总线使用 `SubscribeAsync`（非阻塞）发布。

### 协议

```json
// Server → Client
{"type": "task.completed", "payload": {...}}
{"type": "fact.saved", "payload": {...}}
```

## Vue 3 SPA

### 技术栈

- **Vue 3** — Composition API
- **Naive UI** — 组件库
- **UnoCSS** — 原子化 CSS
- **vue-i18n** — 国际化（6 种语言）
- **vue-router** — 客户端路由

### 视图

| 路径 | 组件 | 认证 | 描述 |
|------|------|------|------|
| `/` | Dashboard | JWT | 统计概览、调度器状态 |
| `/facts` | Facts | JWT | 事实浏览器，支持搜索、编辑、删除 |
| `/insights` | Insights | JWT | 洞察列表 |
| `/reminders` | Reminders | JWT | 提醒列表 |
| `/profile` | TechFacts | JWT | 行为画像元数据 |
| `/settings` | Settings | JWT | 技能管理、配置编辑器 |
| `/tasks` | Tasks | JWT | 今天/逾期/即将到来/全部标签 |
| `/chat` | Chat | JWT | WebSocket Web 聊天 |
| `/users` | Users | 管理员 | 用户 CRUD + 通道绑定 |
| `/channels` | Channels | 管理员 | 通道实例 CRUD |
| `/agent-jobs` | AgentJobs | 管理员 | 代理任务 CRUD |
| `/skills` | ExternalSkills | 管理员 | 市场 + 已安装技能 |
| `/setup` | Setup | 管理员 | Web 设置向导 |
| `/config-debug` | ConfigDebug | 管理员 | 原始配置覆盖查看器 |
| `/login` | Login | 公开 | 登录表单 |

### 路由守卫

```javascript
router.beforeEach((to, from, next) => {
    if (to.meta.public) { next(); return }
    if (!isLoggedIn()) { next({ name: 'login' }); return }
    if (to.meta.admin && !isAdmin()) { next({ name: 'dashboard' }); return }
    next()
})
```

### 关键组合式函数

- `useWebSocket` — 带类型化事件的自动重连 WebSocket
- `useLocale` — 响应式区域设置状态、RTL 检测、后端同步
- `useSkillStatus` — 根据技能可用性控制侧边栏项目

### 技能管理 UI

Settings 视图提供：

1. **技能开关** — 运行时启用/禁用每个技能
2. **配置编辑器** — 每技能配置，包含：
   - 模式驱动的表单字段
   - 密钥保护（API 中从不泄露值）
   - 敏感值自动加密
   - 保存时热重载

### 任务仪表盘

Tasks 视图聚合来自所有提供商的任务：

- **今天标签** — 今天到期的任务
- **逾期标签** — 已过期的任务
- **即将到来标签** — 未来 7 天
- **全部标签** — 所有未完成的任务
- **同步按钮** — 触发一次性调度器任务
- **创建按钮** — 新建任务，可选择提供商

## Prometheus 指标

启用后（`metrics.enabled = true`），指标在独立端口暴露：

| 指标 | 类型 | 标签 |
|------|------|------|
| `iulita_llm_requests_total` | Counter | provider、model、status |
| `iulita_llm_tokens_input_total` | Counter | provider |
| `iulita_llm_tokens_output_total` | Counter | provider |
| `iulita_llm_request_duration_seconds` | Histogram | provider |
| `iulita_llm_cost_usd_total` | Counter | — |
| `iulita_skill_executions_total` | Counter | skill、status |
| `iulita_task_total` | Counter | type、status |
| `iulita_messages_total` | Counter | direction |
| `iulita_cache_hits_total` | Counter | cache_type |
| `iulita_cache_misses_total` | Counter | cache_type |
| `iulita_active_sessions` | Gauge | — |

指标通过订阅事件总线（非阻塞）填充。
