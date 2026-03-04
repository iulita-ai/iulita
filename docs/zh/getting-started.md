# 快速入门

## 概述

Iulita 是一款个人 AI 助手，它从你的真实数据中学习，而非产生幻觉。它只存储你明确分享的经过验证的事实，通过交叉引用你的实际数据构建洞察，从不编造它不知道的内容。

**控制台优先**：默认启动全屏 TUI 聊天界面。也可作为无界面服务器运行，支持 Telegram、Web 聊天和 Web 仪表盘。

## 安装

### 方式一：下载预编译二进制文件

从 [GitHub Releases](https://github.com/iulita-ai/iulita/releases/latest) 下载最新版本：

```bash
# macOS (Apple Silicon)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-darwin-arm64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-darwin-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# Linux (x86_64)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-linux-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/
```

### 方式二：从源码构建

**前置要求**：Go 1.25+、Node.js 22+、npm

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build
```

这将构建 Vue 3 前端和 Go 二进制文件。输出为 `./bin/iulita`。

仅构建 Go 二进制文件（跳过前端）：

```bash
make build-go
```

### 方式三：Docker

```bash
cp config.toml.example config.toml
# 编辑 config.toml — 至少设置 claude.api_key
mkdir -p data
docker compose up -d
```

预构建镜像：

```yaml
services:
  iulita:
    image: ghcr.io/iulita-ai/iulita:latest
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/app/data
      - ./config.toml:/app/config.toml:ro
```

首次运行时如果没有配置文件，服务器将以**设置模式**启动 — 在 `http://localhost:8080` 提供 Web 向导，引导你完成提供商选择、功能配置和 TOML 导入。

## 首次运行

### 交互式设置向导

```bash
iulita init
```

向导将引导你完成：
1. **LLM 提供商选择** — Claude（推荐）、OpenAI 或 Ollama
2. **API 密钥输入** — 安全存储在系统密钥链中（macOS Keychain、Linux SecretService）
3. **可选集成** — Telegram 机器人令牌、代理设置、嵌入提供商
4. **模型选择** — 从选定的提供商动态获取可用模型

密钥在可用时存储在操作系统密钥链中，否则回退到 `~/.config/iulita/encryption.key` 加密文件。

### 启动控制台 TUI（默认模式）

```bash
iulita
```

这将启动交互式全屏 TUI。输入消息，使用 `/help` 查看可用命令。

**控制台斜杠命令：**
| 命令 | 描述 |
|------|------|
| `/help` | 显示可用命令 |
| `/status` | 显示技能数量、每日费用、会话令牌数 |
| `/compact` | 手动压缩聊天历史 |
| `/clear` | 清除内存中的聊天历史 |
| `/quit` / `/exit` | 退出应用程序 |

**键盘快捷键：**
- `Enter` — 发送消息
- `Ctrl+C` — 退出
- `Shift+Enter` — 消息中换行

### 启动服务器模式

以后台服务运行，支持 Telegram、Web 聊天和仪表盘：

```bash
iulita --server
```

或等效地：
```bash
iulita -d
```

仪表盘可通过 `http://localhost:8080` 访问（可通过 `server.address` 配置）。

## 配置

所有设置都在 `config.toml` 中（可选 — 零配置本地安装只需在密钥链中存入 API 密钥即可）。每个选项都可以通过带有 `IULITA_` 前缀的环境变量覆盖。

### 文件位置（符合 XDG 规范）

| 平台 | 配置 | 数据 | 缓存 | 日志 |
|------|------|------|------|------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

使用 `IULITA_HOME` 环境变量覆盖所有路径。

### 关键环境变量

| 变量 | 描述 |
|------|------|
| `IULITA_CLAUDE_API_KEY` | Anthropic API 密钥（Claude 必需） |
| `IULITA_TELEGRAM_TOKEN` | Telegram 机器人令牌 |
| `IULITA_CLAUDE_MODEL` | Claude 模型 ID |
| `IULITA_STORAGE_PATH` | SQLite 数据库路径 |
| `IULITA_SERVER_ADDRESS` | 仪表盘监听地址（`:8080`） |
| `IULITA_PROXY_URL` | 所有请求的 HTTP/SOCKS5 代理 |
| `IULITA_JWT_SECRET` | JWT 签名密钥（未设置时自动生成） |
| `IULITA_HOME` | 覆盖所有 XDG 路径 |

完整参考（包含所有技能配置）请参阅 [`config.toml.example`](../../config.toml.example)。

## CLI 参考

| 命令/标志 | 描述 |
|-----------|------|
| `iulita` | 启动交互式控制台 TUI（默认） |
| `iulita --server` / `-d` | 以无界面服务器运行 |
| `iulita init` | 交互式设置向导 |
| `iulita init --print-defaults` | 打印默认 config.toml |
| `iulita --doctor` | 运行诊断检查 |
| `iulita --version` / `-v` | 打印版本并退出 |

## 快速验证

设置完成后，验证一切正常：

```bash
# 检查诊断
iulita --doctor

# 启动 TUI
iulita

# 输入："remember that my favorite color is blue"
# 然后输入："what is my favorite color?"
```

如果助手正确回忆出 "blue"，说明记忆功能端到端正常工作。

## 后续步骤

- [架构](architecture.md) — 了解系统的构建方式
- [记忆与洞察](memory-and-insights.md) — 事实存储和交叉引用的工作原理
- [通道](channels.md) — 设置 Telegram、Web 聊天或自定义 TUI
- [技能](skills.md) — 探索全部 20 多个可用工具
- [配置](configuration.md) — 深入了解所有配置选项
- [部署](deployment.md) — Docker、Kubernetes 和生产环境设置
