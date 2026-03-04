# 安全

## 认证

### JWT

- **算法**：HMAC-SHA256
- **访问令牌 TTL**：24 小时
- **刷新令牌 TTL**：7 天
- **Claims**：`user_id`、`username`、`role`
- **密钥**：未配置时自动生成 32 字节十六进制
- **存储**：密钥链（macOS Keychain / Linux SecretService）或 `IULITA_JWT_SECRET` 环境变量

### 密码

- **哈希**：bcrypt，默认开销
- **引导**：第一个用户获得随机密码，`MustChangePass: true`
- **仪表盘**：`POST /api/auth/change-password` 修改密码端点

### 中间件

1. `FiberMiddleware` — 验证 `Authorization: Bearer <token>`，将 claims 存储在 fiber locals 中
2. `AdminOnly` — 检查 `role == admin`，否则返回 403

## 密钥管理

### 存储层

| 密钥 | 环境变量 | 密钥链 | 文件回退 |
|------|----------|--------|----------|
| Claude API 密钥 | `IULITA_CLAUDE_API_KEY` | `claude-api-key` | — |
| Telegram 令牌 | `IULITA_TELEGRAM_TOKEN` | `telegram-token` | — |
| JWT 密钥 | `IULITA_JWT_SECRET` | `jwt-secret` | 自动生成 |
| 配置加密密钥 | `IULITA_CONFIG_KEY` | `config-encryption-key` | `encryption.key` 文件 |

**解析顺序**：环境变量 → 密钥链 → 文件回退 → 自动生成。

### 配置加密（AES-256-GCM）

数据库中的运行时配置覆盖可以被加密：

- **算法**：AES-256-GCM（认证加密）
- **Nonce**：12 字节，每次加密随机生成
- **格式**：`base64(nonce ‖ 密文)`
- **自动加密**：在 SKILL.md 清单中声明为 `secret_keys` 的键
- **API 安全**：仪表盘对密钥键从不返回解密值
- **拒绝占位符**：`"***"` 或空值会被拒绝

## SSRF 防护

所有出站 HTTP 请求（Web 获取、Web 搜索、外部技能）都经过 SSRF 防护。

### 被阻止的 IP 范围

| 范围 | 类型 |
|------|------|
| `10.0.0.0/8` | RFC1918 私有 |
| `172.16.0.0/12` | RFC1918 私有 |
| `192.168.0.0/16` | RFC1918 私有 |
| `100.64.0.0/10` | 运营商级 NAT（RFC 6598） |
| `fc00::/7` | IPv6 唯一本地 |
| `127.0.0.0/8` | 回环 |
| `::1/128` | IPv6 回环 |
| `169.254.0.0/16` | 链路本地 |
| `fe80::/10` | IPv6 链路本地 |
| 多播范围 | 全部 |

IPv4 映射的 IPv6 地址在检查前归一化为 IPv4。

### 双层防护（无代理）

**第 1 层 — 预检 DNS**：连接前，解析主机名的所有 IP。如果任何 IP 是私有的，拒绝连接。

**第 2 层 — 连接时控制**：`net.Dialer.Control` 函数在连接时检查实际解析的 IP。这可以捕获 **DNS 重绑定攻击** — 主机名在预检时解析为公共 IP，但在实际连接前重绑定到私有 IP。

### 代理路径

配置代理（`proxy.url`）时，无法使用基于拨号器的方法（代理本身在 Kubernetes 集群中可能有私有 IP）。替代方案：

- `ssrfTransport.RoundTrip` 仅执行 URL 级预检
- 允许代理连接到私有 IP（集群内部代理的有意设计）
- 目标 URL 到私有 IP 仍然被阻止

### 主动代理检测

`isProxyActive()` 实际使用测试请求调用代理函数（不只是 `Proxy != nil`），因为 `http.DefaultTransport` 始终设置了 `Proxy = ProxyFromEnvironment`。

## 工具审批级别

| 级别 | 行为 | 技能 |
|------|------|------|
| `ApprovalAuto` | 立即执行 | 大多数技能（默认） |
| `ApprovalPrompt` | 用户必须确认 | Docker 执行器 |
| `ApprovalManual` | 管理员必须确认 | Shell 执行 |

### 流程

1. 技能通过 `ApprovalDeclarer` 接口声明其级别
2. 执行前，助手检查 `registry.ApprovalLevelFor(toolName)`
3. 对于 `Prompt`/`Manual`：将待处理的工具调用存储在 `approvalStore` 中
4. 向用户发送确认提示
5. 向 LLM 返回 `"awaiting approval"`（非阻塞）
6. 下一条用户消息与区域感知的审批词汇进行匹配检查
7. 如果批准：执行存储的工具调用，返回结果
8. 如果拒绝：返回"已取消"

### 区域感知词汇

审批词汇在所有 6 种语言目录中定义，并包含英语作为回退：

```
# 俄语肯定：
да, д, ок, подтвердить, подтверждаю, yes, y, ok, confirm

# 希伯来语否定：
לא, ביטול, בטל, no, n, cancel
```

## Telegram 安全

- **用户白名单**：`telegram.allowed_ids` 限制谁可以与机器人聊天
- **空白名单**：允许所有用户（记录警告）
- **速率限制**：每聊天滑动窗口速率限制器

## Shell 执行安全

`shell_exec` 技能有最严格的安全措施：

- **审批级别**：`ApprovalManual`（需要管理员确认）
- **仅白名单**：只有 `AllowedBins` 中的可执行文件可以运行
- **禁止路径**：可配置的不能出现在参数中的路径列表
- **路径遍历**：拒绝参数中的 `..`
- **输出限制**：最大 16KB
- **工作目录**：`os.TempDir()`（非项目目录）

## 速率限制

### 每聊天速率限制器

滑动窗口：按 `chatID` 跟踪时间戳。如果 `window` 内的消息数超过 `rate`，消息被拒绝。

### 全局操作限制器

固定窗口：所有聊天每小时的 LLM/工具操作总数。在窗口边界自动重置。

## 费用跟踪

- **内存中**：带互斥锁的每日费用跟踪，在日边界自动重置
- **持久化**：`IncrementUsageWithCost` 保存到 `usage_stats` 表
- **每日限额**：`cost.daily_limit_usd`（0 = 无限制）
- **按模型定价**：`config.ModelPrice{InputPerMillion, OutputPerMillion}`

## CI/CD 安全

- **Pre-commit 钩子**：通过 [gitleaks](https://github.com/gitleaks/gitleaks) 阻止密钥泄露
- **CI**：gitleaks action 扫描所有提交
- **CodeQL**：Go 和 JavaScript/TypeScript 的安全扩展查询（仓库公开时）
- **依赖**：Dependabot 告警（在 GitHub 设置中启用）

## 外部技能安全

- **Slug 验证**：`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$` — 防止路径遍历
- **校验和验证**：远程下载的 SHA-256
- **隔离验证**：技能必须声明隔离级别，根据配置标志检查
- **代码检测**：拒绝包含代码文件的技能，除非正确隔离
- **提示词注入扫描**：对技能正文中的可疑模式发出警告
- **最大归档大小**：可配置（ClawhHub 默认 50MB）
