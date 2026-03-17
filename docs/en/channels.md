# Channels

Iulita supports multiple communication channels. Each channel converts platform-specific messages into a universal `IncomingMessage` format and routes them through the assistant.

## Channel Capabilities

Each channel declares its capabilities via a bitmask on every message:

| Capability | Console | Telegram | WebChat |
|------------|---------|----------|---------|
| Streaming | via bubbletea | Yes (edit-based) | Yes (WebSocket) |
| Markdown | via glamour | Yes | HTML |
| Reactions | No | No | No |
| Buttons | No | Yes (inline keyboard) | Yes |
| Typing indicator | Yes | Yes | No |
| HTML | No | No | Yes |

Capabilities are per-message (not per-channel), allowing mixed behavior when multiple channels share one assistant. Skills can check capabilities via `channel.CapsFrom(ctx)` to adapt their output format.

## Console TUI

The default mode — a full-screen terminal chat powered by [bubbletea](https://github.com/charmbracelet/bubbletea).

### Features

- **Full-screen layout**: viewport (chat history) + divider + textarea (input) + status bar
- **Markdown rendering**: via [glamour](https://github.com/charmbracelet/glamour) with adaptive word wrap
- **Streaming**: live text appearance with spinner indicator
- **Slash commands**: `/help`, `/status`, `/compact`, `/clear`, `/quit`
- **Interactive prompts**: numbered options for skill interactions (e.g., weather location selection)
- **Background color detection**: adapts rendering before bubbletea starts

### Architecture

```
tuiModel (bubbletea)
    ├── viewport.Model (scrollable chat history)
    ├── textarea.Model (user input)
    ├── statusBar (skill name, token counts, cost)
    └── streamBuf (live streaming text)
```

The `console.Channel` struct holds a `*tea.Program` protected by `sync.RWMutex`. The bubbletea program runs in its own goroutine (blocking `Start()`), while `StartStream`, `SendMessage`, and `NotifyStatus` are called from the assistant goroutine concurrently.

### Streaming Bridge

When the assistant streams a response:

1. `StartStream()` returns `editFn` and `doneFn` closures
2. `editFn(text)` sends `streamChunkMsg` to bubbletea (accumulated full text)
3. `doneFn(text)` sends `streamDoneMsg` to bubbletea (finalize and append to history)
4. All messages are thread-safe via bubbletea's `p.Send()`

### Slash Commands

| Command | Description |
|---------|-------------|
| `/help` | Show all commands with descriptions |
| `/status` | Skill counts, daily cost, session tokens, message count |
| `/compact` | Manually trigger history compression (async) |
| `/clear` | Clear in-memory chat history (TUI only) |
| `/quit` / `/exit` | Exit application |

### Server Mode Coexistence

In console mode, the server runs in the background:
- Logs redirect to `iulita.log` (not stderr, to avoid TUI corruption)
- Dashboard is still accessible at the configured address
- Telegram and other channels run alongside the TUI

## Telegram

Full-featured Telegram bot with streaming, debouncing, and interactive prompts.

### Setup

1. Create a bot via [@BotFather](https://t.me/BotFather)
2. Set the token: `iulita init` (keyring) or `IULITA_TELEGRAM_TOKEN` env var
3. Optional: set `telegram.allowed_ids` to whitelist specific Telegram user IDs

### Features

- **User whitelist**: `allowed_ids` restricts who can chat with the bot. Empty = allow all (warning logged)
- **Message debouncing**: rapid messages from the same chat are coalesced (configurable window)
- **Streaming edits**: responses appear incrementally via `EditMessageText` (rate-limited to 1 edit/1.5s)
- **Message chunking**: messages over 4000 chars are split at paragraph/line/word boundaries, preserving code blocks
- **Reply threading**: first chunk replies to the user's message; subsequent chunks are standalone
- **Typing indicator**: `ChatTyping` action sent every 4 seconds while processing
- **Health monitoring**: `GetMe()` called every 60 seconds to detect connectivity issues
- **Interactive prompts**: inline keyboards for skill interactions (weather location, etc.)
- **Media support**: photos (largest size), documents (30MB limit), voice/audio (with transcription)
- **Built-in commands**: `/clear` (clear history), custom registered commands
- **Bookmark button**: 💾 inline keyboard button on every assistant response; click saves the full response as a fact with background LLM refinement
- **Live status messages**: real-time status updates during tool execution and agent orchestration, showing which skill is running and agent progress

### Message Processing Pipeline

```
Incoming Telegram Update
    │
    ├── Callback query?
    │   ├── "noop" → acknowledge silently
    │   ├── "remember:*" → bookmark handler (save fact + ✅ feedback)
    │   └── other → route to prompt handler
    ├── Not a message? → skip
    ├── User not in whitelist? → reject
    ├── /clear command? → handle directly
    ├── Registered command? → route to handler
    ├── Active prompt? → route text to prompt
    │
    ▼
Construct IncomingMessage
    │ Caps = Streaming | Markdown | Typing | Buttons
    │
    ├── Resolve user (platform → iulita UUID)
    ├── Lookup locale from DB
    ├── Download media (photo/document/voice)
    ├── Check rate limit
    │
    ▼
Debounce
    │ merge rapid messages (text joined with \n)
    │ timer reset on each new message
    │
    ▼
Handler (Assistant.HandleMessage)
```

### Debouncer

The debouncer coalesces rapid messages from the same chat to prevent multiple LLM calls:

- Each `chatID` has a buffer with a `time.AfterFunc` timer
- Adding a message resets the timer
- When the timer fires, all buffered messages are merged:
  - Text joined with `"\n"`
  - Images and documents concatenated
  - First message's metadata preserved
- If `debounce_window = 0`, messages are processed immediately (non-blocking)
- `flushAll()` processes remaining buffers during shutdown

### Message Chunking

Long responses are split into Telegram-compatible chunks (4000 chars max):

1. Try splitting at paragraph boundaries (`\n\n`)
2. Try splitting at line boundaries (`\n`)
3. Try splitting at word boundaries (` `)
4. Hard split as last resort
5. **Code block awareness**: if splitting inside a ``` block, close it with ``` and reopen in the next chunk

### Configuration

| Key | Default | Description |
|-----|---------|-------------|
| `telegram.token` | — | Bot token (hot-reloadable) |
| `telegram.allowed_ids` | `[]` | User ID whitelist (empty = allow all) |
| `telegram.debounce_window` | 2s | Message coalescing window |

## WebChat

WebSocket-based web chat embedded in the dashboard.

### Protocol

**Connection**: WebSocket at `/ws/chat?user_id=<uuid>&username=<name>&chat_id=<optional>`

**Incoming messages** (client → server):
```json
{
  "text": "user message",
  "chat_id": "web:abc123",
  "prompt_id": "prompt_123_1",       // only for prompt responses
  "prompt_answer": "option_id",      // only for prompt responses
  "remember_message_id": "nano_ts"   // only for bookmark requests
}
```

**Outgoing messages** (server → client):

| Type | Purpose | Key Fields |
|------|---------|------------|
| `message` | Normal response | `text`, `message_id`, `timestamp` |
| `stream_edit` | Streaming update | `text`, `message_id`, `timestamp` |
| `stream_done` | Stream finalized | `text`, `message_id`, `timestamp` |
| `status` | Processing events | `status`, `skill_name`, `success`, `duration_ms` |
| `prompt` | Interactive question | `text`, `prompt_id`, `options[]` |
| `remember_ack` | Bookmark confirmation | `remember_ack.message_id`, `remember_ack.fact_id`, `remember_ack.status` |

### Bookmark Protocol

The bookmark feature allows users to save assistant responses as facts via a UI button.

**Flow:**
1. Server sends `message` or `stream_done` with `message_id` (Unix nanosecond timestamp)
2. Server caches content keyed by `(message_id, chatID)` for 10 minutes
3. Frontend shows 💾 icon on hover over assistant messages
4. User clicks → sends `{"remember_message_id": "<message_id>"}`
5. Server validates ownership (`chatID` must match cached entry), saves as fact with `source_type="bookmark"`, enqueues background LLM refinement
6. Server sends `{"type": "remember_ack", "remember_ack": {"message_id": "...", "status": "saved", "fact_id": 42}}`
7. Frontend updates icon to ✅

**Status values**: `saved`, `error`, `expired` (message no longer in cache)

### Authentication

WebChat does **not** use the UserResolver. The frontend obtains a JWT token via `/api/auth/login`, extracts `user_id` from the payload, and passes it as a WebSocket query parameter. The channel trusts this `user_id` directly.

### Write Serialization

All WebSocket writes go through a per-connection `sync.Mutex` to prevent concurrent write panics. Each connection is tracked in a `clients[chatID]` map.

### Interactive Prompts

Prompts use atomic counter-based IDs: `prompt_<timestamp>_<counter>`. The server sends a `prompt` message with options; the client responds with `prompt_id` and `prompt_answer`. Pending prompts are stored in a `sync.Map` with a timeout.

## Channel Manager

The `channelmgr.Manager` orchestrates all channel instances at runtime.

### Lifecycle

- **StartAll**: loads all channel instances from DB, starts each in a goroutine
- **StopInstance**: cancels context, waits on done channel (5s timeout)
- **AddInstance / UpdateInstance**: for dashboard-created/modified instances
- **Hot-reload**: `UpdateConfigToken(token)` restarts config-sourced Telegram instances

### Message Routing

When the assistant needs to send a proactive message (reminder, heartbeat):

1. Look up which channel instance owns the `chatID` via DB
2. If found and running, use that channel's sender
3. Fallback: use the first running channel

### Supported Channel Types

| Type | Source | Hot-Reload |
|------|--------|------------|
| Telegram | Config or DB | Token hot-reload |
| WebChat | DB (bootstrap) | — |
| Console | Console mode only | — |

## User Resolution

The `DBUserResolver` maps platform identities to iulita UUIDs:

1. Look up `user_channels` by `(channel_type, channel_user_id)`
2. If found → return existing `user.ID`
3. If not found and auto-registration enabled:
   - Create new `User` with random password and `MustChangePass: true`
   - Bind channel to user
   - Return new UUID
4. If not found and auto-registration disabled → reject

**Per-channel locale**: after resolution, `GetChannelLocale(ctx, channelType, channelUserID)` is called to set `msg.Locale` from the DB-stored preference.

## Status Events

Channels receive `StatusEvent` notifications for UX feedback:

| Type | When | Use |
|------|------|-----|
| `processing` | Message received, before LLM call | Show "thinking..." |
| `skill_start` | Before skill execution | Show skill name |
| `skill_done` | After skill execution | Show duration, success/failure |
| `stream_start` | Before streaming begins | Prepare streaming UI |
| `error` | On error | Show error message |
| `locale_changed` | After set_language skill | Update UI locale |
| `orchestration_started` | Before launching sub-agents | Show agent count |
| `agent_started` | Per agent, before run | Show agent name + type |
| `agent_progress` | Per agent, after each LLM turn | Update turn counter |
| `agent_completed` | Per agent, on success | Show duration + tokens |
| `agent_failed` | Per agent, on error | Show error message |
| `orchestration_done` | After all agents finish | Show summary stats |
