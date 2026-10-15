# Security

## Authentication

### JWT

- **Algorithm**: HMAC-SHA256
- **Access token TTL**: 24 hours
- **Refresh token TTL**: 7 days
- **Claims**: `user_id`, `username`, `role`
- **Secret**: auto-generated 32-byte hex if not configured
- **Storage**: keyring (macOS Keychain / Linux SecretService) or `IULITA_JWT_SECRET` env

### Passwords

- **Hashing**: bcrypt with default cost
- **Bootstrap**: first user gets random password with `MustChangePass: true`
- **Dashboard**: password change endpoint at `POST /api/auth/change-password`

### Middleware

1. `FiberMiddleware` — validates `Authorization: Bearer <token>`, stores claims in fiber locals
2. `AdminOnly` — checks `role == admin`, returns 403 if not

## Secret Management

### Storage Layers

| Secret | Env Variable | Keyring | File Fallback |
|--------|-------------|---------|---------------|
| Claude API key | `IULITA_CLAUDE_API_KEY` | `claude-api-key` | — |
| Telegram token | `IULITA_TELEGRAM_TOKEN` | `telegram-token` | — |
| JWT secret | `IULITA_JWT_SECRET` | `jwt-secret` | auto-generate |
| Config encryption key | `IULITA_CONFIG_KEY` | `config-encryption-key` | `encryption.key` file |

**Resolution order**: environment variable → keyring → file fallback → auto-generate.

### Config Encryption (AES-256-GCM)

Runtime config overrides in the database can be encrypted:

- **Algorithm**: AES-256-GCM (authenticated encryption)
- **Nonce**: 12 bytes, randomly generated per encryption
- **Format**: `base64(nonce ‖ ciphertext)`
- **Auto-encrypt**: keys declared as `secret_keys` in SKILL.md manifests
- **API safety**: dashboard never returns decrypted values for secret keys
- **Reject placeholders**: `"***"` or empty values rejected for secret keys

## SSRF Protection

Web fetch/search and relevant external-skill URL fetches use SSRF protection.
The structured `http_request` engine additionally requires an explicit proxy.

### Blocked IP Ranges

| Range | Type |
|-------|------|
| `10.0.0.0/8` | RFC1918 private |
| `172.16.0.0/12` | RFC1918 private |
| `192.168.0.0/16` | RFC1918 private |
| `100.64.0.0/10` | Carrier-grade NAT (RFC 6598) |
| `fc00::/7` | IPv6 Unique Local |
| `127.0.0.0/8` | Loopback |
| `::1/128` | IPv6 loopback |
| `169.254.0.0/16` | Link-local |
| `fe80::/10` | IPv6 link-local |
| Multicast ranges | All |

IPv4-mapped IPv6 addresses are normalized to IPv4 before checking.

### Dual-Layer Protection (No Proxy)

**Layer 1 — Pre-flight DNS**: Before connecting, all IPs for the hostname are resolved. If any IP is private, the connection is rejected.

**Layer 2 — Connect-time Control**: A `net.Dialer.Control` function checks the actual resolved IP at connect time. This catches **DNS rebinding attacks** where a hostname resolves to a public IP during pre-flight but rebinds to a private IP before the actual connection.

### Proxy Path

When a proxy is configured (`proxy.url`), the dialer-based approach cannot be used (the proxy itself may have a private IP in Kubernetes clusters). Instead:

- `ssrfTransport.RoundTrip` performs URL-level pre-flight check only
- The proxy connection to private IPs is allowed (intentional for cluster-internal proxies)
- Target URLs to private IPs are still blocked

### Active Proxy Detection

`isProxyActive()` actually calls the proxy function with a test request (not just `Proxy != nil`), because `http.DefaultTransport` always has `Proxy = ProxyFromEnvironment` set.

## Tool Approval Levels

| Level | Behavior | Skills |
|-------|----------|--------|
| `ApprovalAuto` | Execute immediately | Most skills (default) |
| `ApprovalPrompt` | User must confirm | Docker executor |
| `ApprovalManual` | Admin must confirm | Shell exec |

### Flow

1. Skill declares its level via `ApprovalDeclarer` interface
2. Before execution, assistant checks `registry.ApprovalLevelFor(toolName)`
3. For `Prompt`/`Manual`: stores pending tool call in `approvalStore`
4. Sends confirmation prompt to user
5. Returns `"awaiting approval"` to LLM (non-blocking)
6. Next user message checked against locale-aware approval vocabulary
7. If approved: executes stored tool call, returns result
8. If rejected: returns "cancelled"

### Locale-Aware Vocabulary

Approval words are defined in all 6 language catalogs and include English as fallback:

```
# Russian affirmative:
да, д, ок, подтвердить, подтверждаю, yes, y, ok, confirm

# Hebrew negative:
לא, ביטול, בטל, no, n, cancel
```

## Telegram Security

- **User whitelist**: `telegram.allowed_ids` restricts who can chat with the bot
- **Empty whitelist**: allows all users (warning logged)
- **Rate limiting**: per-chat sliding window rate limiter

## Shell Exec Security

The `shell_exec` skill has the strictest security:

- **Approval level**: `ApprovalManual` (admin confirmation required)
- **Whitelist-only**: only executables in `AllowedBins` can run
- **Forbidden paths**: configurable list of paths that cannot appear in arguments
- **Path traversal**: `..` in arguments is rejected
- **Output limit**: max 16KB
- **Working directory**: `os.TempDir()` (not project directory)

## Rate Limiting

### Per-Chat Rate Limiter

Sliding window: tracks timestamps per `chatID`. If message count within `window` exceeds `rate`, the message is rejected.

### Global Action Limiter

Fixed window: total count of LLM/tool actions per hour across all chats. Auto-resets at window boundary.

## Cost Tracking

- **In-memory**: daily cost tracked with mutex, auto-resets at day boundary
- **Persistent**: `IncrementUsageWithCost` saves to `usage_stats` table
- **Daily limit**: `cost.daily_limit_usd` (0 = unlimited)
- **Per-model pricing**: `config.ModelPrice{InputPerMillion, OutputPerMillion}`

## CI/CD Security

- **Pre-commit hook**: blocks secrets via [gitleaks](https://github.com/gitleaks/gitleaks)
- **CI**: gitleaks action scans all commits
- **CodeQL**: security-extended queries for Go and JavaScript/TypeScript (when repo is public)
- **Dependencies**: Dependabot alerts (enable in GitHub settings)

## External Skill Security

- **Slug validation**: `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$` — prevents path traversal
- **Checksum verification**: SHA-256 for remote downloads
- **Isolation validation**: skills must declare isolation level, checked against config flags
- **Code detection**: rejects skills with code files unless properly isolated
- **Prompt injection scanning**: warns on suspicious patterns in skill body
- **Max archive size**: configurable (default 50MB for ClawhHub)


## Browser sessions and isolated remote API

Dashboard `/ws/chat` validates the current account and access token; supplied
identity fields are not authority. `/ws` is an admin-only event feed. A supplied
Origin must match the host and transport security; expiration cancels the socket's
pending work. Proxy logs must not retain token-bearing WebSocket query strings.

The [mobile/remote API](mobile-api.md) uses a separate RS256 OAuth realm, private
keys and persistent revocation checks. Dashboard JWT settings above do not apply.
Services get chat scopes only; CLI cannot receive/sync as a phone executor.
Private phone results, UI content and model answers use ephemeral storage and
explicit recipient/input consent, outside ordinary history, events and caches.
UI/Vision remain unavailable at startup until joint native acceptance.

## HTTP grants and proxy boundary

`http_request` separates mutation approval, credential-origin grants and large
transfer confirmation. Standing mutation approvals can be revoked through
`/api/http-standing-grants`; downloads over 100 MB always ask again. A network
failure after a mutation can mean `outcome_unknown`, not permission to retry.

Pre-flight target validation through a proxy is not a connect-time destination
check inside that proxy. Configure proxy-side destination ACLs and DNS-rebinding
protection; do not infer them from the application's URL checks. There is no
direct fallback when the mandatory HTTP-tool proxy is unavailable.
