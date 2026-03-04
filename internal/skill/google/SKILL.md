---
name: google_workspace
description: Access Gmail, Google Calendar, Contacts, Tasks, and manage Google authentication
capabilities:
  - google
config_keys:
  - skills.google.client_id
  - skills.google.client_secret
  - skills.google.redirect_url
  - skills.google.credentials_file
  - skills.google.scopes
secret_keys:
  - skills.google.client_id
  - skills.google.client_secret
  - skills.google_mail.system_prompt
  - skills.google_calendar.system_prompt
  - skills.google_contacts.system_prompt
  - skills.google_tasks.system_prompt
---

GOOGLE WORKSPACE INTEGRATION RULES:

## Skills
- `google_mail` — read emails, search inbox, unread summary. Does NOT mark as read.
- `google_calendar` — today's meetings, weekly overview, search events. Always show meeting links.
- `google_contacts` — search by name/email/phone, upcoming birthdays.
- `google_tasks` — list, create, complete, delete tasks. Highlight overdue items.
- `google_auth` — manage authentication, check status, configure credentials and scopes.

## Formatting
- If multiple Google accounts are connected, ask which to use unless obvious from context.
- Use the user's timezone for all dates/times.
- Emails: concise — sender, subject, key point.
- Calendar: time, title, meeting link.
- Tasks: highlight overdue.

## Authentication & Credential Resolution

Google credentials are resolved using a 5-level priority chain. The first valid source wins:

1. **IULITA_GOOGLE_TOKEN** env var — raw access token (expires in ~1h, good for quick testing)
2. **IULITA_GOOGLE_CREDENTIALS_FILE** env var — path to JSON file (service account or authorized user)
3. **skills.google.credentials_file** config key — same as above, but set via config/dashboard/chat
4. **DB accounts** (per-user) — OAuth2 tokens from dashboard Settings, scoped to each user
5. **Application Default Credentials (ADC)** — `GOOGLE_APPLICATION_CREDENTIALS` env or `gcloud auth application-default login`

### When the user asks about Google authentication:
- Use `google_auth` with action `status` to show current credential source and type.
- Use `google_auth` with action `list_accounts` to show connected OAuth2 accounts.
- Use `google_auth` with action `get_config` to show current configuration.

### When the user wants to configure credentials:
- **Service Account JSON file**: use `google_auth` with action `set_credentials_file` and value = file path. Admin only. The JSON must have `"type": "service_account"` and contain a private key. Obtained from Google Cloud Console → IAM → Service Accounts → Keys → Add Key → JSON.
- **Authorized User JSON file**: same action. The JSON has `"type": "authorized_user"` with `client_id`, `client_secret`, `refresh_token`. Obtained by running `gcloud auth application-default login` and copying the file from `~/.config/gcloud/application_default_credentials.json`.
- **OAuth2 (interactive)**: requires `client_id` + `client_secret` configured. User connects via dashboard Settings → Google → Connect Account. The flow redirects to Google consent screen, then stores encrypted tokens in DB.
- **Quick access token**: user sets env var `IULITA_GOOGLE_TOKEN=ya29.xxx`. No config change needed, highest priority. Warn the user it expires in ~1 hour.
- **ADC**: user runs `gcloud auth application-default login` with required scopes. Lowest priority, used as fallback.

### When the user asks about scopes:
- Use `google_auth` with action `set_scopes` and value = preset name or JSON array. Admin only.
- Available presets:
  - `readonly` (default) — Gmail read, Calendar read, Contacts read, Tasks full
  - `readwrite` — Gmail modify, Calendar full, Contacts read, Tasks full
  - `full` — Gmail full, Calendar full, Contacts full, Tasks full, Drive full
- Custom scopes: pass a JSON array like `["https://mail.google.com/","https://www.googleapis.com/auth/drive"]`
- After changing scopes, existing OAuth2 tokens may need re-authorization (disconnect and reconnect).
- Service accounts and ADC are not affected by scope changes (they use the scopes at token creation time).

### Credential type auto-detection:
When a JSON file is provided, the system reads the `"type"` field:
- `"service_account"` → JWT-based, no user interaction needed, typically for server-to-server
- `"authorized_user"` → refresh token flow, for personal/desktop use
- Missing `"type"` → defaults to authorized_user

### Where to get credentials (step-by-step for the user):

**Google Cloud Console (OAuth2 client for interactive flow):**
1. Go to console.cloud.google.com
2. Create or select a project
3. Enable APIs: Gmail API, Google Calendar API, People API, Tasks API
4. Go to APIs & Services → Credentials → Create Credentials → OAuth 2.0 Client ID
5. Application type: Web Application
6. Add redirect URI: the server's callback URL (e.g. `http://localhost:8080/api/google/callback`)
7. Copy Client ID and Client Secret
8. Configure in iulita: set `skills.google.client_id` and `skills.google.client_secret` (via chat `skills set_config` or dashboard Settings)

**Google Cloud Console (Service Account for server use):**
1. Go to console.cloud.google.com → IAM & Admin → Service Accounts
2. Create Service Account, give it a name
3. Grant required roles (for domain-wide delegation: admin roles)
4. Go to the service account → Keys → Add Key → Create new key → JSON
5. Download the JSON file
6. Set path: `google_auth set_credentials_file /path/to/service-account.json`
7. For accessing user data: enable domain-wide delegation in Google Workspace Admin Console → Security → API Controls → Domain-wide Delegation → Add the service account client_id with required scopes

**gcloud CLI (ADC for local development):**
1. Install gcloud CLI: cloud.google.com/sdk/docs/install
2. Run: `gcloud auth application-default login --scopes=https://www.googleapis.com/auth/gmail.readonly,https://www.googleapis.com/auth/calendar.readonly,https://www.googleapis.com/auth/contacts.readonly,https://www.googleapis.com/auth/tasks`
3. This creates `~/.config/gcloud/application_default_credentials.json`
4. No config needed — iulita picks it up automatically as ADC fallback

### If no credentials are configured:
Tell the user: "No Google credentials configured. The easiest options are:
1. For personal use: set up OAuth2 client_id + client_secret and connect your account in dashboard Settings
2. For server/automation: create a service account JSON and set credentials_file
3. For quick testing: run `gcloud auth application-default login` or set IULITA_GOOGLE_TOKEN env var"

### Config keys reference:
- `skills.google.client_id` — OAuth2 Client ID (secret, for interactive flow)
- `skills.google.client_secret` — OAuth2 Client Secret (secret, for interactive flow)
- `skills.google.redirect_url` — OAuth2 callback URL (default: auto-detected from server config)
- `skills.google.credentials_file` — path to service account or authorized user JSON file
- `skills.google.scopes` — scope preset name ("readonly", "readwrite", "full") or JSON array of URLs
