# Native SSH worker

Build locally with `go build ./cmd/ssh-worker` or `docker build -f Dockerfile.ssh-worker .`.
The bot never falls back to in-process SSH or the legacy `shell_exec` tool.
Configure `[skills.remote_access]` with `worker_url` and `worker_key_file`.
The feature defaults off; an administrator must enable it in Remote Access.

Run the worker as a separate non-root user/container without the bot data volume,
environment, keyring, service-account token, Docker socket, or host namespaces.
Use a read-only root filesystem, no capabilities, no-new-privileges, memory/CPU/PID
limits, disabled core dumps, disabled or encrypted swap and a private writable
persistent directory for nonce claims. Set the PID limit in the container runtime
or cluster kubelet policy; the Kubernetes Pod API has no per-pod PID limit field.
Never share a worker/protocol key across mutually untrusted installations.

Generate at least 32 cryptographically random protocol-key bytes, save mode 0600,
and provision the same bytes through private read-only files to bot and worker.
Values never belong in configuration examples, arguments, logs or chat. Files
must be readable by the configured non-root UID; Kubernetes Secret mounts need
UID/GID ownership preparation compatible with the strict private-file check.
Use TLS with a certificate trusted by the bot, or a mode-0600 Unix socket in a
private shared directory. Plain HTTP and redirects are rejected. Do not expose
or proxy the Unix socket to other users. The worker cannot issue vault requests.

`worker.example.yaml` is a reviewable Kubernetes example, not an applied deployment.
Replace documentation-only destination IPs, image, certificate and namespace
configuration first. It uses numeric server addresses and denies DNS egress.
If DNS is needed, add only the trusted resolver and ensure *all* resolved addresses
are within the deployment-owned `--allow-cidrs`. Each downstream host is resolved
by the worker and passed numerically to the bastion. Bastion servers must also
restrict `PermitOpen`/egress to approved downstream addresses: worker NetworkPolicy
cannot constrain connections originating on a remote bastion. NetworkPolicy
requires an enforcing CNI; verify actual denied connections before enabling use.

The worker protocol authenticates bounded requests with HMAC and claims each
nonce durably before SSH execution. Never erase its nonce state during restart;
claims older than 24 hours are pruned (initial dispatch must start within 5 seconds).
The controller renews a signed 5-second worker lease once per second (up to
1 additional second of accepted clock skew); loss of
renewals closes the SSH transport. A stalled worker can still race cancellation
before dispatch; the remote SSH server does not enforce fencing tokens.
Controller requests are never retried. HTTP loss after sending a request means
`unknown_outcome`. Disconnect/cancel cannot guarantee a remote process stopped.

Remote Access setup: add verified public host keys and the independent trust
source; add/import or generate an identity; create an ordered route (final target
last, up to three jump servers); grant exact route revision/caller/capability with
expiry. The final server may use a different login/key than every jump server.
Interactive commands require human consent. Delegates and schedules can execute
only explicitly granted, revision-pinned runbooks with finite parameters, expiry
and a finite use budget. Scheduled runbooks dispatch directly without an LLM and
claim each firing durably before execution. Agent forwarding, arbitrary port
forwarding and external signers are unavailable. Ordinary shell commands
can use `sudo -n` only to the extent granted by the remote account's sudoers.
There is no safe claim that arbitrary shell is read-only or cannot become root.

Key lifecycle needs Python 3 on Linux/POSIX servers. Grant `key_install` separately
for install/independent login verification and `key_remove` for removal. Approve
the exact generated script in the Operations panel. Installation preserves other
entries, rejects unsafe symlinks/permissions and writes a managed marker. Create
a new route using the new identity, verify login, then remove the old key only
using a recently verified alternate managed identity. Disabling an identity in
Iulita does not remove its public key from servers. Inventory remains for remote
revocation and reconciliation. Private keys are never exported.

Raw output is encrypted for 24 hours and can be viewed only by its authenticated
owner; tools return metadata/artifact IDs without stdout/stderr. No output is
sent to the model automatically. A single controller owns its private atomic JSON
catalog using an exclusive process lock; do not use shared multi-writer storage
or scale the controller horizontally for this feature. Catalog and protocol-key
backups need separate protection; recover the matching encryption key from the
existing Iulita key-management process. A restarted controller marks potentially
dispatched executions unknown and cancels pending approvals without replay.

## Files, background jobs and terminal

The Advanced panel provides native SFTP download/stat/upload, reviewed runbooks,
runbook schedules, managed background jobs and a human-only terminal. These share
route, owner, identity, grant and disable controls. Disabled accounts cannot start
SSH operations; active service checks observe account revocation within five seconds.

SFTP requires a separately granted absolute private root (0700), with no writable
ancestors except sticky directories. Ancestors must be owned by root or the private
root owner; missing ownership/mode metadata is rejected. Paths, symlinks, size and SHA-256 are checked.
Transfers are limited to 10 MiB. Upload uses the OpenSSH hardlink extension for
atomic publication without overwriting existing files; unsupported servers fail
closed. SFTP v3 cannot fully prevent same-account filesystem races: use trusted
private directories, not directories concurrently controlled by hostile processes.
Downloads remain private artifacts and are not sent to the model.

Managed background jobs require explicit helper installation on a Linux server
with Python 3 and procfs. Durable job IDs prevent automatic replay. The helper
tracks the original process group and PID start time, caps output at 10 MiB and
lifetime at 24 hours. Status/cancel remain available when normal access is disabled,
with an appropriate recovery grant and confirmation. A process deliberately escaping
its group is outside this mechanism; it is not a cgroup sandbox. Remote job records
are retained; operators must manage their lifecycle and the private helper directory.

PTY uses a short-lived single-use browser ticket, exact Origin validation and
account/security-epoch checks. No bearer token or credentials appear in the URL.
The terminal is human-only: output is not placed in model context or chat history.
There is no automatic reconnect or replay. Sessions are limited to one hour,
15 minutes idle and 10 MiB output, with bounded input/resize rates. Closing the
connection does not prove a remote process stopped; uncertain outcomes stay unknown.

Runbooks with parameters intentionally support one simple command with finite
argument choices, not arbitrary shell templates. Parameter-free runbooks may hold
a fixed script. Background starts and arbitrary interactive commands still require
main-loop human consent; unattended callers receive only their exact runbook grants.

Controller execution history rolls over finished records (1000 per owner); active
intents are retained. Scheduler claims compact behind a durable monotonic queue-task
watermark. Old or excessively delayed tasks at/below that watermark are denied
permanently rather than replayed. Restore the controller catalog together with its
matching scheduler database; resetting/reusing task IDs is not supported.
