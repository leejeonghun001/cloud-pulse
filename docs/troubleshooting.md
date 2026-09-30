# Troubleshooting

## Hub won't start

- **`CP_AGENT_TOKEN is required`**: set it in `hub.env` (>= 16 chars) —
  see [configuration.md](configuration.md).
- **Address already in use**: another process (or a previous hub
  instance that didn't shut down cleanly) already holds the configured
  `CP_LISTEN` port. `sudo systemctl status cloud-pulse-hub` (Linux) to
  check for a stuck old process; check the Network settings page for a
  stale pending-change listener if you recently edited listen config.
- **Started but the dashboard shows a blank page**: check the
  browser's console for a CSP violation — the dashboard requires
  `script-src 'self'` and no inline scripts; a browser extension or
  proxy injecting scripts can trip this.

## Agent can't reach the hub

- Confirm `CP_HUB_URL` matches the hub's actual listen address and that
  the agent's IP is inside `CP_ALLOWED_CIDRS` (the default allowlist is
  Tailscale/loopback ranges only — a non-Tailscale deployment must
  widen this explicitly, see [security.md](security.md)).
- Confirm `CP_AGENT_TOKEN` matches the hub's token exactly (a
  mismatched token returns `401` from `POST /api/v1/agent/report`, not
  a connection-level error).
- Run `cloud-pulse-agent -once` to collect and print one sample locally
  without needing hub connectivity at all — isolates whether the
  problem is collection or networking.

## Host shows "down" but the agent process is running

- Check the agent's own logs for report failures (network, auth, or a
  400 from a malformed sample).
- `CP_OFFLINE_AFTER` (default `60s`) is how long since the last
  successful report before the hub marks a host offline — a longer
  `CP_INTERVAL` than that margin will cause spurious offline flapping.

## Locked out of the dashboard

- **Forgot the admin password**: `sudo cloud-pulse-hub reset-password`
  from the hub host — see
  [security.md](security.md#recovery).
- **Locked out by a Network settings change**: `sudo
  cloud-pulse-hub reset-network` from the hub host, then restart the
  service — see [security.md](security.md#network-settings). Note that
  the Network settings page's own lock-out/bind-failure/pending/
  auto-revert guards are specifically designed to make this scenario
  rare; reaching it usually means a session expired mid-change or the
  hub was restarted while an untested config was still pending.

## Billing shows "not connected"

`CP_BILLING=auto` silently skips a provider whose CLI isn't installed,
isn't authenticated, or lacks the required IAM/policy permission — this
is by design, not an error. See [billing.md](billing.md#quiet-skip-status-model)
for the full status table, and check the hub's own log at `slog.Info`
level (logged at most once per day per provider) for the classified
reason.

## Storage account stuck on "pending_oauth"

- **Google Drive**: the device-flow code expires after Google's own
  TTL (typically ~15–30 minutes) if never entered — delete the account
  and reconnect to get a fresh code.
- **Dropbox**: the PKCE authorize link's code must be pasted back
  within the same flow session; if you navigated away or the hub
  restarted mid-flow, delete the account and reconnect.

## Notifications aren't arriving

Use the Settings → Notifications page's **Send test** button first —
it reports a classified diagnosis (`discord_webhook_not_found`,
`telegram_chat_not_found`, `whatsapp_window_closed`, etc.) rather than
a bare error. See [notifications.md](notifications.md#send-test-diagnosis)
for the full code table, and
[notifications.md](notifications.md#real-account-end-to-end-verification)
for `cloud-pulse-hub notify verify` as an independent, scriptable check
outside the browser.

## Remote agent update stuck or failing

Check the failure reason shown on the Updates page against the table in
[remote-updates.md](remote-updates.md#statuses-and-failure-reasons) —
most commonly `not_enabled` (the agent hasn't set
`CP_REMOTE_UPDATE=on`) or `unsupported` (OS/version below that
platform's gate). Both show the manual `update` command as a fallback.

## CONTRIBUTING commands fail on a fresh clone

Run `bash scripts/test-contributing.sh` yourself — it parses and
executes every `<!-- contributing:run -->` block from `CONTRIBUTING.md`
in order against a fresh clone with an empty module/package cache, the
same way CI's `docs` job does. If a command that works on your existing
checkout fails there, the likely cause is a cached dependency or
generated file your checkout already has that a fresh clone doesn't.

## Still stuck?

Open an issue with `cloud-pulse-hub -version` / `cloud-pulse-agent
-version` output, your OS/arch, and relevant log lines (with any
tokens/URLs redacted) — see the issue template for the full checklist.
