# Telegram Secretary

Connect a bot to selected private chats using Telegram Secretary Mode, then open **Telegram Secretary** (`/secretary`) in Iulita's authenticated dashboard. Connections, granted Telegram rights, local processing consent and action approvals are separate states. Telegram's **Manage Bot** deep link opens owner-only navigation.

This uses the normal Bot API, including development bots on that service. The isolated `/test` transport is not supported for connected accounts. There is no MTProto login or automatic import of old conversations. The interface lists observed conversations, not every chat in the account. `can_read_messages` allows marking messages read; it is not a history API.

## Private drafts first

Content processing requires encrypted storage, a fresh owner login, an enabled channel binding and consent for both the account and each conversation. An administrator cannot view another owner's correspondence. Without encryption, diagnostics remain metadata-only.

The named model and media processors are shown before activation. Unknown model prices require explicit token-only acknowledgement; this does not guarantee a dollar cap. Durable drafts belong to Iulita, not Telegram's compose field. Edit, reject and approve the exact recipient and content. Approval and execution are separate actions. Unknown delivery outcomes remain visible and are never automatically replayed.

Correspondents cannot access personal memory, insights, credentials, tools or other conversations. Source edits/deletions invalidate context, derived extracts and drafts. Local pause, Telegram disconnect, local erasure and remote message deletion are different operations. External tasks are preserved.

## Account actions

```toml
[telegram]
business_live_writes = false
business_daily_financial_operations = 10
business_daily_stars = 100
business_restore_generation = "initial"
```

The deployment interlock is **off by default**. Keep it off for read-only evaluation. Changing it requires restart and never grants Telegram rights, enables owner policies or approves an action. The matching environment names start with `IULITA_TELEGRAM_BUSINESS_` and end in `LIVE_WRITES`, `DAILY_FINANCIAL_OPERATIONS`, `DAILY_STARS` or `RESTORE_GENERATION`.

Action families cover replies/media, own-message edits, read markers, separate own/all-message deletion, profile and gift settings, gifts/Stars viewing, gift conversion/deletion/upgrades/transfers, account Stars transfers and stories. Each needs its individual right, local policy and exact approval. Asset operations require two confirmations and finite daily limits; zero financial limits disable them. Account Stars are transferred to the bot balance. A grant never enables autonomous asset or profile management. Telegram's recent-message and method-specific restrictions remain authoritative, including the 24-hour reply window.

## Context and attachments

Manual and automatic compaction show the committed context size. Raw text and source-derived extracts have a seven-day retention boundary. Drafts expire after 24 hours; action approvals after five minutes. Export is authenticated and scope-bound, without public links.

Separate account and chat media consent names the configured processors. The latest incoming message or observed album is processed together, at most three items and 1 MiB original input per item. JPEG/PNG images are bounded/resized and require an image-capable selected model. PDF/text extraction is local. Voice/audio requires a bounded transcription path and a separate attempts/duration quota: a full decoded recording must fit within 120 seconds; longer audio is rejected, never silently clipped. Decoded WAV output is limited to 4 MiB. The speech quota reserves the worst-case duration, independently of model tokens, without promising an unconfigured currency cap.

Cached transcripts/extracts are attributed to their source and erased with it. Historical images are not automatically reanalysed; omitted attachments are disclosed. Inbound video analysis is unsupported. Maximum attachment counts do not override the finite combined text/image context budget: compact the conversation if needed. Local outgoing uploads are encrypted, expire after one hour and are physically removed by periodic cleanup.

## Optional automatic replies

Both account and chat need explicit opt-in and rules for request class, timezone, working hours and daily reply cap. Rules intersect; execution rechecks quiet hours. Bot messages, commands and attachment messages receive no automatic replies. Manual owner replies return that conversation to draft mode. Pausing or revoking access closes pending admission. A request already accepted for dispatch can still finish, so its confirmed or unknown outcome is retained.

## Restart and restore

Restart fences old approvals before publishing a replacement channel. Known connections are refreshed before account action admission; duplicate local consumers of one bot are rejected. Do not run another `getUpdates` consumer to debug a running bot.

**Before restoring an older database, change `business_restore_generation` to a new value outside that backup and start with account writes disabled.** The application cannot infer every out-of-band database replacement or reconstruct missed deletions through Telegram history. A changed restore generation quarantines restored correspondence and approvals. Explicit owner reset discards old content and requires new consent; it never reactivates old approvals. Preserve this control value independently of the restored database in your backup procedure.

## Operational diagnostics

When application metrics are enabled, `iulita_telegram_business_operations_total` counts bounded operation/kind/outcome categories; `operation_duration_seconds` and `age_seconds` show latency and observed backlog. No account IDs, chat IDs, connection IDs, usernames, message bodies or provider errors appear in labels. The owner interface also shows the latest processing status and a safe failure reason.

`iulita_telegram_business_reserved_total` measures conservative input/output token and known cost reservations, **not actual billed usage**. Unknown prices still require token-only consent. `iulita_telegram_business_last_success_timestamp_seconds` distinguishes database and upload retention sweeps, so one cannot mask failure of the other. Missing time series mean no observation yet, not success.

Example Prometheus alert expressions for your existing monitoring configuration when this mode is in use:

```promql
# Unknown sends need owner investigation; never replay automatically.
sum(increase(iulita_telegram_business_operations_total{operation="execute",outcome="unknown"}[15m])) > 0

# Competing consumers or repeated rights-refresh failures.
sum(increase(iulita_telegram_business_operations_total{operation="consumer",outcome="conflict"}[10m])) > 0
sum(increase(iulita_telegram_business_operations_total{operation="reconcile",outcome!="ok"}[10m])) > 3

# Hourly retention stopped succeeding, after the first successful sweep.
time() - iulita_telegram_business_last_success_timestamp_seconds{operation="retention",kind="database"} > 7200

# A local processing or sending budget is exhausted.
sum(increase(iulita_telegram_business_operations_total{outcome="quota"}[10m])) > 0
```

These examples create no external alerts or monitoring resources automatically. Investigate missing retention series separately after startup; process restarts reset diagnostic counters, while durable reservations, action outcomes and approvals remain in storage.
