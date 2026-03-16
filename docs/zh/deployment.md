# 部署

## 本地安装

### 二进制文件

```bash
# 下载并安装
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-linux-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# 设置
iulita init        # 交互式向导
iulita             # 启动 TUI（默认）
iulita --server    # 无界面服务器模式
```

### 从源码构建

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build         # 前端 + Go 二进制文件 → ./bin/iulita
make build-go      # 仅 Go 二进制文件（跳过前端重建）
```

**前置要求**：Go 1.25+、Node.js 22+、npm

## Docker

### docker-compose.yml

```yaml
services:
  iulita:
    image: ghcr.io/iulita-ai/iulita:latest
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/app/data
      - ./config.toml:/app/config.toml:ro
      - ./skills:/app/skills:ro
    restart: unless-stopped
```

### 首次运行（Web 向导）

没有 `config.toml` 时，服务器以**设置模式**启动：

1. 访问 `http://localhost:8080`
2. 完成 5 步向导：
   - 欢迎 / 导入现有 TOML
   - LLM 提供商选择
   - 配置（API 密钥、模型）
   - 功能开关
   - 完成
3. 向导将配置保存到数据库
4. 创建 `db_managed` 哨兵文件（禁用 TOML 加载）

### 使用配置文件

```bash
cp config.toml.example config.toml
# 编辑 config.toml — 至少设置 claude.api_key
mkdir -p data
docker compose up -d
```

### Dockerfile（多阶段）

```
阶段 1（ui-builder）：node:22-alpine
    → npm ci + npm run build

阶段 2（go-builder）：golang:1.25-alpine
    → CGO_ENABLED=1（SQLite 必需）
    → 在 Go 构建前复制 UI dist

阶段 3（runtime）：alpine:3.21
    → ca-certificates + tzdata
    → 非 root 用户 "iulita"（UID 1000）
    → 暴露端口 8080
    → 入口点：iulita --server
```

**卷**：`/app/data` 用于 SQLite 数据库和 ONNX 模型缓存。

## 环境变量

所有配置键映射到环境变量：

```bash
# 必需
IULITA_CLAUDE_API_KEY=sk-ant-...

# 可选
IULITA_TELEGRAM_TOKEN=123456:ABC...
IULITA_STORAGE_PATH=/app/data/iulita.db
IULITA_SERVER_ADDRESS=:8080
IULITA_PROXY_URL=socks5://proxy:1080
IULITA_JWT_SECRET=your-secret-here
IULITA_CLAUDE_MODEL=claude-sonnet-4-5-20250929
```

## 反向代理

### nginx

```nginx
server {
    listen 443 ssl;
    server_name iulita.example.com;

    ssl_certificate /etc/ssl/certs/iulita.crt;
    ssl_certificate_key /etc/ssl/private/iulita.key;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # WebSocket 支持
    location /ws {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }

    location /ws/chat {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }
}
```

### Caddy

```txt
iulita.example.com {
    reverse_proxy localhost:8080
}
```

Caddy 自动处理 WebSocket 升级。

## 健康检查

### CLI 诊断

```bash
iulita --doctor
```

检查项：
- 配置文件可访问性
- 数据库连接性
- LLM 提供商可达性
- 密钥链可用性
- 嵌入模型状态

### Telegram 健康监控

Telegram 通道每 60 秒调用 `GetMe()`。连续失败会被记录。这可以检测网络问题和令牌吊销。

## 监控

### Prometheus 指标

在配置中启用：

```toml
[metrics]
enabled = true
address = ":9090"
```

关键指标：
- `iulita_llm_requests_total` — 按提供商/状态的 LLM 调用量
- `iulita_llm_cost_usd_total` — 累积费用
- `iulita_skill_executions_total` — 技能使用模式
- `iulita_messages_total` — 消息量（入/出）
- `iulita_cache_hits_total` — 缓存有效性

### 费用控制

```toml
[cost]
daily_limit_usd = 10.0  # 每日费用达到 $10 时停止 LLM 调用
```

费用在内存中跟踪（每日重置）并持久化到 `usage_stats` 表。

## 备份

### 数据库

SQLite 数据库是唯一的事实来源。备份 `{DataDir}/iulita.db` 文件：

```bash
# 简单复制（WAL 模式下无写入时安全）
cp ~/.local/share/iulita/iulita.db backup/

# 使用 SQLite 备份 API（写入期间安全）
sqlite3 ~/.local/share/iulita/iulita.db ".backup backup/iulita.db"
```

### 配置

如果使用基于文件的配置：
```bash
cp ~/.config/iulita/config.toml backup/
```

如果使用数据库管理的配置（Docker 向导）：
- 配置存储在数据库的 `config_overrides` 表中
- 备份数据库即包含配置

### 密钥

密钥链中的密钥**不包含**在文件备份中。导出它们：
```bash
export IULITA_CLAUDE_API_KEY=$(security find-generic-password -s iulita -a claude-api-key -w)  # macOS
```

## Makefile 目标

| 目标 | 描述 |
|------|------|
| `make build` | 构建前端 + Go 二进制文件 |
| `make build-go` | 仅 Go 二进制文件 |
| `make ui` | 仅构建 Vue SPA |
| `make run` | 构建 + 启动控制台 TUI |
| `make console` | 运行 TUI（go run，无构建） |
| `make server` | 构建 + 运行无界面服务器 |
| `make dev` | 开发模式：Vue 开发服务器 + Go 服务器 |
| `make test` | 运行所有测试（Go + 前端） |
| `make test-go` | 仅 Go 测试 |
| `make test-ui` | 仅前端测试 |
| `make test-coverage` | 两者的覆盖率 |
| `make tidy` | go mod tidy |
| `make clean` | 移除构建产物 |
| `make check-secrets` | 运行 gitleaks 扫描 |
| `make setup-hooks` | 安装 pre-commit 钩子 |
| `make release` | 打标签并推送发布 |

## 开发

### 热重载开发

```bash
make dev
```

这将启动：
1. 端口 5173 上带 HMR 的 Vue 开发服务器
2. 带 `--server` 标志的 Go 服务器

Vue 开发服务器将 API 调用代理到 Go 服务器。

### 运行测试

```bash
make test              # 所有测试
make test-go           # 带竞态检测器的 Go 测试
make test-ui           # Vitest
make test-coverage     # 覆盖率报告
```

### Pre-commit 钩子

```bash
make setup-hooks
```

安装 git pre-commit 钩子，运行 `gitleaks detect` 以防止意外提交密钥。
