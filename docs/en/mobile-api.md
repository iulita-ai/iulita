# Mobile, CLI and service API

::: warning Availability
This chapter describes the backend in the current source tree. It has not been
included in a published release through v0.47.0. All mobile feature flags default
to `false`. There is no Android, iOS or remote CLI application shipped with this
backend; server simulators do not establish Pixel/GrapheneOS compatibility.
:::

The API uses native gRPC over HTTP/2 and TLS, with the package
`iulita.mobile.v1`. OAuth runs on a separate HTTPS listener. Dashboard JWTs,
dashboard WebSockets and worker credentials cannot authenticate this API.

## Choose a registration

| Kind | Login | Scope ceiling | Intended use |
| --- | --- | --- | --- |
| `native` | Browser Authorization Code + PKCE S256 | Chats, device management, device receive/sync, phone data/UI | A native application and its device executor |
| `cli` | Device Authorization Grant, then refresh | Chats, device management, phone data/UI; no receive/sync | An interactive remote console |
| `service` | Client Credentials, HTTP Basic | `chats:read`, optionally `chats:write` | An unattended integration bound to one owner |

Each registration has its own scope ceiling and revocation state. Request only
the needed subset. OAuth scopes do not grant Android permissions, local consent
or an execution lease. A CLI cannot impersonate the phone executor. A service
cannot read phone data, operate the UI or use device-management RPCs, even if its
owner has those rights. Service-initiated chat work also cannot obtain phone tools.

Services have a separate principal ID bound to an existing enabled owner whose
initial password has been changed. Their access tokens last 300 seconds, have
no refresh token, and default to `chats:read` when scope is omitted. Enabling
`chats:write` permits owner-scoped chat mutations and model usage; it is an
explicit policy choice.

## Prepare a server

Use the existing account database and an initialized model configuration. Set up
the realm offline before enabling the feature:

```sh
iulita mobile-init --settings /etc/iulita/mobile-runtime.json
```

This command creates private realm keys and OAuth storage; it starts no listener.
Use the repository's
[runtime JSON example](https://github.com/iulita-ai/iulita/blob/main/deploy/mobile/runtime.example.json).
Settings require an exact HTTPS issuer origin, audience, absolute clean paths for
the existing account DB, separate OAuth DB, realm directory and TLS files, and
distinct IP:port listen addresses. The certificate must cover the issuer and the
TLS private key must be a private regular file. Startup does not generate missing
keys or silently reset the realm.

```toml
[mobile]
chat_enabled = true
device_enabled = false
ui_enabled = false
vision_enabled = false
runtime_file = "/etc/iulita/mobile-runtime.json"
```

`ui_enabled` requires `device_enabled`; `vision_enabled` requires `ui_enabled`.
UI and Vision also have a build-owned joint native acceptance gate: this build
rejects enabling either at startup. Their server mechanisms can be exercised by
simulators, but configuration alone cannot make them available to real clients.
The equivalent environment variables are `IULITA_MOBILE_CHAT_ENABLED`,
`IULITA_MOBILE_DEVICE_ENABLED`, `IULITA_MOBILE_UI_ENABLED`,
`IULITA_MOBILE_VISION_ENABLED` and `IULITA_MOBILE_RUNTIME_FILE`.

Both listeners require TLS 1.3. Preserve HTTP/2 to gRPC and the configured OAuth
Host when routing through ingress. Forwarded headers do not establish OAuth TLS
or identity. See the
[Traefik examples](https://github.com/iulita-ai/iulita/tree/main/deploy/mobile).
Keep health and metrics endpoints on an appropriate administrative network.

## Register and administer clients

Registration is an offline operator action. Stop the running mobile runtime
before these commands: it holds an exclusive realm lock. There is no public
dynamic registration endpoint.

A minimal CLI registration file:

```json
{"client_id":"personal-cli","name":"Personal console","kind":"cli","scopes":["chats:read","chats:write"]}
```

```sh
iulita mobile-client register --settings /etc/iulita/mobile-runtime.json --registration /etc/iulita/cli-registration.json
iulita mobile-client list --settings /etc/iulita/mobile-runtime.json
```

For a service, use `kind: "service"`, an existing `owner_id` UUID and an explicit
scope list. Do not supply `principal_id`, `epoch` or `disabled`; the server owns
those fields. Services and CLI registrations have no redirect URIs; native
registrations require explicit HTTPS redirect URIs.

```sh
iulita mobile-client register --settings /etc/iulita/mobile-runtime.json --registration /etc/iulita/service-registration.json --secret-out /secure/service-secret
iulita mobile-client rotate-secret --settings /etc/iulita/mobile-runtime.json --client-id reporting-service --secret-out /secure/service-secret-next
iulita mobile-client disable --settings /etc/iulita/mobile-runtime.json --client-id reporting-service
```

Service secrets are generated into a **new** absolute file with mode `0600`,
durably written before the credential change. Existing files are not overwritten;
secrets are not printed or returned by `list`. Rotation invalidates the old secret
and previously issued service tokens; there is no overlap window. Disabling a
registration also invalidates its access. The primary native registration cannot
be disabled with this command.

## OAuth integration

Discover metadata at `/.well-known/oauth-authorization-server` and public signing
keys at `/oauth2/jwks` on the issuer. The precise request schemas and extension
fields are in the
[OAuth OpenAPI contract](https://github.com/iulita-ai/iulita/blob/main/api/mobile-oauth.openapi.json).
This is OAuth2 with Iulita installation-binding extensions; it does not implement
OpenID Connect login or issue ID tokens.

### Native browser login

Use an external browser with `state`, S256 PKCE and
`iulita_installation_key_thumbprint`. The registered redirect must match exactly.
Check the returned issuer and state before exchanging the code; exchange includes
the verifier and `iulita_installation_jwk` (a P-256 public JWK). Android App Links
can publish `/.well-known/assetlinks.json` when the package and release certificate
fingerprints are configured. Without them, that endpoint returns 404.

### CLI Device Flow

1. Create a P-256 installation key and retain its private half locally.
2. POST a form to `/oauth2/device_authorization` with `client_id`, requested
   `scope` and `iulita_installation_key_thumbprint`.
3. Display the returned `verification_uri` and `user_code`. The user enters the
   code, logs in and approves in the browser. The page warns against approving a
   code supplied by someone else.
4. Poll `/oauth2/token` using grant type
   `urn:ietf:params:oauth:grant-type:device_code`, `client_id`, `device_code` and
   `iulita_installation_jwk`. No client secret is used.
5. Respect the returned interval (initially 5 seconds), `authorization_pending`
   and `slow_down`. `slow_down` increases the polling interval by 5 seconds.
   Stop on `access_denied` or `expired_token`; the request expires after 600 seconds.

Native and CLI tokens can initially have stage `pending_activation` or
`provisional_recovery`. They do not authorize normal active-stage RPCs. Sign the
returned installation challenge with the bound key, call `ActivateDevice` or
`CompleteRecovery`, then refresh to obtain active authority. A provisional JWT
does not become active in place. Preserve operation IDs when recovering an
uncertain RPC response. Refresh tokens rotate; reuse can revoke the family.

### Service tokens

POST `/oauth2/token` with HTTP Basic authentication for the registered client and
form fields `grant_type=client_credentials` and optional `scope`. Credentials in
the form body are rejected. The response has `iulita_principal_type: "service"`
and no installation challenge or refresh token. Use the returned access token as
gRPC metadata `authorization: Bearer …`. Obtain another token after expiry.
Revoke a service access token at `/oauth2/revoke` using the same client’s HTTP
Basic credentials and a `token` form field.

## Chats and device work

The [Protobuf definitions](https://github.com/iulita-ai/iulita/tree/main/api/proto/iulita/mobile/v1)
are the RPC reference. Send `ClientContext.protocol_version = 1`. Mutations use
an `Operation` UUID and timestamp: retry an uncertain response with the same ID
and unchanged semantic payload. Changed payload under the same ID is a conflict.

Chats are separate owner-scoped conversations with settings, history generations,
turns and resumable events. Clear/cancel/revocation fence pending work; a stale
cursor may require a new snapshot. Ordinary chat transcripts contain no raw
phone observations or UI images. Dashboard history endpoints are not an alternate
way to read mobile conversations.

Device tasks require an active owner/device/session binding and live Control
stream. Supported server contracts include status, location, notifications,
torch/navigation and constrained UI tasks. They do not perform the action without
a future device executor. UI execution additionally needs an allowlisted scenario,
local confirmation, a current snapshot, generation fencing and a short live lease.
An empty scenario list authorizes no UI applications. Lost lease or uncertain
execution is reported as `unknown_outcome`; do not automatically repeat a mutation.

Before model processing, `PrepareProcessing` returns the exact prepared input,
question, recipient/profile/endpoint and consent digest. The future client must
display that prepared preview and approve its digest with `ProcessResult`.
Changing the recipient, data or authority invalidates approval. Private processing
does not retry or fail over after a provider send, and does not persist raw data,
images or derived private answers in ordinary history, events, facts or caches.
Ephemeral results disappear on expiry or process restart. External chat bindings
are explicit and expiring; they deliver fixed metadata notifications, not raw
phone results.

## Revocation, backup and verification

Access is checked against current account, client, device, token-family and realm
epochs, including during streams and before execution. Account disable/password
changes, family/device revoke, client disable/rotation and realm rotation fence
the relevant authority. Revocation cannot undo an action already executed or data
already sent to a provider.

Keep consistent account/OAuth DB backups and private realm key material. With the
runtime stopped, `iulita mobile-rotate --settings …` rotates realm credentials;
`--restore` invalidates restored authority with a fresh signing epoch. `--recover`
is for an interrupted offline rotation. Restoring old files and starting normally
is not a safe revocation reset. Schema downgrades require matching backups.

Server tests cover TLS/gRPC, OAuth, ownership, concurrent idempotency, device
simulation, privacy, leases and revocation. Run `go test -race ./...`; contract
checks use `scripts/check_mobile_oauth.py` with
`scripts/requirements-mobile-contract.txt`. Device permissions, Accessibility,
background execution and screenshots on Android/GrapheneOS remain client/device
acceptance work. iOS requires its own capability design.
