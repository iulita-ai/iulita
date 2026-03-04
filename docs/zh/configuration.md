# 配置

Iulita 使用分层配置系统，支持零配置本地安装，同时允许高级部署进行完全自定义。

## 配置层

配置按顺序加载，后面的层覆盖前面的：

```
1. 编译默认值（始终存在）
2. TOML 文件（~/.config/iulita/config.toml，可选）
3. 环境变量（IULITA_* 前缀）
4. 密钥链密钥（macOS Keychain、Linux SecretService）
5. 数据库覆盖（config_overrides 表，运行时可编辑）
```

### 第 1 层：编译默认值

`DefaultConfig()` 提供无需外部文件即可工作的配置。所有模型 ID、超时、记忆设置和功能标志都有合理的默认值。系统只需 API 密钥即可开箱即用。

### 第 2 层：TOML 文件

可选。位于 `~/.config/iulita/config.toml`（或 `$IULITA_HOME/config.toml`）。

TOML 文件在以下情况下**跳过**：
- 配置路径下不存在文件
- 存在 `db_managed` 哨兵文件（Web 向导模式）

完整参考请参阅 `config.toml.example`。

### 第 3 层：环境变量

所有设置都可以通过 `IULITA_*` 环境变量覆盖：

```
IULITA_CLAUDE_API_KEY      → claude.api_key
IULITA_TELEGRAM_TOKEN      → telegram.token
IULITA_CLAUDE_MODEL        → claude.model
IULITA_STORAGE_PATH        → storage.path
IULITA_SERVER_ADDRESS      → server.address
IULITA_PROXY_URL           → proxy.url
```

**映射规则**：去除 `IULITA_` 前缀，小写化，将 `_` 替换为 `.`。

### 第 4 层：密钥链密钥

密钥安全存储在操作系统密钥链中：

| 密钥 | 环境变量 | 密钥链账户 |
|------|----------|------------|
| Claude API 密钥 | `IULITA_CLAUDE_API_KEY` | `claude-api-key` |
| Telegram 令牌 | `IULITA_TELEGRAM_TOKEN` | `telegram-token` |
| JWT 密钥 | `IULITA_JWT_SECRET` | `jwt-secret` |
| 配置加密密钥 | `IULITA_CONFIG_KEY` | `config-encryption-key` |

**每个密钥的回退链**：环境变量 → 密钥链 → 文件（仅加密密钥） → 自动生成（仅 JWT）。

密钥链使用 `zalando/go-keyring`：
- **macOS**：Keychain
- **Linux**：SecretService（GNOME Keyring、KDE Wallet）
- **回退**：`~/.config/iulita/encryption.key` 加密文件

### 第 5 层：数据库覆盖（配置存储）

存储在 `config_overrides` SQLite 表中的运行时可编辑配置。管理方式：
- 仪表盘配置编辑器
- 聊天中的 `skills` 工具（`set_config` 操作）
- Web 设置向导

**功能特性：**
- AES-256-GCM 加密敏感值
- 通过事件总线即时热重载
- 审计日志（谁改了什么，何时改的）
- 仅重启键受保护不可修改

## 符合 XDG 规范的路径

| 平台 | 配置 | 数据 | 缓存 | 状态 |
|------|------|------|------|------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

**覆盖**：设置 `IULITA_HOME` 使用自定义根目录，含 `data/`、`cache/`、`state/` 子目录。

### 派生路径

| 路径 | 位置 |
|------|------|
| 配置文件 | `{ConfigDir}/config.toml` |
| 数据库 | `{DataDir}/iulita.db` |
| ONNX 模型 | `{DataDir}/models/` |
| 技能 | `{DataDir}/skills/` |
| 外部技能 | `{DataDir}/external-skills/` |
| 日志文件 | `{StateDir}/iulita.log` |
| 加密密钥 | `{ConfigDir}/encryption.key` |

## 配置章节

### App

| 键 | 默认值 | 描述 |
|------|--------|------|
| `app.system_prompt` | （内置） | 助手的基础系统提示词 |
| `app.context_window` | 200000 | 上下文令牌预算 |
| `app.request_timeout` | 120s | 每条消息的超时时间 |

### Claude（主要 LLM）

| 键 | 默认值 | 描述 |
|------|--------|------|
| `claude.api_key` | — | Anthropic API 密钥（必需） |
| `claude.model` | `claude-sonnet-4-5-20250929` | 模型 ID |
| `claude.max_tokens` | 8192 | 最大输出令牌数 |
| `claude.base_url` | — | 覆盖 API 基础 URL |
| `claude.thinking` | 0 | 扩展思考预算（0 = 禁用） |

### Ollama（本地 LLM）

| 键 | 默认值 | 描述 |
|------|--------|------|
| `ollama.url` | `http://localhost:11434` | Ollama 服务器 URL |
| `ollama.model` | `llama3` | 模型名称 |

### OpenAI（兼容）

| 键 | 默认值 | 描述 |
|------|--------|------|
| `openai.api_key` | — | API 密钥 |
| `openai.model` | `gpt-4` | 模型 ID |
| `openai.base_url` | `https://api.openai.com/v1` | API 基础 URL |

### Telegram

| 键 | 默认值 | 描述 |
|------|--------|------|
| `telegram.token` | — | 机器人令牌（支持热重载） |
| `telegram.allowed_ids` | `[]` | 用户 ID 白名单（空 = 所有） |
| `telegram.debounce_window` | 2s | 消息合并时间窗口 |

### 存储

| 键 | 默认值 | 描述 |
|------|--------|------|
| `storage.path` | `{DataDir}/iulita.db` | SQLite 数据库路径（仅重启） |

### 服务器

| 键 | 默认值 | 描述 |
|------|--------|------|
| `server.enabled` | true | 启用仪表盘服务器 |
| `server.address` | `:8080` | 监听地址（仅重启） |

### 认证

| 键 | 默认值 | 描述 |
|------|--------|------|
| `auth.jwt_secret` | （自动生成） | JWT 签名密钥 |
| `auth.token_ttl` | 24h | 访问令牌 TTL |
| `auth.refresh_ttl` | 7d | 刷新令牌 TTL |

### 代理

| 键 | 默认值 | 描述 |
|------|--------|------|
| `proxy.url` | — | HTTP/SOCKS5 代理（仅重启） |

### 记忆

| 键 | 默认值 | 描述 |
|------|--------|------|
| `skills.memory.half_life_days` | 30 | 时间衰减半衰期 |
| `skills.memory.mmr_lambda` | 0 | MMR 多样性（推荐 0.7） |
| `skills.memory.vector_weight` | 0 | 混合搜索权重 |
| `skills.memory.triggers` | `[]` | 记忆触发关键词 |

### 洞察

| 键 | 默认值 | 描述 |
|------|--------|------|
| `skills.insights.min_facts` | 20 | 生成所需的最少事实数 |
| `skills.insights.max_pairs` | 6 | 每次运行的最大簇对数 |
| `skills.insights.ttl` | 720h | 洞察过期时间（30 天） |
| `skills.insights.interval` | 24h | 生成频率 |
| `skills.insights.quality_threshold` | 0 | 最低质量分数 |

### 嵌入

| 键 | 默认值 | 描述 |
|------|--------|------|
| `embedding.enabled` | true | 启用 ONNX 嵌入 |
| `embedding.model` | `KnightsAnalytics/all-MiniLM-L6-v2` | 模型名称 |

### 调度器

| 键 | 默认值 | 描述 |
|------|--------|------|
| `scheduler.enabled` | true | 启用任务调度器 |
| `scheduler.worker_token` | — | 远程工作器的 Bearer 令牌 |

### 费用

| 键 | 默认值 | 描述 |
|------|--------|------|
| `cost.daily_limit_usd` | 0 | 每日费用上限（0 = 无限制） |

### 缓存

| 键 | 默认值 | 描述 |
|------|--------|------|
| `cache.enabled` | false | 启用响应缓存 |
| `cache.ttl` | 60m | 缓存 TTL |
| `cache.max_items` | 1000 | 最大缓存响应数 |

### 指标

| 键 | 默认值 | 描述 |
|------|--------|------|
| `metrics.enabled` | false | 启用 Prometheus 指标 |
| `metrics.address` | `:9090` | 指标服务器地址 |

## 设置向导

### CLI 向导（`iulita init`）

交互式设置，引导完成：
1. LLM 提供商选择（Claude/OpenAI/Ollama，多选）
2. API 密钥输入（存储在密钥链中）
3. 可选集成（Telegram、代理、嵌入）
4. 模型选择（从提供商动态获取）

密钥存入密钥链；非密钥存入 `config.toml`。

### Web 设置向导（Docker）

用于没有终端访问的 Docker 部署：

1. 当未配置 LLM 且向导未完成时，服务器以**设置模式**启动
2. 仅仪表盘模式（无技能、调度器或通道）
3. 5 步向导：欢迎/导入 → 提供商 → 配置 → 功能 → 完成
4. TOML 导入支持（粘贴现有配置）
5. 创建 `db_managed` 哨兵文件（禁用 TOML 加载）
6. 在 config_overrides 中设置 `_system.wizard_completed`

## 热重载

以下设置可在运行时更改而无需重启：

| 设置 | 触发方式 | 机制 |
|------|----------|------|
| Claude 模型/令牌数/密钥 | 仪表盘配置编辑器 | `UpdateModel()`/`UpdateMaxTokens()`/`UpdateAPIKey()` |
| Telegram 令牌 | 仪表盘配置编辑器 | `channelmgr.UpdateConfigToken()` → 重启实例 |
| 技能启用/禁用 | 仪表盘或聊天 | `registry.EnableSkill()`/`DisableSkill()` |
| 技能配置（API 密钥） | 仪表盘配置编辑器 | `ConfigReloadable.OnConfigChanged()` |
| 系统提示词 | 仪表盘配置编辑器 | `asst.SetSystemPrompt()` |
| 思考预算 | 仪表盘配置编辑器 | `asst.SetThinkingBudget()` |

### 仅重启设置

以下需要完全重启：
- `storage.path`
- `server.address`
- `proxy.url`
- `security.config_key_env`

## AES-256-GCM 加密

数据库中的密钥配置值被加密：

1. **密钥来源**：`IULITA_CONFIG_KEY` 环境变量 → 密钥链 → 自动生成文件
2. **算法**：AES-256-GCM（认证加密）
3. **格式**：`base64(12字节nonce ‖ 密文)`
4. **自动加密**：在 SKILL.md 中声明为 `secret_keys` 的键始终加密
5. **不泄露**：仪表盘 API 对加密键返回空值
