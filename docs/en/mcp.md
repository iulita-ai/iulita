# Remote MCP connections

Iulita can act as a client of remote MCP servers over HTTPS. Configure connections
in **MCP** in the dashboard. Local processes, stdio servers and a Podman MCP worker
are not enabled by the application.

## Deployment configuration

MCP execution is disabled by default. Enable it and provide an exact allowlist:

```toml
[skills.mcp]
enabled = true
allowed_origins = ["https://mcp.example.org"]
oauth_redirect = "https://assistant.example.org/api/mcp/oauth/callback"
```

Origins must be lowercase HTTPS origins without a path, query or trailing slash.
Only port 443 is supported. An empty allowlist denies remote connections.
`IULITA_SKILLS_MCP_ALLOWED_ORIGINS` accepts a JSON array of strings, for example
`["https://mcp.example.org"]`; it does not accept a comma-separated list.
`IULITA_SKILLS_MCP_ENABLED` and `IULITA_SKILLS_MCP_OAUTH_REDIRECT` override the
other settings.

Encrypted credential storage and the deployment-owned `[proxy].url` are required.
MCP traffic, including OAuth network requests, uses that proxy with no direct
fallback. DNS answers must all be public addresses. Iulita pins the numeric proxy
destination while preserving the server's TLS name and HTTP host. Private,
loopback and link-local destinations are rejected. OAuth providers may use public
origins different from the MCP server; these requests retain the same network
restrictions. Configure proxy access controls for other applications separately.

The administrator controls whether MCP execution is enabled. Connection settings,
credentials, permissions, execution results and audit views belong to the owner;
administrator status does not grant access to another owner's private connections.

Personal/work labels guide the owner and assistant; they are not automatic
content classification or data-loss prevention. Check the selected server and
arguments when approving each operation.

## Connect and use a server

1. Add a connection with its HTTPS endpoint and personal or work data domain.
   Saving a draft does not contact the remote server.
2. Configure credentials in authenticated MCP settings. Never paste tokens into
   a chat tool call. OAuth requires the public callback URL above to match the
   provider registration.
3. Connect and review the discovered tools, resources and prompts. Discovery does
   not grant permission to execute them. Review updated descriptors again when
   their revision changes.
4. Prepare an exact request and approve it. Approval is one-use and bound to the
   request, connection and descriptor revision. Chat uses the same owner-scoped
   control plane as the dashboard. Returned metadata and content are untrusted.

Disconnecting prevents further dispatch but cannot prove that work already
received by a remote server has stopped. Cancellation has the same limitation.
If an execution reports `unknown_outcome`, investigate the remote server before
repeating it: the original operation may have completed. Iulita does not
silently replay such operations. Results are private by default.

## Upgrade and rollback

Back up the application database and encryption key before upgrading. Keep the
key available when restoring the database. Existing SSH worker configuration and
mobile contracts are separate from MCP and remain required where already used.
No MCP worker service or image needs to be deployed.

For a conservative application rollback, first disable MCP execution and stop the
application, then restore a matching database backup with the previous binary.
Do not delete encrypted records or keys to troubleshoot connectivity. Preserve
execution records when investigating uncertain remote outcomes.
