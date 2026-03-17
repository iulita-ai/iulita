# 技能

技能是助手在对话中可以调用的工具。每个技能向 LLM 暴露一个或多个工具，包含名称、描述和 JSON 输入模式。

## 技能接口

```go
type Skill interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage  // nil 表示纯文本技能
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

**可选接口：**
- `CapabilityAware` — `RequiredCapabilities() []string`：如果任何能力缺失则排除该技能
- `ConfigReloadable` — `OnConfigChanged(key, value string)`：运行时配置更改时调用
- `ApprovalDeclarer` — `ApprovalLevel() ApprovalLevel`：审批要求

## 审批级别

| 级别 | 行为 | 使用场景 |
|------|------|----------|
| `ApprovalAuto` | 立即执行（默认） | 大多数技能 |
| `ApprovalPrompt` | 用户必须在聊天中确认 | Docker 执行器 |
| `ApprovalManual` | 管理员必须确认 | Shell 执行 |

审批流程是**非阻塞**的：技能向 LLM 返回"等待审批"。下一条用户消息会与区域感知的审批词汇（6 种语言的是/否）进行匹配检查。

## 内置技能

### 记忆组

| 工具 | 输入 | 描述 |
|------|------|------|
| `remember` | `content` | 存储事实。通过 FTS 检查重复。触发自动嵌入。 |
| `recall` | `query`、`limit` | 通过 FTS5 搜索事实。应用时间衰减 + MMR 重排序。强化已访问的事实。 |
| `forget` | `id` | 按 ID 删除事实。级联到 FTS 和向量表。 |

详情请参阅[记忆与洞察](memory-and-insights.md)。

### 洞察组

| 工具 | 输入 | 描述 |
|------|------|------|
| `list_insights` | `limit` | 列出最近的洞察及质量评分 |
| `dismiss_insight` | `id` | 删除洞察 |
| `promote_insight` | `id` | 延长或移除洞察过期时间 |

### Web 搜索与获取

| 工具 | 输入 | 描述 |
|------|------|------|
| `websearch` | `query`、`count` | 通过 Brave API + DuckDuckGo 回退进行 Web 搜索。1-10 个结果。 |
| `webfetch` | `url` | 获取并摘要 Web 页面。使用 go-readability 提取内容。SSRF 防护。 |

Web 搜索链为 `Brave → DuckDuckGo`，通过 `FallbackSearcher` 实现。DuckDuckGo 不需要 API 密钥，因此 Web 搜索始终可用。

### 指令

| 工具 | 输入 | 描述 |
|------|------|------|
| `directives` | `action`、`content` | 管理持久化的自定义指令（设置/获取/清除）。加载到系统提示词中。 |

### 提醒

| 工具 | 输入 | 描述 |
|------|------|------|
| `reminders` | `action`、`title`、`due_at`、`timezone`、`id` | 创建/列出/删除基于时间的提醒。由调度器投递。 |

### 日期/时间

| 工具 | 输入 | 描述 |
|------|------|------|
| `datetime` | `timezone` | 当前日期、时间、时区名称、Unix 时间戳。零外部依赖。 |

### 天气

| 工具 | 输入 | 描述 |
|------|------|------|
| `weather` | `location`、`days` | 天气预报（1-16 天）。交互式位置解析。 |

**后端链**：Open-Meteo（主要，免费）→ wttr.in（回退，免费）→ OpenWeatherMap（可选，需要密钥）。

功能特性：
- 通过通道提示的交互式位置解析（Telegram 内联键盘、WebChat 按钮、Console 编号选项）
- 西里尔字母地理编码支持
- 多日预报，含 WMO 天气代码描述（6 种语言翻译）
- 输出根据通道能力自适应（markdown vs 纯文本）

### 地理定位

| 工具 | 输入 | 描述 |
|------|------|------|
| `geolocation` | `ip` | 基于 IP 的地理定位。自动检测公共 IP。 |

提供商链：ipinfo.io（带密钥）→ ip-api.com → ipapi.co。验证 IP 为公共地址（阻止 RFC1918、回环等）。

### 汇率

| 工具 | 输入 | 描述 |
|------|------|------|
| `exchange_rate` | `from`、`to`、`amount` | 货币汇率。160 多种货币。不需要 API 密钥。 |

### Shell 执行

| 工具 | 输入 | 描述 |
|------|------|------|
| `shell_exec` | `command`、`args` | 沙箱化的 Shell 命令执行。**ApprovalManual**（需要管理员确认）。 |

**安全措施：**
- 仅白名单：只有 `AllowedBins` 中的程序可以执行
- 参数中检查 `ForbiddenPaths`
- 拒绝 `..` 路径遍历
- 最大 16KB 输出
- 默认执行目录：`os.TempDir()`

### 委派

| 工具 | 输入 | 描述 |
|------|------|------|
| `delegate` | `prompt`、`provider` | 将子提示路由到次要 LLM 提供商（如 Ollama 用于廉价任务）。 |

### PDF 阅读器

| 工具 | 输入 | 描述 |
|------|------|------|
| `pdf_read` | `url` | 获取并读取 PDF 文档。验证 `%PDF-` 魔术字节。 |

### 设置语言

| 工具 | 输入 | 描述 |
|------|------|------|
| `set_language` | `language` | 切换界面语言。接受 BCP-47 代码或语言名称（English/Russian）。 |

更新数据库中的 `user_channels.locale`。确认消息使用**新**语言。

### Google Workspace

| 工具 | 描述 |
|------|------|
| `google_auth` | OAuth2 流程发起、账户列表 |
| `google_calendar` | 列出/创建/更新/删除事件，空闲/忙碌 |
| `google_contacts` | 列出联系人、生日查询 |
| `google_mail` | 列出/读取/搜索 Gmail（只读） |
| `google_tasks` | Google Tasks 的 CRUD 操作 |

需要通过仪表盘设置 OAuth2。支持多账户。

### Todoist

| 工具 | 输入 | 描述 |
|------|------|------|
| `todoist` | `action`、... | 完整的 Todoist 任务管理。34 个操作。 |

**操作**：创建、列出、获取、更新、完成、重新打开、删除、移动、快速添加、筛选、完成历史。支持优先级（P1-P4）、截止日期、到期日期/时间、循环、标签、项目、分区、子任务、评论。

使用 Unified API v1（`api.todoist.com/api/v1`）。API 令牌认证。

### 统一任务

| 工具 | 输入 | 描述 |
|------|------|------|
| `tasks` | `action`、`provider`、... | 聚合 Todoist + Google Tasks + Craft Tasks。 |

**操作**：`overview`（所有提供商）、`list`、`create`、`complete`、`provider`（直通）。

### Craft

| 工具 | 描述 |
|------|------|
| `craft_read` | 读取 Craft 文档 |
| `craft_write` | 写入 Craft 文档 |
| `craft_tasks` | 管理 Craft 任务 |
| `craft_search` | 搜索 Craft 文档 |

### 多智能体编排

| 工具 | 输入 | 描述 |
|------|------|------|
| `orchestrate` | `agents[]`、`timeout`、`max_tokens` | 并行启动多个专业化子智能体。 |

**智能体类型**：`researcher`、`analyst`、`planner`、`coder`、`summarizer`、`generic` — 每种都有专业化的系统提示词和工具子集。

子智能体通过 `errgroup` 并行执行，共享原子令牌预算，不能生成更多子智能体（最大深度 = 1）。需要审批的技能（ApprovalManual、ApprovalPrompt）从子智能体工具列表中过滤。

详见[多智能体编排](multi-agent.md)。

### 技能管理

| 工具 | 输入 | 描述 |
|------|------|------|
| `skills` | `action`、... | 通过聊天列出/启用/禁用/获取配置/设置配置技能。变更操作仅限管理员。 |

## 文本技能（系统提示词注入）

技能可以纯粹是系统提示词注入 — 没有 `Execute` 方法，没有给 LLM 的工具定义。

### SKILL.md 格式

```yaml
---
name: my-skill
description: What this skill does
capabilities: [optional-cap]
config_keys: [skills.my-skill.setting]
secret_keys: [skills.my-skill.api_key]
force_triggers: [keyword1, keyword2]
---

注入到系统提示词中的 Markdown 指令。
```

- `InputSchema() == nil` 的技能仅贡献于 `staticSystemPrompt()`
- Markdown 正文成为 LLM 指令的一部分
- `force_triggers` 当关键词匹配用户消息时强制特定工具调用

### 加载路径

1. **嵌入在 Go 包中**：`//go:embed SKILL.md` + `LoadManifestFromFS()`
2. **外部目录**：从 `~/.local/share/iulita/skills/` 加载 `LoadExternalManifests(dir)`
3. **已安装的外部技能**：通过 ClawhHub 市场或 URL

## 技能管理器（外部技能）

### ClawhHub 市场

从 [ClawhHub](https://clawhub.ai) 安装社区技能：

```
# 通过仪表盘：技能 → 外部 → 搜索
# 通过聊天：从 URL 安装
```

市场 API（`clawhub.ai/api/v1`）支持：
- `Search(query)` — BM25 相关性排名结果
- `Resolve(slug)` — 获取下载 URL 和校验和
- `Download()` — 获取归档文件（最大 50MB）

### 安装流程

1. 检查 `MaxInstalled` 限制
2. 从来源解析（ClawhHub、URL 或本地目录）
3. 验证 slug 格式 `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`
4. 下载并验证 SHA-256 校验和
5. 解析带扩展 frontmatter 的 `SKILL.md`
6. 根据配置验证隔离级别
7. 扫描提示词注入模式
8. 原子移动到安装目录
9. 在技能注册表中注册

### 隔离级别

| 级别 | 行为 | 审批 |
|------|------|------|
| `text_only` | 仅系统提示词注入 | 自动 |
| `shell` | 通过 `ShellExecutor` 执行 Shell | 手动 |
| `docker` | Docker 容器执行 | 提示 |
| `wasm` | WebAssembly 运行时 | 自动 |

**回退链**：如果技能需要 shell 但 shell_exec 被禁用，回退到 `webfetchProxySkill`（从提示中提取 URL 并获取），然后回退到 `text_only`。

### 安全性

- Slug 验证防止路径遍历
- 远程下载的校验和验证
- 根据配置验证隔离级别（`AllowShell`、`AllowDocker`、`AllowWASM`）
- 代码文件检测：拒绝包含 `.py`/`.js`/`.go` 等文件的技能，除非正确隔离
- 提示词注入扫描：对技能正文中的可疑模式发出警告

## 技能热重载

技能支持运行时配置更改而无需重启：

1. 技能在启动时调用 `RegisterKey()` 声明其配置键
2. 仪表盘配置编辑器调用 `Store.Set()` 发布 `ConfigChanged`
3. 事件总线分发到 `registry.DispatchConfigChanged()`
4. 实现 `ConfigReloadable` 的技能接收新值

**关键规则**：技能必须无条件注册（不能放在 `if apiKey != ""` 内）。使用能力门控代替：API 密钥存在时 `AddCapability("web")`，移除时 `RemoveCapability("web")`。

## 强制触发

技能可以声明强制工具调用的关键词：

```yaml
force_triggers: [weather, погода, météo]
```

当用户消息包含触发关键词（不区分大小写的子串匹配）时，在第 0 次迭代的 LLM 请求上设置 `ForceTool`。这确保 LLM 总是调用工具而不是从训练数据回答。

记忆触发（如 "remember"、"запомни"）单独配置并强制调用 `remember` 工具。
