# Getting Started

## Overview

Iulita is a personal AI assistant that learns from your real data, not hallucinations. It stores only verified facts you explicitly share, builds insights by cross-referencing your actual data, and never invents things it doesn't know.

**Console-first**: launches a full-screen TUI chat by default. Also runs as a headless server with Telegram, Web Chat, and a web dashboard.

## Installation

### Option 1: Download Pre-built Binary

Download the latest release from [GitHub Releases](https://github.com/iulita-ai/iulita/releases/latest):

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

### Option 2: Build from Source

**Prerequisites**: Go 1.25+, Node.js 22+, npm

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build
```

This builds the Vue 3 frontend and the Go binary. The output is `./bin/iulita`.

To build only the Go binary (skipping frontend):

```bash
make build-go
```

### Option 3: Docker

```bash
cp config.toml.example config.toml
# Edit config.toml — set claude.api_key at minimum
mkdir -p data
docker compose up -d
```

Pre-built image:

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

On first run without config, the server starts in **setup mode** — a web wizard at `http://localhost:8080` walks you through provider selection, feature configuration, and TOML import.

## First Run

### Interactive Setup Wizard

```bash
iulita init
```

The wizard guides you through:
1. **LLM Provider selection** — Claude (recommended), OpenAI, or Ollama
2. **API key entry** — stored securely in the system keyring (macOS Keychain, Linux SecretService)
3. **Optional integrations** — Telegram bot token, proxy settings, embedding provider
4. **Model selection** — dynamically fetches available models from the selected provider

Secrets are stored in the OS keyring when available, falling back to an encrypted file at `~/.config/iulita/encryption.key`.

### Launch Console TUI (Default Mode)

```bash
iulita
```

This launches the interactive full-screen TUI. Type messages, use `/help` for available commands.

**Console slash commands:**
| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/status` | Show skill counts, daily cost, session tokens |
| `/compact` | Manually compress chat history |
| `/clear` | Clear in-memory chat history |
| `/quit` / `/exit` | Exit the application |

**Keyboard shortcuts:**
- `Enter` — Send message
- `Ctrl+C` — Exit
- `Shift+Enter` — New line in message

### Launch Server Mode

For running as a background service with Telegram, Web Chat, and dashboard:

```bash
iulita --server
```

Or equivalently:
```bash
iulita -d
```

The dashboard is accessible at `http://localhost:8080` (configurable via `server.address`).

## Configuration

All settings are in `config.toml` (optional — zero-config local install works with just an API key in keyring). Every option can be overridden via environment variables with the `IULITA_` prefix.

### File Locations (XDG-compliant)

| Platform | Config | Data | Cache | Logs |
|----------|--------|------|-------|------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

Override all paths with `IULITA_HOME` environment variable.

### Key Environment Variables

| Variable | Description |
|----------|-------------|
| `IULITA_CLAUDE_API_KEY` | Anthropic API key (required for Claude) |
| `IULITA_TELEGRAM_TOKEN` | Telegram bot token |
| `IULITA_CLAUDE_MODEL` | Claude model ID |
| `IULITA_STORAGE_PATH` | SQLite database path |
| `IULITA_SERVER_ADDRESS` | Dashboard listen address (`:8080`) |
| `IULITA_PROXY_URL` | HTTP/SOCKS5 proxy for all requests |
| `IULITA_JWT_SECRET` | JWT signing key (auto-generated if not set) |
| `IULITA_HOME` | Override all XDG paths |

See [`config.toml.example`](../../config.toml.example) for the full reference with all skill configs.

## CLI Reference

| Command / Flag | Description |
|----------------|-------------|
| `iulita` | Launch interactive console TUI (default) |
| `iulita --server` / `-d` | Run as headless server |
| `iulita init` | Interactive setup wizard |
| `iulita init --print-defaults` | Print default config.toml |
| `iulita --doctor` | Run diagnostic checks |
| `iulita --version` / `-v` | Print version and exit |

## Quick Verification

After setup, verify everything works:

```bash
# Check diagnostics
iulita --doctor

# Launch TUI
iulita

# Type: "remember that my favorite color is blue"
# Then: "what is my favorite color?"
```

If the assistant correctly recalls "blue", memory is working end-to-end.

## Next Steps

- [Architecture](architecture.md) — understand how the system is built
- [Memory and Insights](memory-and-insights.md) — how fact storage and cross-referencing works
- [Channels](channels.md) — set up Telegram, Web Chat, or customize the TUI
- [Skills](skills.md) — explore all 20+ available tools
- [Configuration](configuration.md) — deep dive into all config options
- [Deployment](deployment.md) — Docker, Kubernetes, and production setup
