# Security

## Sign-in and accounts

The dashboard always requires signing in. The hub has a single account,
username `admin`.

- **First run**: the hub bootstraps the admin account with the
  well-known default password `changeme` and marks it as needing a
  change. Every startup while that flag is still set, the hub logs a
  warning reminding the operator to sign in and change it (escalated
  to an error-level warning if `CP_ALLOWED_CIDRS` also allows every
  address).
- **First login**: signing in with `admin`/`changeme` succeeds but the
  dashboard immediately shows a non-dismissable "set a new password"
  dialog; every other page and API route is blocked (`403
  password_change_required`) until the password is changed. Only `GET
  /api/v1/auth/me`, `POST /api/v1/auth/password`, and `POST
  /api/v1/auth/logout` are reachable in that state.
- **Password policy**: 8–1024 bytes, not all whitespace, not the
  literal string `changeme`, and (when changing an existing password)
  different from the current password. A violation returns `400` with
  code `weak_password`.
- **Password storage**: PBKDF2-HMAC-SHA256 (Go standard library
  `crypto/pbkdf2`), 600,000 iterations, a fresh random 16-byte salt per
  hash, 32-byte derived key — stored as
  `pbkdf2-sha256$<iterations>$<salt>$<hash>` (base64, no padding).
  Verification always runs the full PBKDF2 computation — including for
  an unknown username or a malformed/missing stored hash — so there is
  no timing signal distinguishing "wrong username" from "wrong password
  for a real username."
- **Sessions**: a successful login issues a 32-byte random bearer token
  (base64url, returned once); the hub stores only `sha256(token)`,
  never the token itself. Sessions slide forward 7 days on activity, up
  to an absolute 30-day maximum from creation. `GET
  /api/v1/auth/sessions` lists active sessions; `POST
  /api/v1/auth/sessions/revoke-others` signs out every other session.
- **Rate limiting**: 5 failures from the same client IP (host only,
  port stripped) within 15 minutes triggers a lockout, starting at 1
  minute and doubling on each further lockout up to a 15-minute cap; a
  successful login clears that IP's state. Separately, more than 30
  failures per minute across every client triggers a 60-second
  fleet-wide lockout. Either lockout responds `429` with code
  `rate_limited` and a `Retry-After` header. At most 2 password hashes
  compute concurrently; a request that can't get a slot within 5
  seconds also gets `429`.
- **`CP_UI_TOKEN`**: an optional static bearer token for scripts (agents
  don't use it — they use `CP_AGENT_TOKEN`). When set, a request
  bearing it is authenticated as `api_token`: full read/admin access,
  never subject to the must-change-password gate.

### Recovery

`sudo cloud-pulse-hub reset-password [--data-dir DIR]
[--password-stdin]` resets the admin password without needing to sign
in first — by default back to `changeme` (forcing a change on next
login), or with `--password-stdin`, to a password piped in on stdin.
Either way, every existing session is revoked. It works correctly even
while the hub is running (SQLite WAL + busy-timeout).

## Network settings

The dashboard's Settings → Network page manages the hub's own listen
address(es) and access allowlist as hub-managed state (persisted in
SQLite), instead of requiring an edit to `hub.env` and a restart:

- **Adapters**: lists every network interface via `net.Interfaces()`,
  classified `loopback`/`tailscale`/`virtual`/`physical`, each address
  carrying a `suggested_cidr` for one-click allowlist shortcuts.
- **Listen selection**: choose "all interfaces" or specific addresses
  plus a port (1024–65535). Applying a change opens new listener(s)
  before closing removed ones, and a removed listener's socket stays
  open a further second so any in-flight response has time to flush.
- **Access allowlist**: the same CIDR/IP list `CP_ALLOWED_CIDRS`
  represents, editable live; a swap of the effective list is atomic.
- **A Network settings change can never lock out the admin making it**:
  - A new allowlist that would exclude the requesting client's own IP
    is rejected outright: `409 would_lock_out`.
  - A new listen configuration where no address ends up
    `listening`/`waiting` is rolled back automatically: `409
    bind_failed`.
  - A change that binds successfully but would stop serving the
    requesting client's own connection is treated as **pending**: the
    hub keeps serving the old configuration too, returns candidate URLs
    to verify the new one from, and starts a 120-second deadline.
    `POST .../confirm` persists it; `POST .../revert` (or doing nothing
    until the deadline) reverts automatically.
  - `DELETE /api/v1/settings/network` drops the hub-side override,
    reverting to `CP_LISTEN`/`CP_ALLOWED_CIDRS`.
- **`sudo cloud-pulse-hub reset-network [--data-dir DIR]`**: the offline
  recovery path if an admin locks themselves out anyway — deletes the
  persisted override so the next restart falls back to the env config.

## Security model

- **Agent authentication**: bearer token compared in constant time
  (SHA-256 both operands, then `crypto/subtle.ConstantTimeCompare`).
- **Network access control**: an IP allowlist checked against
  `http.Request.RemoteAddr` only; `X-Forwarded-For` and other
  client-supplied headers are never trusted.
- **Must-change-password gate**: a session whose account still has the
  default (or otherwise flagged) password can reach only
  `/api/v1/auth/me`, `/api/v1/auth/password`, and `/api/v1/auth/logout`.
- **Well-known default password, by design, with mitigations**: a
  fresh hub always bootstraps `admin`/`changeme` so first-run setup
  never requires an out-of-band secret exchange. Mitigations: the
  must-change gate forces a password change on the very first login;
  the hub logs an escalating warning while the default is still in
  effect; and the default allowlist restricts access to
  Tailscale/loopback ranges only.
- **Security headers** on every response: CSP (`script-src 'self'`, no
  inline scripts, `frame-ancestors 'none'`), `X-Content-Type-Options:
  nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`.
- **Hardened service definitions**: `NoNewPrivileges`,
  `ProtectSystem=strict`, `ProtectHome=read-only`, `PrivateTmp`,
  `PrivateDevices`, empty capability set, and a
  `RestrictAddressFamilies` list scoped to what each binary actually
  needs (the hub's systemd unit additionally allows `AF_NETLINK`,
  required by `net.Interfaces()` for the Network settings page).
  macOS's LaunchDaemon and Windows's service registration apply the
  equivalent least-privilege posture available on those platforms (a
  dedicated `_cloudpulse` user; a virtual service account with no
  interactive-logon rights).
- **No secrets logged**: tokens, webhook URLs, and passwords are logged
  only as `(set)`/`(not set)` or never at all.
- **Notify-channel and storage-account secrets stored in the hub's
  SQLite database, not separately encrypted**: a channel's
  `bot_token`/`access_token`/`webhook_url`, or a storage account's OAuth
  client secret/refresh token, is persisted as plain JSON inside the
  hub's own database file — there is no separate secrets store or
  at-rest encryption layer. Confidentiality rests entirely on
  **filesystem permissions**: `CP_DATA_DIR` is created (if missing)
  with mode `0750`, and the installed hub runs as an unprivileged
  system user, so only that user (and root) can read `cloud-pulse.db`
  on a stock install.
- **Secret redaction is applied only at the API boundary, never at
  rest**: every list/get/create/update response redacts a channel's or
  storage account's secret fields before serialization — the
  underlying store methods always return the unredacted row, and every
  handler is responsible for calling the redaction helper itself.
- **SSRF guard on Discord/Telegram/WhatsApp notify channels and on
  Google Drive/Dropbox storage-provider requests**: each validates its
  endpoint against a fixed allowlist of official hosts and requires
  `https://`. `CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS=1` /
  `CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS=1` relax this — **tests/CI fakes
  only**, never production.
- **Cloud-billing CLI child processes run sandboxed, never with a
  shell**: `aws`/`oci` are invoked via a fixed argument array, a
  60-second timeout, output capped at 4 MiB, and a minimal explicit
  environment. See [billing.md](billing.md).
- **Remote agent updates keep the hub in a request-only role**: a
  compromised hub can move an opted-in agent between official release
  tags of that agent's own already-configured release feed, but cannot
  supply its own binary URL or checksum. See
  [remote-updates.md](remote-updates.md#security-model).
- **Privileged-helper file handling**: the agent-side update-request
  and result files use platform-appropriate no-follow-symlink reads
  (`O_NOFOLLOW` on Unix, `FILE_FLAG_OPEN_REPARSE_POINT` +
  `FILE_ATTRIBUTE_REPARSE_POINT` rejection on Windows) and a
  privileged-owned result directory the unprivileged agent process
  cannot write to.
- **Audit log**: every settings/pricing/currency/billing-interval/
  storage-account/update-batch change that mutates admin-facing state
  is recorded to an `audit_log` table with the actor, requesting IP,
  action, and full before/after values — mirrored to `slog.Info` with
  no secret values in either. `GET
  /api/v1/audit?entity_type=&limit=&before=` (admin) serves the log.

## Reporting a vulnerability

See [../SECURITY.md](../SECURITY.md) for how to report a suspected
vulnerability privately.
