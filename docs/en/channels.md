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

The table describes declared capability flags; Telegram reactions are also
handled directly by the adapter. Capabilities are per-message (not per-channel), allowing mixed behavior when multiple channels share one assistant. Skills can check capabilities via `channel.CapsFrom(ctx)` to adapt their output format.

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

- Long polling through `go-telegram/bot`, with typed reaction and callback updates.
- Streaming edits, or experimental drafts with `telegram.draft_streaming = true`
  (default off). Draft errors fall back to the edit path.
- Markdown converted to Telegram HTML: nested styles, blockquotes, expandable
  long quotes, spoilers, and monospace tables with emoji-aware column widths.
  Long replies split at block boundaries without cutting formatting tags.
- Processing reaction 👀; save a recent response with 🔖 or the 💾 button;
  native copy button on supported responses.
- Photos, documents, voice/audio, location pins and venue cards. Sending a venue
  requires explicit approval; private location data is not a reusable send grant.
- `/clear` clears stored history and attempts native deletion of recent bot
  messages within Telegram's limits (generally the last 48 hours).
- Native Todoist checklists where supported; a one-way mirror, without Todoist
  completion sync.
- Suspected secret replies use ephemeral delivery without bookmark/copy buttons.
  If that API fails, a regular-send fallback exists: do not treat this feature as
  a guaranteed private delivery mechanism.
- User whitelist, message coalescing, interactive prompts and tool status updates.

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
    │ Caps = Streaming | Markdown | Typing | Buttons | Locations
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

The HTML renderer splits responses into bounded Telegram chunks at block
boundaries. Oversized code blocks are rewrapped in each chunk. Raw HTML from
model output is stripped and text nodes are escaped. Stored bookmarks keep the
original Markdown, not the display HTML.

### Configuration

| Key | Default | Description |
|-----|---------|-------------|
| `telegram.token` | — | Bot token (hot-reloadable) |
| `telegram.allowed_ids` | `[]` | User ID whitelist (empty = allow all) |
| `telegram.debounce_window` | 1.5s | Message coalescing window |
| `telegram.draft_streaming` | false | Experimental draft streaming |

### Voice input {#voice-input}

Voice notes and audio are transcribed to text before assistant processing. The
input guard is 24 MB. Configure `transcription.provider`, `.api_key`, `.model`
and `.base_url` in dashboard settings; these settings take precedence over TOML
and support hot reload. Use provider `openai` for a Whisper-compatible endpoint
or `zai` for GLM-ASR. For a third-party compatible endpoint, set provider explicitly.

```toml
[transcription]
provider = "openai"
model = "whisper-large-v3"
base_url = "https://api.groq.com/openai/v1"
# Store transcription.api_key through dashboard settings.
```

If neither provider nor key is set, an existing Z.ai connection can enable
`glm-asr-2512` automatically. Explicit Z.ai mode can also reuse that connection's
key and endpoint. A key without a provider is not sufficient in the current
implementation: set `provider = "openai"` explicitly.

The container includes ffmpeg. When the transcoding wrapper is active, formats
other than MP3/WAV are converted to 16 kHz mono MP3. If ffmpeg is missing or
conversion fails, the original bytes are sent; strict endpoints can reject them.
GLM-ASR has a documented 30-second clip limit; longer notes are not automatically
segmented. Audio is sent to the selected transcription endpoint; a local Iulita
installation does not imply local-only transcription.

## Slack

The Socket Mode bot and owner-only personal OAuth read/search integration can
be enabled independently. Configure the bot channel and personal connection in
the dashboard. `slack_search` provides search/history/replies; personal OAuth
rejects write scopes. `slack_post` uses per-channel approval or automatic mode,
an allowlist, quiet hours, hourly caps and a secret filter. Search content cannot
silently grant autonomous posting authority. The admin Slack Activity view
contains bounded metadata rather than message bodies and queries.

## WebChat

WebSocket-based web chat embedded in the dashboard.

### Protocol

**Connection**: WebSocket at `/ws/chat?token=<access-token>`. For non-browser
clients, an `Authorization: Bearer …` header is also accepted at upgrade.
Do not record token-bearing URLs in proxy access logs.

**Incoming messages** (client → server):
```json
{
  "text": "user message",
  "client_message_id": "stable-request-id",
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

The server validates the access token and current account before upgrade, and
derives the identity and `web:<user_id>` chat ID itself. Client-supplied identity
overrides cannot select another account or chat. A supplied browser Origin must
match the host and secure/insecure scheme. Expired or replaced sockets cancel
pending work; the frontend refreshes credentials before reconnecting. WebChat
retains one chat slot per user. For separate conversations, use the
[mobile/remote API](mobile-api.md), with its own OAuth registration.

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
3. Legacy proactive delivery can fall back to the first running channel. Private
   phone bindings instead require the exact registered instance and destination;
   no fallback or raw-result delivery is allowed.

### Supported Channel Types

| Type | Source | Hot-Reload |
|------|--------|------------|
| Telegram | Config or DB | Token hot-reload |
| WebChat | DB (bootstrap) | — |
| Slack | DB | Runtime instance configuration |
| Discord | DB | Runtime instance configuration |
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
