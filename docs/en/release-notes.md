# Release guide

This guide summarizes the public releases from v0.38.0 through v0.49.4. Follow the linked release for its
exact changelog. A source feature is not automatically available in an older
container or binary.

## v0.49.4: mobile stream keepalive

[v0.49.4](https://github.com/iulita-ai/iulita/releases/tag/v0.49.4) permits HTTP/2
keepalive pings at intervals of at least 30 seconds while an RPC is active.
Native clients can use 45 seconds to keep idle streams alive across network
proxies. Pings without active RPCs remain disallowed. Authentication expiry,
revocation, local-consent deadlines and stream capacity limits still apply.

## v0.49.3: explicit phone data requests

[v0.49.3](https://github.com/iulita-ai/iulita/releases/tag/v0.49.3) handles
`/phone status`, `/phone location` and `/phone notifications` as fixed requests
to the selected phone. They retain the normal turn journal, authority, consent
and native-session checks without relying on a model to invoke a tool.
Acknowledgements come from actual tool results; raw content remains in private
views. Ambiguous operations are never automatically repeated.

## v0.49.2: OAuth response contract compatibility

[v0.49.2](https://github.com/iulita-ai/iulita/releases/tag/v0.49.2) normalizes
Fosite browser/refresh replies to the published OpenAPI `token_type: Bearer`.
Device Flow and service replies already use this spelling. Token authority,
lifetimes, rotation and revocation remain unchanged.

## v0.49.1: chat refresh and browser OAuth

[v0.49.1](https://github.com/iulita-ai/iulita/releases/tag/v0.49.1) fixes mobile
snapshot cache exhaustion during chat refresh. The owner limit remains32
snapshots: a fresh snapshot replaces the oldest entry, whose cursors then
require resynchronization. Other owners and persisted messages are unaffected.

Login and consent forms use `Referrer-Policy: same-origin` so browser form
POSTs retain Origin. Exact Origin and CSRF checks remain mandatory; OAuth
redirect responses retain `no-referrer`.

## v0.49.0: explicit phone targets and runtime time samples

[v0.49.0](https://github.com/iulita-ai/iulita/releases/tag/v0.49.0) adds
`SendMessage.target_device_id`: the selected owned phone is frozen with the
accepted turn and enforced independently of model tool arguments. Reusing a
message ID with a different target is a conflict. Omitted targets bind to the
original caller device; they cannot select another owned phone implicitly.

The independent OAuth listener provides nonce-bound `/oauth2/time` samples.
Clock IDs are shared with gRPC server information, device commands, leases and
native session mirrors. Runtime restart or a detected wall-clock step changes
the clock ID and fences sessions tied to the old clock. Clients must still
account for round-trip delay, resolution, clock error, drift and sample age.

Account security reservations now queue before competing for the SQLite writer.
Waiting requests respect cancellation without entering the business operation;
transactional account and token revocation checks remain in place.

All mobile defaults remain off. UI/Vision remain behind the joint native
acceptance gate; this backend release does not ship an Android application or
automatically enable phone access. See [the integration guide](mobile-api.md).

## v0.48.1: HTTP approval management

[v0.48.1](https://github.com/iulita-ai/iulita/releases/tag/v0.48.1) connects the
standing-approval store to the production dashboard. The authenticated
`/api/http-standing-grants` API now lists and revokes approvals, instead of
falling through to the SPA. Users remain limited to their own grants;
administrators can inspect and revoke another owner's grants.

## v0.48.0: mobile and remote-client backend

[v0.48.0](https://github.com/iulita-ai/iulita/releases/tag/v0.48.0) adds isolated OAuth2 and gRPC, owner-scoped multiple chats,
device tasks, consent-bound private model processing and constrained UI leases.
CLI clients have Device Flow; unattended services have separate registrations
and chat-only Client Credentials. Registrations support disable, secret rotation
and access revocation. All flags default off.

Read the [API and operator guide](mobile-api.md) before enabling this subsystem.
No native mobile application or remote CLI is included. Simulator checks do not
establish Android/GrapheneOS or iOS compatibility.

## v0.46–v0.47: structured HTTP and consent

[`v0.46.0`](https://github.com/iulita-ai/iulita/releases/tag/v0.46.0) introduced
`http_request`: structured requests, credential references, bounded chains,
downloads and an explicit mandatory proxy. There is no direct-connection
fallback. [`v0.46.1`](https://github.com/iulita-ai/iulita/releases/tag/v0.46.1)
connected channel confirmations through the manager.

[`v0.47.0`](https://github.com/iulita-ai/iulita/releases/tag/v0.47.0) added
standing mutation approvals: an exact origin for 30 days or all origins for
7 days, with revocation through `/api/http-standing-grants`. Downloads above
100 MB always require a fresh confirmation. Credential-origin grants are
separate from mutation approval. See [Skills](skills.md#http-client-http-request-on-by-default)
and [Configuration](configuration.md#skillshttp).

## v0.44–v0.45: voice input

[`v0.44.0`](https://github.com/iulita-ai/iulita/releases/tag/v0.44.0) added
Whisper-compatible transcription, endpoint overrides and a 24 MB input guard.
v0.44.1–v0.44.4 added dashboard configuration, hot reload and reuse of the Z.ai
model connection for GLM-ASR. With no explicit transcription provider or key,
an available Z.ai connection is used automatically.

[`v0.45.0`](https://github.com/iulita-ai/iulita/releases/tag/v0.45.0) bundled
ffmpeg and converts non-MP3/WAV formats to 16 kHz mono MP3 when the transcoding
wrapper is active. Missing ffmpeg or failed conversion passes the original bytes
to the endpoint; a strict endpoint can still reject them. The documented
`glm-asr-2512` limit is 30 seconds; automatic segmentation is not included.
See [voice input](channels.md#voice-input).

## v0.40–v0.43: Telegram

- [`v0.40.0`](https://github.com/iulita-ai/iulita/releases/tag/v0.40.0): location
  pins and venue cards, with explicit approval for venue sends.
- [`v0.40.2`](https://github.com/iulita-ai/iulita/releases/tag/v0.40.2) through
  v0.40.4: HTML rendering, nested formatting, quotes, spoilers and aligned
  monospace tables with emoji-aware display widths.
- [`v0.41.0`](https://github.com/iulita-ai/iulita/releases/tag/v0.41.0): processing
  reactions, reaction bookmarks, copy buttons and native message deletion with
  `/clear`, subject to Telegram limits.
- [`v0.42.0`](https://github.com/iulita-ai/iulita/releases/tag/v0.42.0): migration
  to `go-telegram/bot` and native Todoist checklists. The checklist is a one-way
  mirror; checking it in Telegram does not complete the Todoist task.
- [`v0.43.0`](https://github.com/iulita-ai/iulita/releases/tag/v0.43.0): ephemeral
  delivery for detected secret replies and optional `telegram.draft_streaming`.
  Draft streaming defaults off and falls back to classic edits. Ephemeral send
  failure can fall back to a regular message; this is not a guaranteed privacy
  boundary.

See [Channels](channels.md) for current behavior.

## v0.39: model profiles and browser sessions

[`v0.39.0`](https://github.com/iulita-ai/iulita/releases/tag/v0.39.0) introduced
DeepSeek/GLM profiles, encrypted connections, task roles, explicit probes,
credential revocation and per-attempt accounting. v0.39.1–v0.39.2 protected browser
WebSockets and bound WebChat identity to the authenticated account.
[`v0.39.3`](https://github.com/iulita-ai/iulita/releases/tag/v0.39.3) added opt-in
paid classification with quality evaluation and verified backup profiles.

New connections and role changes require explicit verification and activation.
Probe and classifier evaluation calls can incur provider charges. Read
[model profiles and routing](../model-routing.md).

Before upgrading from v0.38 or earlier, retain a consistent SQLite backup and its
configuration encryption key. The usage index now includes provider identity;
rollback requires the matching pre-upgrade database, not just an older binary.

## v0.38: Slack

[`v0.38.0`](https://github.com/iulita-ai/iulita/releases/tag/v0.38.0) introduced
independently selectable Socket Mode bot and owner-only personal read/search
integration. Posting uses channel allowlists, approval/auto policy, quiet hours,
hourly limits and a secret filter. Personal OAuth rejects write scopes. Search
results cannot silently authorize autonomous posting. Audit views retain
metadata, not searched text or message bodies.
