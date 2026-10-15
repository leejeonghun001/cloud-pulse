# Troubleshooting

## Hub won't start

- **`CP_AGENT_TOKEN is required`**: set it in `hub.env` (>= 16 chars) —
  see [configuration.md](configuration.md).
- **Address already in use**: another process (or a previous hub
  instance that didn't shut down cleanly) already holds the configured
  `CP_LISTEN` port. `sudo systemctl status cloud-pulse-hub` (Linux) to
  check for a stuck old process; check the Network settings page for a
  stale pending-change listener if you recently edited listen config.
- **Started but the dashboard is blank or slow**: use the browser's
  Network panel to separate a failed API response from an asset/render
  failure; then check Console for JavaScript or CSP errors. The embedded
  app loads its own assets (no runtime CDN); extensions or proxies that
  inject scripts can violate `script-src 'self'`.

## No hosts or the dashboard looks empty

- A new data directory starts with no host samples. A host appears after
  its agent sends a valid report; `cloud-pulse-agent -once` only collects
  and prints a local sample and does **not** contact the hub.
- Check that the agent service is running, its `CP_HUB_URL` points at the
  hub listener, and its `CP_AGENT_TOKEN` matches the hub. Check the agent
  log for report failures. The dashboard host/list endpoints use a
  dashboard session or optional `CP_UI_TOKEN`, not the agent token.
- `GET /healthz` is public liveness only; HTTP 200 does not prove that a
  report was accepted or that the dashboard has data. See the API
  authentication classes in [api.md](api.md#authentication).

## Agent can't reach the hub

- Confirm `CP_HUB_URL` matches the hub's actual listen address and that
  the agent's IP is inside `CP_ALLOWED_CIDRS` (the default allowlist is
  Tailscale/loopback ranges only — a non-Tailscale deployment must
  widen this explicitly, see [security.md](security.md)).
- First check `GET /healthz` at the same host/port from the agent
  network. It has no auth and checks liveness only; it does not prove the
  agent route or token works.
- Agent `POST /api/v1/agent/report` and `GET /api/v1/agent/time` require
  the hub's `CP_AGENT_TOKEN` as a Bearer token. A mismatch returns 401;
  a dashboard session or `CP_UI_TOKEN` is not interchangeable. The
  allowlist checks the socket peer address, not `X-Forwarded-For`.
- Run `cloud-pulse-agent -once` to collect and print one sample locally
  without needing hub connectivity at all — isolates whether the
  problem is collection or networking.

## Host data is stale or marked down

- Compare the last successful report with the agent's `CP_INTERVAL`
  (default `15s`) and the hub's `CP_OFFLINE_AFTER` (default `60s`). A
  down state means the hub has not received a successful report within
  that configured window; a running process alone is not a successful
  report.
- Check the agent log for a 401 (token/auth), 403 (allowlist), connection
  failure, or 400 (invalid report), and check the hub log for ingest
  errors. `GET /api/v1/hosts` requires dashboard auth; do not test it with
  `CP_AGENT_TOKEN`.

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

## Dashboard/API authentication errors

- **401 on an agent route**: use the configured `CP_AGENT_TOKEN` Bearer
  credential. **401 on a dashboard API**: sign in for a dashboard session,
  or use optional `CP_UI_TOKEN` for an API client. These credentials have
  different purposes; do not substitute one for the other. The UI token
  grants full API admin access but does not sign in the browser dashboard.
- **403 `password_change_required`**: a normal session still requires a
  password change. Complete the first-login flow; until then only
  `/api/v1/auth/me`, `/api/v1/auth/password`, and `/api/v1/auth/logout`
  are available to that session. See
  [security.md](security.md#sign-in-and-accounts).

## Network path differs on mobile or another client

- From the affected device, check the same hub hostname, port, scheme,
  and VPN/Tailscale route used by the browser. Request `/healthz`; if it
  cannot connect, check listener binding, DNS, firewall, and
  `CP_ALLOWED_CIDRS` before investigating rendering.
- If `/healthz` works but only that browser is blank/slow, inspect its
  Network and Console panels: distinguish `/assets/...` failures, API
  status codes, and client-side errors. Compare another browser on the
  same device/network; a desktop success does not prove the phone uses
  the same DNS, VPN, or source address.
- `/healthz` is not an authenticated data/API check. Do not share bearer
  tokens, reset links, or unredacted request headers in a report.

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

## A channel shows "Failing" on the Health tab

Settings → Notifications → Health (`#/settings/notifications?tab=health`)
marks a channel failing when its latest weekly test failed or fewer
than 50% of its deliveries in the last 7 days succeeded — see
[notifications.md](notifications.md#health-report) for the thresholds.
Open the channel's diagnosis card (or press **Send test** on the
Channels tab) for the classified reason, fix the channel, then press
**Run now** to re-test. The "channel X is failing" alert goes out
through another healthy channel; when there is none, the dashboard
shows a banner instead.

## Alerts arrive in the wrong language

Notification text follows the hub-wide Settings → General →
Notification language, not your dashboard language. Text inside chart
images is always English.

## `/login` or a bookmarked page returns 404

Hubs older than v0.8.0 only serve the dashboard at `/`; use
`http://<hub>:8090/#/login` there, or upgrade. On v0.8.0+, a path like
`/login` or `/settings/network` opens the dashboard, while missing
files under `/assets/` and unknown `/api/` paths still return 404.

## Remote agent update stuck or failing

Check the failure reason shown on the Rollout page (select the batch
in its batch history) against the table in
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
