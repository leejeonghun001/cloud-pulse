# cloud-pulse

Ultra-lightweight, CGO-free server monitor for small fleets — a
Beszel-inspired hub + agent, one static binary each, no runtime
dependencies, an embedded SQLite database, and a dashboard baked into the
binary.

[![CI](https://github.com/leejeonghun001/cloud-pulse/actions/workflows/ci.yml/badge.svg)](https://github.com/leejeonghun001/cloud-pulse/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/leejeonghun001/cloud-pulse)](https://github.com/leejeonghun001/cloud-pulse/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25%2B-00ADD8)](go.mod)

![cloud-pulse fleet overview](docs/screenshots/overview.png)

More screenshots: [host detail](docs/screenshots/host-detail.png) ·
[mobile](docs/screenshots/mobile.png) · [settings](docs/screenshots/settings.png)

## Features

- **15-second host metrics**: CPU, memory (true "available", not naive
  free), disk usage + IO, network rates, load average — pushed by a
  single-binary agent over HTTP(S).
- **Outbound and inbound egress accounting**, tracked and alerted on
  separately: outbound keeps provider-aware defaults (AWS 100 GB, OCI
  10 TB) with alerts at 80/95/100% of the limit; inbound gets its own
  optional limit (default unlimited) with the same threshold/alert
  behavior. Both are overridable **per host from the hub** (persisted in
  SQLite), taking precedence over the agent-reported/provider default.
- **Hub-synchronized, aligned sampling**: agents estimate their clock
  offset from the hub (NTP-style) and stamp every sample with the same
  hub-time interval boundary, so a whole fleet's samples share identical
  timestamps regardless of each host's own clock drift.
- **Dashboard sign-in**: a single `admin` account (bootstrapped with a
  default password on first run, forced change on first login), server-side
  sessions, per-IP + fleet-wide login rate limiting, and a `reset-password`
  recovery CLI — see [Sign-in and accounts](#sign-in-and-accounts).
  `CP_UI_TOKEN` is now an **optional** static bearer token for scripts only;
  the dashboard itself always requires signing in.
- **Settings UI** (`#/settings`) to reveal the agent token and copy a
  ready-to-run install command, edit per-host egress/ingress limit
  overrides, manage the alert webhook URL, and configure which network
  interfaces/IPs the hub listens on and who may reach it — see
  [Network settings](#network-settings).
- **Cloud object storage monitoring**: Amazon S3 via CloudWatch, Cloudflare
  R2 via GraphQL Analytics, collected every 15 minutes by default.
- **Alerting with chart images to Discord, Telegram, and WhatsApp**:
  configurable rules (CPU/memory/disk/load average sustained above a
  threshold for N minutes, outbound/inbound egress above X% of its
  limit, host offline) fire through a persistent state machine
  (ok → pending → firing → resolved) and deliver a rendered PNG chart
  alongside the notification — see [Alerting](#alerting).
- **Inventory: Docker containers + listening ports**: the agent reports
  every TCP/UDP listening socket (with best-effort process name) and, on
  hosts running Docker or Podman, every container's name, image, state,
  health, and published ports — shown on each host's detail page. See
  [Inventory](#inventory-docker-services--listening-ports).
- **Single static binaries**, `CGO_ENABLED=0` always, no AWS SDK (hand-written
  SigV4), embedded SQLite (`modernc.org/sqlite`) with 15s/5m/1h rollups and
  automatic retention pruning.
- **Embedded dashboard** — vanilla ES modules + vendored uPlot charts +
  prebuilt Tailwind CSS, no Node.js needed to run the hub, no CDN calls at
  runtime.

## Architecture

```
                    Tailscale HTTPS/HTTP + bearer PSK
 ┌──────────┐   push    ┌──────────────────────────────┐   pulls    ┌─────────────┐
 │  agent A │──────────▶│                              │───────────▶│  CloudWatch │
 ├──────────┤   push    │             hub              │            │   (S3)      │
 │  agent B │──────────▶│  REST API · SQLite · collec-  │            └─────────────┘
 ├──────────┤   push    │  tors · embedded dashboard    │───────────▶┌─────────────┐
 │  agent N │──────────▶│                              │            │  Cloudflare │
 └──────────┘           └──────────────────────────────┘            │  GraphQL(R2)│
                                       │ serves                     └─────────────┘
                                       ▼
                                  ┌─────────┐
                                  │ browser │
                                  └─────────┘
```

Agents push metrics to the hub over HTTP(S) authenticated with a
pre-shared bearer token (`CP_AGENT_TOKEN`) — no inbound access to agents
is required, so it works behind NAT/CGNAT. The hub separately polls AWS
CloudWatch and Cloudflare's GraphQL Analytics API on a timer for object
storage stats, and serves both a JSON REST API and its own embedded
dashboard to the browser.

## Quick start

Install the hub (as root or via `sudo`):

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-hub.sh \
  | sudo bash
```

**Run at a real terminal with no flags**, this shows an interactive menu
(all prompts read from `/dev/tty`, never from stdin — the same command
works correctly even though stdin is the piped script itself):

```
cloud-pulse hub installer
  Status: not installed
  1) Install      (설치)
  2) Reinstall    (재설치: latest version, keeps settings, tokens and data)
  3) Uninstall    (삭제)
  0) Exit
Select [1-3, 0]:
```

- **1) Install** prompts for the listen port, whether to enable the web
  Settings page (generates `CP_UI_TOKEN`), and an optional alert webhook
  URL, then installs.
- **2) Reinstall** is the upgrade/migration path for an existing install
  of any version (see [Upgrading](#upgrading)): shows current → target
  version, confirms, keeps every existing token/setting/data file, and
  offers to enable the Settings page if it isn't already.
- **3) Uninstall** confirms, then asks separately whether to also purge
  config/tokens/data.

**Piping any flag, or running with no tty** (CI, `curl | bash -s --
<flag>`, cron) skips the menu entirely and never blocks — this is the
scriptable/automation path:

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-hub.sh \
  | sudo bash -s -- --install --yes
```

Non-interactive action flags: `--install` (fails if already installed),
`--reinstall` (fails if not installed), `--uninstall` (`--purge` to also
remove config/data), and `-y`/`--yes` (assume default confirmation
answers; never blocks on a prompt). With no action flag at all, behavior
is the historical "auto" mode: install if not installed, reinstall if
already installed.

Either path downloads the correct `cloud-pulse-hub` release binary for
your OS/arch, verifies its sha256 against the release's `checksums.txt`,
installs it under `/usr/local/bin`, creates an unprivileged `cloud-pulse`
system user, generates `CP_AGENT_TOKEN`, writes `/etc/cloud-pulse/hub.env`
(mode `0640`), and installs+starts a hardened systemd unit. At the end it
prints an agent one-liner with the hub's Tailscale/LAN IP and the agent
token filled in, plus the web UI token if one was generated **this run**
(shown once — see [Settings UI](#settings-ui)):

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh \
  | sudo bash -s -- --hub-url http://<hub-ip>:8090 --token <printed-token>
```

- **Upgrade**: choose **2) Reinstall** from the menu, or pass
  `--reinstall` non-interactively; the binary and systemd unit are
  replaced, `hub.env`/`agent.env` secrets are preserved unless you pass an
  explicit flag to change them. See [Upgrading](#upgrading) for the full
  migration story and why, past v0.3.1, you generally won't need to
  re-run the installer again at all.
- **Uninstall**: choose **3) Uninstall** from the menu, or pass
  `--uninstall` (stops/disables the service, removes the binary and
  unit; config and data are kept). Add `--purge` as well to also remove
  `/etc/cloud-pulse/*.env` and (hub only) the data directory.
- **Dry run**: add `--dry-run` to any invocation to print the actions that
  would be taken without changing anything.
- Run either script with `--help` for the full flag list.

## Tailscale setup

cloud-pulse is designed to run on a [Tailscale](https://tailscale.com)
tailnet: agents and the browser reach the hub over Tailscale's private
mesh, so the hub's HTTP API never needs to be exposed on the public
internet.

1. Install Tailscale on the hub host and every agent host, and log them
   into the same tailnet.
2. On the hub, find its Tailscale IP: `tailscale ip -4`.
3. Point agents at that IP with `--hub-url http://<tailscale-ip>:8090`.
4. The hub binds to all interfaces (`CP_LISTEN=":8090"` by default) but
   enforces an **IP allowlist** on every request's `RemoteAddr`
   (`X-Forwarded-For` and other client-supplied headers are never
   trusted). The default allowlist (`CP_ALLOWED_CIDRS`) is:
   - `100.64.0.0/10` — Tailscale's CGNAT range
   - `fd7a:115c:a1e0::/48` — Tailscale's IPv6 ULA range
   - `127.0.0.0/8`, `::1/128` — loopback
5. Optionally run `tailscale serve https / http://127.0.0.1:8090` on the
   hub to get a browser-trusted HTTPS URL for the dashboard without
   opening any port beyond the tailnet.
6. The dashboard itself always requires signing in (see
   [Sign-in and accounts](#sign-in-and-accounts)) regardless of the
   allowlist. If you widen `CP_ALLOWED_CIDRS` (e.g. to `*` for a
   non-Tailscale deployment), the hub logs an escalated warning at
   startup for as long as the admin account still has the default
   password, since that combination means `changeme` is reachable from
   anywhere the hub is exposed — change the password immediately after
   first sign-in. `CP_UI_TOKEN` remains available as an **optional**
   static bearer token for scripts (agents, curl, CI) that need API
   access without going through the sign-in flow; it never bypasses the
   dashboard's own login.
7. Both the listen address and the allowlist can also be managed from
   the dashboard itself (Settings → Network) instead of editing
   `hub.env` — see [Network settings](#network-settings).

## Sign-in and accounts

The dashboard always requires signing in — there is no more "no
`CP_UI_TOKEN` ⇒ open read API" mode. The hub has a single account,
username `admin`.

- **First run**: the hub bootstraps the admin account with the
  well-known default password `changeme` and marks it as needing a
  change. Every startup while that flag is still set, the hub logs a
  warning reminding the operator to sign in and change it (escalated to
  an error-level warning if `CP_ALLOWED_CIDRS` also allows every
  address — see [Tailscale setup](#tailscale-setup) point 6).
- **First login**: signing in with `admin` / `changeme` succeeds but the
  dashboard immediately shows a non-dismissable "set a new password"
  dialog; every other page and API route is blocked (`403
  password_change_required`) until the password is changed. The only
  routes reachable in that state are `GET /api/v1/auth/me`, `POST
  /api/v1/auth/password`, and `POST /api/v1/auth/logout`.
- **Password policy**: 8–1024 bytes, not all whitespace, not the literal
  string `changeme`, and (when changing an existing password) different
  from the current password. A violation returns `400` with code
  `weak_password`.
- **Password storage**: PBKDF2-HMAC-SHA256 (Go standard library
  `crypto/pbkdf2`), 600,000 iterations, a fresh random 16-byte salt per
  hash, 32-byte derived key — stored as
  `pbkdf2-sha256$<iterations>$<salt>$<hash>` (base64, no padding) in the
  `auth_password_hash` setting row. Measured hash time on this project's
  target hardware: ~186 ms on arm64 (Raspberry Pi 5), ~1.05 s on armv7.
  Verification always runs the full PBKDF2 computation — including for
  an unknown username or a malformed/missing stored hash — so a wrong
  username and a wrong password for a real username take the same time,
  and there is no fast-reject timing signal to distinguish them.
- **Sessions**: a successful login issues a 32-byte random bearer token
  (base64url, returned once); the hub stores only `sha256(token)` in a
  `sessions` table, never the token itself. Sessions slide forward 7
  days on activity, up to an absolute 30-day maximum from creation
  (whichever comes first); activity touches (updates) the stored expiry
  at most once per minute per session to bound write load. `GET
  /api/v1/auth/sessions` lists active sessions (creation/last-seen/
  expiry/remote address/user agent, with the caller's own session
  flagged); `POST /api/v1/auth/sessions/revoke-others` signs out every
  other session (e.g. after a password change, or if a token is
  suspected leaked).
- **Rate limiting** (`POST /api/v1/auth/login` and `POST
  /api/v1/auth/password`, both unauthenticated-attempt-prone routes):
  5 failures from the same client IP (`RemoteAddr` host only, port
  stripped — an IP:port pair is different for every TCP connection even
  from the same client, so the limiter must key on the host alone to
  ever accumulate failures) within 15 minutes triggers a lockout,
  starting at 1 minute and doubling on each further lockout up to a
  15-minute cap; a successful login clears that IP's state. Separately,
  more than 30 failures per minute across every client triggers a
  60-second fleet-wide lockout on all login attempts. Either lockout
  responds `429` with code `rate_limited`, a `Retry-After` header, and a
  matching `retry_after_seconds` field. At most 2 password hashes are
  computed concurrently (a semaphore bounds PBKDF2 CPU usage under a
  login burst); a request that can't get a slot within 5 seconds also
  gets `429`. Failed logins are logged at `slog.Warn` with the remote
  address only — passwords and tokens are never logged.
- **`CP_UI_TOKEN` today**: an **optional** static bearer token for
  scripts (agents don't use it — they use `CP_AGENT_TOKEN`; this is for
  curl/CI access to the read API). When set, a request bearing it is
  authenticated as `api_token`: full read/admin access, and — unlike a
  session — never subject to the must-change-password gate. The
  dashboard's own login flow no longer reads or writes it at all.
- **Recovery**: `sudo cloud-pulse-hub reset-password [--data-dir DIR]
  [--password-stdin]` resets the admin password without needing to sign
  in first — by default back to `changeme` (forcing a change on next
  login), or with `--password-stdin`, to a password piped in on stdin
  (policy-checked, does not force a further change). Either way, every
  existing session is revoked. It works correctly even while the hub is
  running (SQLite WAL + busy-timeout allow the CLI and the live hub
  process to write concurrently), resolving its data directory with the
  same precedence as the hub itself (explicit flag > `CP_DATA_DIR` >
  `/var/lib/cloud-pulse` if present > `./data`), and — when run as root —
  restores ownership of the database file (and any `-wal`/`-shm`
  sidecars) to the data directory's owner afterward.



The hub polls Amazon CloudWatch directly (no AWS SDK; a hand-written SigV4
signer) — it never touches your bucket's object data, only metrics.

**IAM policy** (attach to the IAM user/role whose keys you configure):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "cloudwatch:GetMetricData",
      "Resource": "*"
    }
  ]
}
```

**Environment variables**:

| Variable | Purpose |
|---|---|
| `CP_S3_BUCKETS` | `name[:region],...` — buckets to collect, e.g. `my-bucket:us-east-1,other-bucket` |
| `CP_S3_REGION` / `AWS_REGION` | Default region for entries in `CP_S3_BUCKETS` that omit `:region` (default `us-east-1`) |
| `AWS_ACCESS_KEY_ID` | AWS access key ID |
| `AWS_SECRET_ACCESS_KEY` | AWS secret access key |
| `AWS_SESSION_TOKEN` | Optional, for temporary/STS credentials |
| `CP_S3_FILTER_ID` | S3 request-metrics filter ID (default `EntireBucket`) |

**Storage metrics** (`BucketSizeBytes`, `NumberOfObjects`) are CloudWatch's
free, always-on daily storage metrics — no bucket configuration needed.

**Request metrics** (`AllRequests`, per-class GET/PUT/HEAD/LIST/etc. counts,
`BytesDownloaded`) require **S3 Request Metrics** to be explicitly enabled
on the bucket, which is a **paid** CloudWatch feature billed by AWS. Without
it, cloud-pulse reports `request_metrics_available: false` for that bucket
(not an error — storage size/object count still work). Enable it with a
filter ID matching `CP_S3_FILTER_ID` (default `EntireBucket`):

```bash
aws s3api put-bucket-metrics-configuration \
  --bucket my-bucket \
  --id EntireBucket \
  --metrics-configuration Id=EntireBucket
```

Or via the console: bucket → **Metrics** tab → **Create filter**, filter
name `EntireBucket`, no prefix/tag filter (covers the whole bucket).

## Cloudflare R2 setup

The hub queries Cloudflare's GraphQL Analytics API — no S3-compatible API
calls, no object listing.

1. Create an API token at **Cloudflare dashboard → My Profile → API
   Tokens** with the **Account Analytics: Read** permission for the
   account that owns the R2 buckets.
2. Set:

   | Variable | Purpose |
   |---|---|
   | `CP_R2_ACCOUNT_ID` | Cloudflare account ID |
   | `CP_R2_API_TOKEN` | API token with Account Analytics: Read |
   | `CP_R2_BUCKETS` | Optional comma-separated bucket names; empty = all buckets visible in the account's analytics |

R2's free tier (used to render usage bars in the dashboard) is **10 GB**
storage, **1,000,000** Class A operations, and **10,000,000** Class B
operations per month. R2 egress is always free, so cloud-pulse does not
track R2 egress bytes.

## Configuration reference

### Hub environment variables (`CP_*`)

| Variable | Default | Notes |
|---|---|---|
| `CP_LISTEN` | `:8090` | HTTP listen address; overridden by a hub-side Network settings override once one is confirmed from the dashboard (see [Network settings](#network-settings)) |
| `CP_DATA_DIR` | `./data` | Directory holding `cloud-pulse.db` |
| `CP_AGENT_TOKEN` | *(required)* | ≥16 chars; authenticates agent report ingestion |
| `CP_UI_TOKEN` | *(unset)* | ≥8 chars if set; **optional** static bearer token for scripts (curl/CI) — full read/admin access, never subject to the must-change-password gate. The dashboard itself always requires signing in regardless of this setting (see [Sign-in and accounts](#sign-in-and-accounts)) |
| `CP_ALLOWED_CIDRS` | `100.64.0.0/10,fd7a:115c:a1e0::/48,127.0.0.0/8,::1/128` | Comma-separated CIDRs/IPs allowed to reach the hub; `*` disables the allowlist; overridden by a hub-side Network settings override once one is confirmed |
| `CP_OFFLINE_AFTER` | `60s` | Host reported "down" after this long since last-seen |
| `CP_CLOUD_INTERVAL` | `15m` (min `1m`) | Interval between S3/R2 collections |
| `CP_ALERT_WEBHOOK_URL` | *(unset)* | Slack- or Discord-compatible webhook for egress alerts; also used to seed a `"Default webhook"` notify channel on the v0.5.0 upgrade migration (see [Alerting](#alerting)) |
| `CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS` | `0` (disabled) | `1` relaxes the SSRF guard on Discord/Telegram/WhatsApp notify channels, allowing a channel's `api_base`/`webhook_url` to point at any host instead of only the official one. **Only intended for tests/fakes** — set only in a sandboxed or test environment, never on a production hub with real credentials (see [Security notes](#security-model)) |
| `CP_UPDATE_CHECK` | `true` | `false` disables all outbound checks against GitHub for a newer release (see [Update notifications](#update-notifications)) |
| `CP_UPDATE_LATEST_URL` | *(unset)* | Override the "latest release" URL the hub polls, e.g. for a mirror or air-gapped release feed |
| `CP_RELEASE_BASE_URL` | *(unset)* | Override the base URL `sudo cloud-pulse-hub update` downloads the binary + `checksums.txt` from (same semantics as the installers' `CP_RELEASE_BASE_URL`) |
| `CP_LOG_LEVEL` | `info` | `debug\|info\|warn\|error` |
| `CP_LOG_FORMAT` | `text` | `text\|json` |
| `CP_S3_BUCKETS` | *(unset)* | `name[:region],...` |
| `CP_S3_REGION` / `AWS_REGION` | `us-east-1` | Default region for bucket entries without `:region` |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` | *(unset)* | AWS credentials for CloudWatch calls |
| `CP_S3_FILTER_ID` | `EntireBucket` | S3 request-metrics filter ID |
| `CP_R2_ACCOUNT_ID` / `CP_R2_API_TOKEN` | *(unset)* | Cloudflare R2 credentials |
| `CP_R2_BUCKETS` | *(unset, = all)* | Comma-separated R2 bucket names |

S3 collection is enabled only when `CP_S3_BUCKETS` is non-empty **and**
both AWS key env vars are set. R2 collection is enabled only when both
`CP_R2_ACCOUNT_ID` and `CP_R2_API_TOKEN` are set.

### Hub CLI flags (`cloud-pulse-hub`)

| Flag | Purpose |
|---|---|
| `-listen ADDR` | Override `CP_LISTEN` |
| `-data-dir DIR` | Override `CP_DATA_DIR` |
| `-check-config` | Load + validate config, print a secret-redacted summary, exit |
| `-gen-token` | Print a random 32-byte hex token (for `CP_AGENT_TOKEN`/`CP_UI_TOKEN`) and exit |
| `-version` | Print version info and exit |

### Hub update subcommand (`cloud-pulse-hub update`)

| Usage | Purpose |
|---|---|
| `cloud-pulse-hub update` | Update to the latest release and restart the service if active (v0.3.0+ binaries only) |
| `cloud-pulse-hub update --check` | Check only; exit `10` if an update is available, `0` if already up to date |
| `cloud-pulse-hub update --version vX.Y.Z` | Install an exact tag instead of latest (allows downgrade) |
| `cloud-pulse-hub update --no-restart` | Install but skip the restart step |

Exit codes: `0` success or already up to date, `10` (`--check` only) an
update is available, `1` error. See
[Upgrading](#upgrading) for full behavior and the legacy migration path.

### Hub `systemd-unit` subcommand (`cloud-pulse-hub systemd-unit`)

Single source of truth (`internal/systemdunit`) for rendering and
updating the hub's own systemd unit; both installers and `update` (since
v0.3.1) call into this instead of maintaining their own unit templates.

| Usage | Purpose |
|---|---|
| `cloud-pulse-hub systemd-unit print --bin-path P --env-file E [--user U --group G] [--read-write-path D]` | Print a rendered unit file to stdout; `--read-write-path` renders `ReadWritePaths=D` for sandbox installs instead of `StateDirectory=cloud-pulse` |
| `cloud-pulse-hub systemd-unit apply [--unit-path /etc/systemd/system/cloud-pulse-hub.service] [--no-reload]` | Re-render an already-installed unit file in place if it has drifted from the current template; backs up the previous content to `<unit-path>.bak`, then runs `systemctl daemon-reload` unless `--no-reload` |

Both are dispatched before flag parsing/config loading, the same as
`update`. `print` is what the installers' `render_unit()` calls on a
freshly downloaded v0.3.1+ binary, falling back to a built-in bash
heredoc only if that call fails (kept byte-identical to `Render`'s
output, checked by a drift test in `scripts/test-install.sh`). Since
v0.4.0 the hub's rendered unit additionally restricts
`RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK` (the extra
`AF_NETLINK` is required by `net.Interfaces()`, used by the Network
settings page to list adapters — the agent unit is unaffected, it
doesn't need netlink). `sudo cloud-pulse-hub update` from a v0.3.1+
install applies this automatically via `systemd-unit apply`; see
[Upgrading](#upgrading).

### Hub `reset-password` / `reset-network` subcommands

Recovery CLIs, dispatched before flag parsing like `update`, for an
admin locked out of the dashboard itself. Both resolve their data
directory with the same precedence as the hub (explicit `--data-dir` >
`CP_DATA_DIR` > `/var/lib/cloud-pulse` if it exists > `./data`), work
correctly while the hub is running (SQLite WAL + busy-timeout), and —
when run as root — restore ownership of the database file (and any
`-wal`/`-shm` sidecars) to the data directory's owner afterward.

| Usage | Purpose |
|---|---|
| `cloud-pulse-hub reset-password [--data-dir DIR]` | Reset the admin password to the default `changeme`, forcing a change on next login; revokes every existing session |
| `cloud-pulse-hub reset-password --password-stdin [--data-dir DIR]` | Reset the admin password to one line read from stdin (policy-checked); does not force a further change; revokes every existing session |
| `cloud-pulse-hub reset-network [--data-dir DIR]` | Delete the hub-side Network settings override so the hub falls back to `CP_LISTEN`/`CP_ALLOWED_CIDRS` on next restart — the recovery path for an admin who locked themselves out via the Network settings page (see [Network settings](#network-settings)) |

See [Sign-in and accounts](#sign-in-and-accounts) and
[Network settings](#network-settings) for the full behavior each
recovers from.

### Agent environment variables (`CP_*`)

| Variable | Default | Notes |
|---|---|---|
| `CP_HUB_URL` | *(required)* | Hub base URL, `http://` or `https://` |
| `CP_AGENT_TOKEN` | *(required)* | ≥16 chars; must match the hub's `CP_AGENT_TOKEN` |
| `CP_HOST_ID` | sanitized hostname | 1–128 chars of `[A-Za-z0-9._-]` if set explicitly |
| `CP_INTERVAL` | `15s` (min `5s`) | Collect/report interval |
| `CP_PROVIDER` | `auto` | `auto\|aws\|oci\|other`; `auto` detects via Linux DMI |
| `CP_EGRESS_LIMIT_GB` | *(unset = provider default)* | `0` = unlimited; GiB units, fractional allowed |
| `CP_NET_EXCLUDE` | `lo,lo0,docker*,veth*,br-*,virbr*,tailscale*,utun*,cni*,flannel*,cali*,kube*,vxlan*,tun*,wg*,zt*` | Comma-separated interface-name globs excluded from egress/network accounting |
| `CP_TIME_SYNC` | `hub` | `hub\|local`; `hub` corrects sample timestamps to the hub's clock (see [Time synchronization](#time-synchronization)), `local` uses the agent's own clock unmodified |
| `CP_SEND_JITTER` | `0s` | Max random delay between collecting and sending a sample, to spread simultaneous sends across a fleet; must be `<=` half of `CP_INTERVAL` |
| `CP_DOCKER` | `auto` | `auto` (probe `/var/run/docker.sock` or `DOCKER_HOST`'s `unix://` path) \| `off` (disable Docker collection entirely) \| an explicit socket path/URL (e.g. a rootless Podman socket) — see [Inventory](#inventory-docker-services--listening-ports) |
| `CP_LOG_LEVEL` | `info` | `debug\|info\|warn\|error` |
| `CP_LOG_FORMAT` | `text` | `text\|json` |

### Agent CLI flags (`cloud-pulse-agent`)

| Flag | Purpose |
|---|---|
| `-once` | Collect two samples 1s apart, print the second as JSON, exit (no hub config needed) |
| `-print-host` | Print detected `HostInfo` as JSON and exit (requires hub config) |
| `-version` | Print version info and exit |

### Agent update subcommand (`cloud-pulse-agent update`)

| Usage | Purpose |
|---|---|
| `cloud-pulse-agent update` | Update to the latest release and restart the service if active (v0.3.0+ binaries only) |
| `cloud-pulse-agent update --check` | Check only; exit `10` if an update is available, `0` if already up to date |
| `cloud-pulse-agent update --version vX.Y.Z` | Install an exact tag instead of latest (allows downgrade) |
| `cloud-pulse-agent update --no-restart` | Install but skip the restart step |

Same exit codes as the hub's `update` subcommand. `CP_UPDATE_LATEST_URL`
and `CP_RELEASE_BASE_URL` (see the hub env table above) also override the
agent binary's own `update` subcommand — both binaries read the same two
variables via the shared `internal/selfupdate` package, so a mirror/
air-gapped override only needs to be set once per environment.

### Agent `systemd-unit` subcommand (`cloud-pulse-agent systemd-unit`)

Same shape as the hub's (see above), rendering `cloud-pulse-agent.service`
instead. `print` ignores `--read-write-path` (the agent unit has no
`StateDirectory=`/`ReadWritePaths=` line at all).

| Usage | Purpose |
|---|---|
| `cloud-pulse-agent systemd-unit print --bin-path P --env-file E [--user U --group G]` | Print a rendered unit file to stdout |
| `cloud-pulse-agent systemd-unit apply [--unit-path /etc/systemd/system/cloud-pulse-agent.service] [--no-reload]` | Re-render an already-installed unit file in place if drifted; backs up to `<unit-path>.bak`, then `systemctl daemon-reload` unless `--no-reload` |

## Egress accounting

Outbound (egress/TX) and inbound (ingress/RX) traffic are tracked and
alerted on **separately**. Both are approximated as the **sum of byte
deltas on physical network interfaces**, accumulated per **UTC calendar
month**. Virtual/tunnel interfaces are excluded by default via
`CP_NET_EXCLUDE` (loopback, Docker bridges/veth, Tailscale, generic VPN
tunnel prefixes — see the table above for the exact glob list).

This is an **approximation**, not a billing-accurate figure:

- Intra-region and intra-VPC traffic is counted the same as internet
  egress, even though many providers don't bill for it.
- AWS's real 100 GB free tier is aggregated **across the whole account**,
  not per host — cloud-pulse tracks each host independently.
- OCI's 10 TB free tier is **per tenancy**, same caveat.
- The projected month-end figure is a simple linear projection from
  month-to-date usage, not a forecast that accounts for traffic patterns.

**Limits and precedence**: outbound keeps the existing billing-limit
semantics (AWS 100 GB, OCI 10 TB, other = unlimited default, overridable
per agent via `CP_EGRESS_LIMIT_GB`). Inbound has no provider default — it
is unlimited unless a limit is configured. Both directions can be
**overridden per host from the hub** (`PUT /api/v1/hosts/{id}/limits` or
the Settings UI), persisted in SQLite. Precedence, evaluated wherever
egress is computed (host summaries, `/api/v1/egress`, alerts):

- **Outbound**: hub override (if set) > agent-reported limit
  (`CP_EGRESS_LIMIT_GB` / provider default).
- **Inbound**: hub override (if set) > unlimited.

A hub override of `0` means **explicitly unlimited**; clearing the
override (omitting the field, or setting it back to `null`) reverts to
the value above it in precedence. Limits are stored and returned in
**bytes**, but the Settings UI's input fields use **GiB** (`1 GiB =
2^30 bytes`) to match `CP_EGRESS_LIMIT_GB`'s units. Each host's response
includes `limit_source` (`"agent"` or `"hub"`) and `rx_limit_source`
(`"none"` or `"hub"`) so the dashboard can show which side of the
precedence chain is currently effective.

Alerts fire once per host per month per direction at each of 80%
(warning), 95% (critical), and 100% (exceeded) of that direction's
configured limit, via the effective webhook URL (hub-side override, else
`CP_ALERT_WEBHOOK_URL`, see [Settings UI](#settings-ui)). A report that
crosses multiple thresholds at once still emits each newly attained
alert, independently for outbound and inbound.

## Time synchronization

Agents in a fleet can have clocks that drift independently, which used to
mean each host's samples carried its own, slightly different notion of
"now." v0.2 synchronizes sample timestamps across the whole fleet to a
single reference: **the hub's clock**.

- **The hub host should run NTP** (e.g. `systemd-timesyncd`, enabled by
  default on most distros — check with `timedatectl status`). The hub
  itself does nothing special to stay in sync; it's just the fleet's
  agreed-upon reference clock, so its own accuracy matters.
- **Offset estimation is NTP-style**: before its first sample, and after
  every report, an agent measures its local send/receive time around a
  request to the hub and computes `offset = server_time − midpoint(t0,
  t1)`. It keeps the last 8 such observations and uses the one with the
  smallest round-trip time (least likely to be skewed by queueing delay)
  as its current best offset estimate — the same "clock filter" idea NTP
  uses. Samples with RTT `> 5s` or negative are discarded.
- **Sampling is epoch-aligned**: instead of "every `CP_INTERVAL` since the
  agent started," each agent collects at the next hub-time boundary that's
  a multiple of the interval (e.g. `:00/:15/:30/:45` for a 15s interval).
  Since every agent computes the same boundary from the same corrected
  clock, a whole fleet's samples share identical `ts` values for the same
  collection round, regardless of when each agent process actually
  started or how far its local clock had drifted.
- **Sends happen right after collecting** — simultaneously across the
  fleet — unless `CP_SEND_JITTER` is set to spread outbound requests
  over a window (useful for larger fleets hitting the hub at the same
  instant). Rate/delta metrics inside a sample still use the real
  monotonic time elapsed since the previous collection, not the
  boundary-to-boundary difference, so a missed or delayed boundary (e.g.
  the host was suspended) never distorts a rate calculation — the agent
  simply skips forward to the next future boundary instead of bursting
  catch-up samples.
- **Backward compatible**: an old agent talking to a new hub keeps
  working unmodified (it just doesn't read `server_time_ms`). A new agent
  talking to an old hub gets a 404 from `GET /api/v1/agent/time`, logs
  "hub does not support time sync; using local clock" once, and falls
  back to its own clock without retrying that endpoint.
- Set `CP_TIME_SYNC=local` to disable hub-clock correction entirely and
  use each agent's own clock, matching v0.1 behavior.

## Upgrading

### v0.3.1 and later: `2) Reinstall` (or `--reinstall`) once, then `update` forever

Starting with v0.3.1, **`update` handles both the binary and the systemd
unit**, so re-running the installer is no longer needed after the first
time you reach v0.3.1. If you're upgrading an existing install of *any*
version (v0.1.x/v0.2.x/v0.3.0), run the installer one more time — at a
terminal, choose **2) Reinstall** from the menu; non-interactively, pass
`--reinstall`:

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-hub.sh \
  | sudo bash -s -- --reinstall
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh \
  | sudo bash -s -- --reinstall
```

This keeps every existing token/setting/data file untouched, re-renders
the systemd unit (via the freshly downloaded binary's own
`systemd-unit print`, see below), explicitly restarts the service if it
is already running (or starts it if inactive), and prints `Upgraded vA → vB` plus,
the first time this crosses the v0.3.0 boundary, "This install now
includes the built-in updater." **From that point on**, the two
`update` subcommands are the only thing you need — including for future
systemd-unit template changes:

```bash
sudo cloud-pulse-hub update
sudo cloud-pulse-agent update
```

Both binaries ship a self-update subcommand, dispatched before any flag
parsing or config loading, so it works even on an otherwise unconfigured
install (no `CP_AGENT_TOKEN`/`CP_HUB_URL` required).

Flags (identical on both binaries):

| Flag | Effect |
|---|---|
| `--check` | Resolve the latest release and report whether an update is available, without downloading or installing anything |
| `--version vX.Y.Z` | Install this exact tag instead of the latest release (allows downgrading) |
| `--no-restart` | Install the new binary but skip the service-restart step |

Exit codes: `0` success (or already up to date), `10` (`--check` only) an
update is available, `1` error.

What `update` does, in order:

1. Resolves the latest release tag via GitHub's `.../releases/latest`
   redirect (or `--version`'s explicit tag). No GitHub API calls, so no
   rate limiting.
2. Downloads the platform-matching asset and `checksums.txt` from
   `.../releases/download/<tag>/...`, verifies the asset's sha256 against
   the exact `checksums.txt` entry, and aborts with no change to the
   running binary if it doesn't match.
3. Runs the newly downloaded binary's own `-version` and requires the
   target tag to appear in its output, catching a corrupted or
   mismatched asset before it's ever installed.
4. Atomically replaces the running binary (temp file staged in the same
   directory, then renamed over the original — never a window where the
   binary is missing or partially written).
5. **Applies any systemd unit changes** (new since v0.3.1): only when
   running as root on Linux with `systemctl` available, an existing
   `cloud-pulse-hub.service`/`cloud-pulse-agent.service` unit file, and
   the tag just installed is ≥ `v0.3.1` (so the newly installed binary
   is guaranteed to ship the `systemd-unit` subcommand itself), runs
   `<binary> systemd-unit apply --unit-path <unit>` — this re-renders
   the unit from the same `internal/systemdunit.Render` template used by
   the installers, backs up the previous unit alongside it
   (`<unit>.bak`), and reloads systemd if anything changed. A failure
   here is printed as a warning but never fails the update — the binary
   replacement already succeeded, and restart still proceeds against
   whichever unit file is currently on disk.
6. If running as root on Linux with `systemctl` available and the unit
   installed and currently active, runs `systemctl restart` on it. If
   the unit exists but is inactive, it's deliberately left stopped
   rather than started. In every other case (not root, no systemd,
   `--no-restart`, Windows/macOS/FreeBSD), it prints a reminder to
   restart manually — the binary is still updated either way.

Recommended order for a fleet: **upgrade the hub first, then agents**:

```bash
sudo cloud-pulse-hub update
# on each agent host:
sudo cloud-pulse-agent update
```

Old agents keep working against a new hub unmodified (see D-048 in
[DECISIONS_LOG.md](DECISIONS_LOG.md)), so there's no requirement to
upgrade every agent in lockstep — the dashboard will simply show them as
outdated in the meantime.

**Upgrading an existing install to v0.4.0**: a plain `sudo
cloud-pulse-hub update` (no reinstall needed, since your install is
already on v0.3.1+) is enough — it replaces the binary and, via
`systemd-unit apply`, adds the new `AF_NETLINK` permission the Network
settings page needs. The next time you open the dashboard, it will show
the sign-in screen: use `admin` / `changeme` if this is the first time
v0.4.0 has started (a fresh admin account is bootstrapped only when no
password hash exists yet — an upgrade from any v0.3.x database is not
"fresh," so if you never signed in before, this is genuinely the first
login and `changeme` is correct), and you'll immediately be asked to
choose a new password. If your hub has been running long enough to have
already gone through a v0.4.0 first-login-and-password-change on a
previous update, sign in with that existing password. Forgot it? `sudo
cloud-pulse-hub reset-password` from the same host, without needing to
stop the service. `CP_UI_TOKEN`, if you already had one set for the old
open-read-API behavior, keeps working exactly as before as an optional
API bearer token — it's no longer read by the dashboard's login flow,
but scripts using it as a bearer token need no changes.

### Upgrading from v0.1.x / v0.2.x / v0.3.0 (no unit-syncing `update` yet)

Versions before v0.3.0 don't have the `update` subcommand at all — an old
binary given `update` as its first argument just ignores it and fails
during normal config loading (`CP_AGENT_TOKEN is required`, etc.), since
that dispatch didn't exist yet and can't be added retroactively to an
already-installed binary. A v0.3.0 binary has `update`, but that
`update` only replaces the binary — it doesn't know how to touch the
systemd unit, since `internal/systemdunit`/`systemd-unit apply` didn't
exist until v0.3.1.

To move any pre-v0.3.1 install (v0.1.x, v0.2.x, or v0.3.0) onto v0.3.1+,
run the same installer one-liner you used originally, once — at a
terminal, choose **2) Reinstall**; non-interactively, pass `--reinstall`:

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-hub.sh \
  | sudo bash -s -- --reinstall
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh \
  | sudo bash -s -- --reinstall
```

This is the same idempotent re-run-to-upgrade path described in
[Quick start](#quick-start): it downloads the current release, replaces
the binary, and re-renders the systemd unit (via the freshly downloaded
binary's own `systemd-unit print`, falling back to a built-in heredoc
only if that call fails, e.g. an explicit `--version` pin older than
v0.3.1), while **preserving** `hub.env`/`agent.env` (tokens, settings,
per-host data) untouched unless you pass an explicit flag to change one.
The installer detects the previous version before replacing the binary
and prints an `Upgraded vA → vB` line plus, the first time this crosses
the v0.3.0 boundary, "This install now includes the built-in updater."
From that point on, `sudo cloud-pulse-hub update` / `sudo
cloud-pulse-agent update` is all you need for future upgrades — **no
more re-running the installer, even for future systemd-unit template
changes**.

The dashboard's per-host update badge (see below) always shows the
correct command for that specific agent — the legacy installer one-liner
for an agent still below v0.3.0, or `sudo cloud-pulse-agent update` once
it's past that line — so you don't have to track each host's version by
hand. Same recommended order as above: hub first, then agents.

### Update notifications

- **Dashboard banner**: when the hub's `GET /api/v1/version` reports
  `update_available: true`, every dashboard page shows a dismissible
  banner with the `update_command` and a link to the release notes.
  Dismissing a banner remembers that release tag, so it won't reappear
  until a newer one is published.
- **Per-agent badge**: each host card/detail page shows an "update
  available" badge when that agent's own reported version is older than
  the latest known release, with the exact command for that agent
  (`sudo cloud-pulse-agent update` for v0.3.0+ agents, the legacy
  installer one-liner for older ones).
- **Disabling outbound checks**: set `CP_UPDATE_CHECK=false` on the hub
  to stop it from ever contacting GitHub for release information — no
  background check, `update_check_enabled: false` in the API response,
  no banner, no per-agent badges. Useful for air-gapped or
  privacy-sensitive deployments. This only affects the *background
  notification* check; running `cloud-pulse-hub update`/
  `cloud-pulse-agent update` by hand still works and still contacts
  GitHub (or your configured mirror) on demand.
- **Mirrors / air-gapped installs**: `CP_UPDATE_LATEST_URL` and
  `CP_RELEASE_BASE_URL` redirect both the hub's background check and
  both binaries' `update` subcommand to a mirror or internal release
  feed instead of `github.com`. Set once per environment; both binaries
  read the same two variables.
- **Trust model**: updates are fetched over HTTPS and verified against a
  sha256 checksum published in the same release's `checksums.txt` — this
  proves the downloaded binary matches what GitHub is currently serving
  for that tag, but it is **not a cryptographic signature**. Anyone who
  can tamper with the release assets (or a compromised/malicious
  `CP_RELEASE_BASE_URL` mirror) can tamper with both the binary and its
  checksum together. This is the same trust boundary the install scripts
  already have (see [D-026](DECISIONS_LOG.md)); `update` doesn't
  introduce a weaker one, but it doesn't add package-signing-level
  assurance either.

## Settings UI

The dashboard's Settings page (`#/settings`) lets you reveal the agent
token and copy a ready-to-run install command, edit per-host egress/
ingress limit overrides, and manage the alert webhook URL — all from the
browser instead of editing `hub.env` and restarting the service.

**Why it requires signing in**: the agent token grants write access to
ingest metrics for any host, so it must never be exposed to an
unauthenticated request. All settings/token endpoints are **admin**
endpoints — since v0.4.0 that simply means "authenticated as the signed-
in `admin` account or the optional `CP_UI_TOKEN`," the same as every
other dashboard route (see [Sign-in and accounts](#sign-in-and-accounts)).
The old v0.3.x `admin_disabled` mode — where these endpoints were
unreachable at all on a hub with no `CP_UI_TOKEN` configured — no longer
exists: the dashboard always requires signing in, so the Settings page
is always reachable to whoever can sign in as `admin`.

**`CP_UI_TOKEN` today** is optional and no longer gates the Settings
page at all: set it only if you also want script/CI access to the same
endpoints without going through the sign-in flow.

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-hub.sh \
  | sudo bash -s -- --reinstall --generate-ui-token
```

- `--generate-ui-token` is **idempotent**: it generates a token only if
  `CP_UI_TOKEN` isn't already set, and leaves an existing one unchanged
  — safe to pass on every reinstall/upgrade without rotating the token
  operators already have.
- `--rotate-ui-token` always generates a **new** token, replacing any
  existing one (use this to invalidate a token you suspect has leaked).
- Either way, a token that was newly generated or rotated **this run**
  is printed exactly once in the final summary (`API token: ...`); an
  unchanged existing token instead prints a hint to read it from
  `hub.env` (`sudo grep CP_UI_TOKEN /etc/cloud-pulse/hub.env`) rather
  than ever re-printing its value.

Manual alternative (no installer re-run): generate a token with
`cloud-pulse-hub -gen-token`, add `CP_UI_TOKEN=<token>` to
`/etc/cloud-pulse/hub.env`, then `sudo systemctl restart cloud-pulse-hub`.

## Network settings

Since v0.4.0, the dashboard's Settings → Network page manages the hub's
own listen address(es) and access allowlist as **hub-managed state**
(persisted in SQLite), instead of requiring an edit to `hub.env` and a
service restart for every change:

- **Adapters**: lists every network interface the hub host has (via
  `net.Interfaces()`), each address's family (IPv4/IPv6), scope
  (global/link-local/loopback), and a best-effort classification —
  `loopback`, `tailscale` (by interface name or membership in
  Tailscale's `100.64.0.0/10` / `fd7a:115c:a1e0::/48` ranges), `virtual`
  (Docker/veth/bridge/VPN-style names), or `physical`. Each address
  carries a `suggested_cidr` for the "Allow" one-click shortcuts in the
  allowlist editor (e.g. a Tailscale address suggests
  `100.64.0.0/10`/`fd7a:115c:a1e0::/48`; a loopback address suggests
  `127.0.0.0/8`/`::1/128`; anything else suggests its own subnet). A
  failure to enumerate interfaces (rare; platform-dependent) is reported
  as an `interfaces_error` string in the response — **never** a `500`,
  since the rest of the page still needs to work.
- **Listen selection**: choose "all interfaces" or one or more specific
  addresses, plus a port (1024–65535). Applying a change opens the new
  listener(s) **before** closing any removed ones, and a removed
  listener's socket stays open for a further second after that so any
  in-flight HTTP response (including the API response confirming the
  change) has time to flush — a client is never abruptly cut off by its
  own request.
- **Access allowlist**: the same CIDR/IP list `CP_ALLOWED_CIDRS`
  represents, editable live; a swap of the effective list is atomic.
- **Change safety — a Network settings change can never lock out the
  admin who's making it**:
  - A new allowlist that would exclude the requesting client's own IP is
    rejected outright, before anything is applied: `409` with code
    `would_lock_out`.
  - A new listen configuration where **no** address ends up `listening`
    or `waiting` is rolled back to the previous configuration
    automatically: `409` with code `bind_failed` and a per-address error
    summary.
  - If the change still binds successfully but would stop serving the
    *requesting client's own connection* (e.g. removing the address the
    admin is currently connected through), the hub does **not** apply it
    unconditionally — it opens the new listener(s) immediately (so they
    can be tested) but treats the change as **pending**: it keeps
    serving the old configuration's address(es) too, returns the set of
    candidate URLs to verify the new configuration from, and starts a
    120-second deadline. `POST .../confirm` from a connection that
    already proves the new configuration works persists it for good;
    `POST .../revert` (or simply doing nothing until the deadline) puts
    the previous configuration back automatically — logged at `warn`
    either way. Only one change may be pending at a time; a second `PUT`
    while one is pending gets `409` `change_pending`.
  - `DELETE /api/v1/settings/network` drops the hub-side override
    entirely, reverting to `CP_LISTEN`/`CP_ALLOWED_CIDRS` from the
    environment — subject to the same lock-out/bind-failure checks as a
    `PUT`.
- **`sudo cloud-pulse-hub reset-network [--data-dir DIR]`**: the offline
  recovery path if an admin manages to lock themselves out anyway (e.g.
  by editing the allowlist from a session that then expired, or by
  restarting the hub while a config that only barely worked was active).
  Deletes the persisted override so the *next restart* falls back to
  `CP_LISTEN`/`CP_ALLOWED_CIDRS`; same data-directory resolution and
  root-owned-chown behavior as `reset-password`.
- The **`AF_NETLINK`** capability the hub's systemd unit gained in
  v0.4.0 (see [systemd-unit](#hub-systemd-unit-subcommand-cloud-pulse-hub-systemd-unit))
  exists solely so `net.Interfaces()` can enumerate adapters for this
  page — the agent unit is unaffected, since agents read network
  metrics from `/proc`, not netlink sockets.

## Alerting

Since v0.5.0, alerting is a configurable rule engine
(`internal/alerting`) rather than the fixed egress-percentage-only
behavior of earlier versions: any number of **rules** evaluate a
**metric** against a **threshold**, optionally sustained for a
**duration**, and notify one or more **channels** (Discord, Telegram,
WhatsApp, or a generic webhook) with a rendered chart image attached.

### Rule model

An `AlertRule` (`internal/models/alerting.go`) has:

- **Metric**: `cpu` | `memory` | `disk` | `load1` | `egress_out_pct` |
  `egress_in_pct` | `host_down`.
- **Scope**: a specific `host_id`, or `""` for "every host."
- **Operator/threshold**: `>` or `>=` against the metric's own unit
  (percent for cpu/memory/disk/egress; a raw load-average number for
  `load1`; ignored for `host_down`).
- **Duration** (`duration_sec`): the sustained window the condition must
  hold for. `0` fires immediately on the first breaching sample. For
  `host_down`, the effective offline threshold is `CP_OFFLINE_AFTER +
  duration_sec` — a rule's own duration is *added on top of* the
  existing offline-detection window, not a replacement for it.
- **Cooldown** (`cooldown_sec`, default `3600`): the minimum time
  between re-notifications while a rule keeps firing on the same host
  ("still firing" reminders). `0` means notify once on firing and never
  again until it resolves and re-fires.
- **Notify resolved**: whether a resolved transition also sends a
  notification.
- **Channels**: the list of notify-channel IDs this rule delivers to.

### Sustained-window semantics

A rule with `duration_sec > 0` (for cpu/memory/disk/load1 — egress and
host_down are evaluated differently, see below) only fires once **every
raw sample** in `[now - duration_sec, now]` satisfies the condition,
**and** that window is actually covered by history (the earliest sample
returned must be at or before `now - duration_sec`, within a small
tolerance for collection jitter). A host that started reporting less
than `duration_sec` ago cannot fire a duration-gated rule on partial
data — the window must be fully covered first. This is checked against
the raw (un-rolled-up) sample tier via the same `QuerySeries` path the
charts use, so the window's resolution matches the agent's actual report
interval, not a rollup bucket.

### State machine

Each `(rule, host)` pair is tracked independently
(`alert_state`, `PRIMARY KEY (rule_id, host_id)`), moving through:

```
ok → pending → firing → resolved → ok
```

- **ok → pending**: the condition is newly satisfied but hasn't yet been
  sustained for `duration_sec`.
- **pending → firing**: the condition has now held for the full
  sustained window (or `duration_sec == 0`) — an `AlertEvent` row is
  created and the initial notification is sent.
- **firing → firing** ("still firing"): the condition remains satisfied
  on a later evaluation; re-notifies only once `cooldown_sec` has
  elapsed since the last notification, otherwise just refreshes the
  tracked value silently.
- **firing → resolved**: the condition is no longer satisfied. The
  active `AlertEvent` is marked resolved; a notification is sent only if
  `notify_resolved` is set on the rule.
- **pending → ok**: the condition dropped before ever sustaining long
  enough to fire — no event, no notification, silent.

The engine (`internal/alerting.Engine.Evaluate`) runs after every agent
report (for that host) and on a 30-second scheduler tick (covering
`host_down` and cooldown/duration transitions that need to happen even
without a fresh sample). `Preview` runs the same condition check
read-only, against every current host, without touching persisted state
— this is what the Settings UI's rule editor uses for its live "would
fire now" preview.

### Egress rules: once-per-month dedupe, not the general state machine

`egress_out_pct`/`egress_in_pct` rules deliberately do **not** use the
pending/cooldown/resolved machinery above. Per the pre-v0.5 egress-alert
behavior they replace, each rule+host+calendar-month combination
notifies **at most once**, regardless of `cooldown_sec` (ignored for
egress metrics), and **never** sends a resolved notification even if
`notify_resolved` is set — usage dropping back under a threshold and
crossing it again in the *same* month does not re-fire; only a new UTC
calendar month resets eligibility. This is implemented by repurposing
the same `alert_state` row: `state = firing` + `since` inside the
current month means "already notified this threshold this month."

### Default rules on upgrade

Migration `0004_alerting.sql` seeds three enabled `egress_out_pct` rules
on every hub — new or upgrading from v0.4.x — replicating the old fixed
80/95/100% outbound-egress behavior exactly:

| Name | Threshold |
|---|---|
| Outbound traffic 80% (warning) | `>= 80` |
| Outbound traffic 95% (critical) | `>= 95` |
| Outbound traffic 100% (exceeded) | `>= 100` |

If a webhook URL was already configured (`CP_ALERT_WEBHOOK_URL`, or a
previously-set `alert_webhook_url` setting from the old `PUT
/api/v1/settings/alerts` endpoint) at the time this migration runs, a
`"Default webhook"` notify channel is created from it and attached to
all three seeded rules automatically — an upgrading hub keeps alerting
exactly as before with zero manual reconfiguration. If no webhook was
configured, the three rules are still seeded (enabled, with no
channels) so they show up ready to attach a channel to, rather than
silently absent. The old `PUT /api/v1/settings/alerts` and `POST
/api/v1/settings/alerts/test` endpoints keep working unmodified — they
read/write the same `"Default webhook"` channel by name.

### Chart images

Every firing/resolved notification for a series-backed metric (cpu,
memory, disk, load1 — egress and host_down have no queryable time
series and correctly ship with no image) includes an 800×400 PNG chart
(`internal/alerting/chart`, standard library `image/png` only, no font
or charting dependency): a dark background, the metric's line over
`max(duration_sec * 3, 1h)` of history, a dashed red threshold line, a
shaded band over the breach window, axis ticks, and a title like
`HOSTNAME · CPU 93.4% > 90% for 5m`. Text is rendered with a small
embedded 5×7 bitmap font written in Go — there is no font file, system
font dependency, or third-party rendering library involved. Rendering is
deterministic (covered by a golden-hash test), so the same inputs always
produce byte-identical PNG output.

## Notification channels

A **notify channel** (`NotifyChannel`) is a saved destination — Discord,
Telegram, WhatsApp, or a generic webhook — that one or more alert rules
can deliver to. Channels are managed via `GET/POST
/api/v1/alerts/channels` and `PUT/DELETE /api/v1/alerts/channels/{id}`,
or from the dashboard's Settings → Notifications page, which shows a
step-by-step setup guide for the selected platform next to the channel
form.

Delivery is asynchronous: `internal/alerting.Engine` enqueues each
channel's delivery onto a bounded worker queue (100 jobs) with a 20-
second per-attempt timeout and up to 3 retries with backoff on `5xx` or
`429` responses (honoring a platform's `Retry-After` header when
present, e.g. Telegram's `parameters.retry_after` or Discord/Graph's
`Retry-After` header). Every delivery attempt — success or failure, for
every channel a firing rule targets — is recorded on the `AlertEvent`'s
`deliveries` list, visible via `GET /api/v1/alerts/events`.

### Discord

**Setup**: Server Settings → Integrations → Webhooks → New Webhook →
copy the Webhook URL.

**Config**: `webhook_url` (secret) — must be `https://discord.com/...`
or `https://discordapp.com/...`; any other host is rejected unless
`CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS=1` (see
[Security notes](#security-notes) below).

**Delivery**: Discord's "Execute Webhook" endpoint, multipart/form-data
— a `payload_json` field carrying an embed (title, description, color
by severity, fields, timestamp, dashboard URL, and
`image: {url: "attachment://chart.png"}` when a chart is attached) plus
a `files[0]` part with the PNG bytes. A `429` response's `retry_after`
is honored by the delivery worker's backoff.

### Telegram

**Setup**:
1. Message [@BotFather](https://t.me/botfather) → `/newbot` → follow the
   prompts → copy the bot token it gives you.
2. Add the bot to the target chat/group/channel (or message it directly
   for a private chat).
3. Send any message to the chat, then open
   `https://api.telegram.org/bot<TOKEN>/getUpdates` in a browser and
   find `"chat":{"id": ...}` in the response — that number is the
   `chat_id`. (The Settings UI's Telegram guide shows this URL
   pre-filled with a copy button once a bot token is entered.)

**Config**: `bot_token` (secret), `chat_id`, optional
`message_thread_id` (for a specific topic in a forum-style group).

**Delivery**: `sendPhoto` (multipart, `photo` file + `caption` ≤ 1024
characters, `parse_mode=HTML`) when a chart is attached, else
`sendMessage` (HTML). `<`, `>`, `&` in message text are escaped to
`&lt;`/`&gt;`/`&amp;` before being sent as HTML. A `429` response's
`parameters.retry_after` (or the `Retry-After` header as a fallback) is
honored.

### WhatsApp (Meta Cloud API)

**Setup**:
1. [Meta for Developers](https://developers.facebook.com) → create an
   app → add the WhatsApp product → API Setup tab.
2. Copy the temporary access token (or generate a permanent one via a
   System User for production use) and the Phone Number ID.
3. Add the recipient's phone number as an allowed tester number (while
   using a temporary/test number) under API Setup → "To."
4. **Recommended**: create and get Meta's approval for a message
   template with an **image header** — this is what lets alerts be
   delivered at any time (see the 24-hour window note below).

**Config**: `access_token` (secret), `phone_number_id`, `to` (E.164
digits only, no `+`/spaces/punctuation), optional `template_name`,
`template_lang` (default `en_US`), `api_version` (default `v21.0`).

**Delivery flow**: the chart PNG is always uploaded first (`POST
/{phone_number_id}/media`, multipart) to get a media ID, then:

- **If `template_name` is set**: sends an approved template message with
  the uploaded image as the header and `[title, text]` as body
  parameters. **Works at any time**, regardless of when the recipient
  last messaged the business.
- **If `template_name` is empty**: sends a plain `type: image` message
  with a text caption. **Only deliverable inside WhatsApp's 24-hour
  customer-service window** — i.e. only if the recipient has messaged
  the business's WhatsApp number within the last 24 hours. Outside that
  window, Meta's API rejects the send. For unattended alerting (the
  common case — nobody is actively messaging the alert bot), configure
  an approved template instead.

### Webhook (generic)

The pre-v0.5 behavior, still available as its own channel type for
Slack-/Discord-compatible or custom receivers: a JSON POST with `text`,
`content` (duplicate of `text`, for Slack/Discord payload-shape
compatibility), `title`, `severity`, `fields`, `url`, and — only when the
channel's `include_image` config is `"true"`/`"1"` — an
`image_png_base64` field carrying the chart as base64-encoded PNG bytes.
Requires `https://` unless custom endpoints are allowed (see below).

### Manual verification checklist

Automated tests use `httptest` fakes for every platform — nothing in the
test suite or CI ever contacts a real Discord/Telegram/WhatsApp
endpoint. Before relying on a channel in production, verify it manually:

1. Create the channel in Settings → Notifications, filling in the
   platform's guide fields exactly as shown.
2. Click **Send test** (or `POST /api/v1/alerts/channels/{id}/test`) —
   confirm the response shows `ok: true` and a real message (with a
   generic sample chart) actually arrives in the target Discord
   channel / Telegram chat / WhatsApp conversation.
3. For WhatsApp specifically: test **both** with and without
   `template_name` set, from a device that has *not* messaged the
   business number in the last 24 hours — the non-template path should
   fail outside the window (confirming the 24h-window caveat is real,
   not just documented), while the template path should still succeed.
4. Create a real low-effort rule (e.g. CPU `>= 0`, duration `0`) scoped
   to a test host, confirm it fires within one report interval, that the
   chart image renders correctly on the receiving platform, and that
   `GET /api/v1/alerts/events` shows the delivery recorded with `ok:
   true` for every attached channel. Delete the test rule afterward.
5. Confirm a resolved notification arrives (if `notify_resolved` is set)
   once the condition clears, and that egress rules do **not** re-fire
   within the same calendar month after dropping and re-crossing a
   threshold.

## Inventory: Docker services + listening ports

Every 60 seconds (independent of the sample-collection interval), the
agent collects a snapshot of listening network sockets and, if Docker or
Podman is available, running containers — attached to an `AgentReport`
as an optional `inventory` field whenever it has changed or every 10
minutes, whichever comes first (an old hub ignores the field entirely;
an old agent simply never sends it — both directions degrade
gracefully).

### Listening ports

Collected via gopsutil's `net.ConnectionsWithContext(ctx, "inet")`: every
TCP socket in `LISTEN` state and every bound UDP socket, deduplicated by
`(proto, ip, port)`, capped at 1000 entries. Each entry carries the
owning process's PID and name **when the agent can resolve them** — since
the agent runs **unprivileged** by design (see
[Security model](#security-model)), it frequently cannot: a process
owned by another user is invisible to `/proc/<pid>/...` lookups without
elevated permissions, so `pid: 0`/`process: ""` for such a port is
expected and not a bug. Running the agent as root (not recommended) or
granting it `CAP_SYS_PTRACE` would resolve more process names, at the
cost of the same privilege-escalation surface described in the Docker
section below.

### Docker / Podman containers

Controlled by `CP_DOCKER` (default `auto`):

- **`auto`**: probe `/var/run/docker.sock` (or `DOCKER_HOST`'s
  `unix://` path, if set) using plain `net/http` over the unix socket —
  no Docker SDK dependency. Calls `GET /version` and `GET
  /containers/json?all=1` against Docker Engine API **v1.41+**, 5-second
  timeout per request.
- **`off`**: Docker collection is disabled outright; no socket is ever
  touched.
- **an explicit path or `unix://` URL**: use that socket instead of the
  default — this is how a **rootless Podman** socket (typically
  `unix:///run/user/<uid>/podman/podman.sock`) is monitored: Podman's
  API is Docker Engine API-compatible, so no separate integration code
  exists or is needed.

Each container reports name, image, state, Docker's raw status text,
a parsed health suffix (`healthy`/`unhealthy`/`starting`, when the
container has a healthcheck), creation time, Compose project/service
(from the `com.docker.compose.project`/`.service` labels, when set by
`docker compose`), and published ports — capped at 500 containers.
Listening ports that match a container's published port carry that
container's short (12-character) ID, letting the dashboard link a raw
port back to the container serving it.

Collection degrades to one of these statuses instead of ever erroring
the whole report:

| Status | Meaning |
|---|---|
| `ok` | Reached the daemon and listed containers successfully |
| `unavailable` | No socket found at the resolved path (Docker/Podman not installed, or `CP_DOCKER=off`) |
| `permission_denied` | Socket exists but the agent's user can't open it (see below) |
| `error` | Reached the socket but the daemon returned something else unexpected |
| `unsupported` | `CP_DOCKER` names a transport this agent build doesn't implement yet (e.g. Windows `npipe://`) |

### The `--docker` installer flag — and its root-equivalence caveat

By default, a freshly installed agent runs as an unprivileged
`cloud-pulse` system user with no group membership beyond its own —
Docker collection will report `permission_denied` against the default
socket (owned `root:docker`) even with `CP_DOCKER=auto`, since the agent
user isn't in the `docker` group. `install-agent.sh --docker` (or the
interactive menu's "Monitor Docker containers?" prompt) fixes this the
same way any Docker-monitoring tool must: it adds the agent's system
user to the host's `docker` group via `usermod -aG docker` and sets
`CP_DOCKER=auto`.

**This is a real, documented security tradeoff, not an oversight**:
membership in the `docker` group is **root-equivalent** on the host —
the Docker Engine API can mount arbitrary host paths into a container,
so anything able to talk to the socket can trivially read/write any file
on the host as root, run arbitrary commands, or escape to a root shell.
The installer's `--docker` help text and the interactive menu's prompt
both say this explicitly before an operator opts in. Weigh this against
the alternative:

- **Rootless Podman** (see above) doesn't have this problem — a
  rootless Podman socket's containers run as the invoking non-root user,
  so pointing `CP_DOCKER` at that socket path gets equivalent inventory
  visibility without ever granting the agent root-equivalent access.
- If you don't need container inventory on a given host, simply don't
  pass `--docker` — the agent still reports listening ports (with
  whatever process names it can resolve unprivileged) and everything
  else exactly as before; only the Docker section of that host's
  inventory reports `permission_denied` or `unavailable`.

## REST API


All responses are JSON. Read endpoints (`GET`, except `/healthz` and static
assets) require signing in (session bearer token) or the optional
`CP_UI_TOKEN` bearer token — see [Sign-in and accounts](#sign-in-and-accounts).
**Admin** endpoints require the same; there is no longer an
"admin_disabled" mode.

| Method & path | Purpose | Auth |
|---|---|---|
| `POST /api/v1/agent/report` | Ingest a batch of samples; response includes `server_time_ms` | Agent token |
| `GET /api/v1/agent/time` | Hub wall clock, for agent NTP-style offset estimation | Agent token |
| `POST /api/v1/auth/login` | Sign in with `admin`/password; issues a session token | None (rate-limited) |
| `POST /api/v1/auth/logout` | Delete the caller's current session | Session or API token |
| `GET /api/v1/auth/me` | Identify the caller (username, must-change flag, auth method) | Session or API token |
| `POST /api/v1/auth/password` | Change the admin password; issues a fresh session, revokes all others | Session or API token |
| `GET /api/v1/auth/sessions` | List active sessions (creation/last-seen/expiry/remote/user agent) | Session or API token |
| `POST /api/v1/auth/sessions/revoke-others` | Sign out every session except the caller's own | Session or API token |
| `GET /api/v1/hosts` | List all hosts with status, latest sample, outbound + inbound egress usage | Session or API token |
| `GET /api/v1/hosts/{id}` | One host's summary | Session or API token |
| `GET /api/v1/hosts/{id}/metrics?range=1h\|6h\|24h\|7d\|30d` | Time series for charts (default `1h`) | Session or API token |
| `GET /api/v1/egress?month=YYYY-MM` | Per-host outbound + inbound egress usage for a month (default current) | Session or API token |
| `GET /api/v1/buckets` | Latest S3/R2 stats, 24h history, collector status | Session or API token |
| `GET /api/v1/version` | Build version/commit/date + self-update check status (see [Update notifications](#update-notifications)) | Session or API token |
| `GET /api/v1/settings` | Hub config summary + per-host limits | Admin |
| `GET /api/v1/settings/agent-token` | Reveal the agent token + a ready-to-run install command | Admin |
| `PUT /api/v1/hosts/{id}/limits` | Set/clear a host's outbound/inbound limit overrides | Admin |
| `PUT /api/v1/settings/alerts` | Set/clear the hub-side alert webhook URL override | Admin |
| `POST /api/v1/settings/alerts/test` | Send a test notification to the effective webhook URL | Admin |
| `GET /api/v1/settings/network` | Adapters, listen config, allowlist, listener status, pending change | Admin |
| `PUT /api/v1/settings/network` | Set the listen config + allowlist (validated; may return `pending`) | Admin |
| `POST /api/v1/settings/network/confirm` | Persist a pending network change | Admin |
| `POST /api/v1/settings/network/revert` | Revert a pending network change immediately | Admin |
| `DELETE /api/v1/settings/network` | Drop the hub-side network override, revert to env config | Admin |
| `GET /api/v1/alerts/channels` | List notify channels, secrets redacted | Admin |
| `POST /api/v1/alerts/channels` | Create a notify channel | Admin |
| `PUT /api/v1/alerts/channels/{id}` | Update a notify channel (omitted/`"***"` secret fields preserve the stored value) | Admin |
| `DELETE /api/v1/alerts/channels/{id}` | Delete a notify channel (does not cascade into rules' `channel_ids`) | Admin |
| `POST /api/v1/alerts/channels/{id}/test` | Send a sample notification to a saved channel | Admin |
| `POST /api/v1/alerts/channels/test` | Send a sample notification using an unsaved (draft) channel config | Admin |
| `GET /api/v1/alerts/rules` | List alert rules | Admin |
| `POST /api/v1/alerts/rules` | Create an alert rule | Admin |
| `PUT /api/v1/alerts/rules/{id}` | Update an alert rule | Admin |
| `DELETE /api/v1/alerts/rules/{id}` | Delete an alert rule and its persisted state-machine rows | Admin |
| `POST /api/v1/alerts/rules/{id}/preview` | Report whether a rule is currently satisfied, per host, without altering state | Admin |
| `GET /api/v1/alerts/events?state=&host=&limit=&before=` | Paginated firing/resolved alert event history | Session or API token |
| `GET /api/v1/alerts/active` | Every currently-firing event (navbar bell) | Session or API token |
| `GET /api/v1/hosts/{id}/inventory` | A host's most recently reported listening ports + Docker containers | Session or API token |
| `GET /healthz` | Liveness check | None |
| `GET /` and static assets | Embedded dashboard | None |

`GET /api/v1/version` fields: `version`/`commit`/`date` (existing build
metadata), `latest_version` (latest release tag known to the hub, `""` if
never checked or `CP_UPDATE_CHECK=false`), `update_available` (bool),
`update_check_enabled` (reflects `CP_UPDATE_CHECK`), `checked_at`
(unix seconds of the last check attempt, `0` if none yet), `check_error`
(omitted unless the last check failed), `release_url` (the GitHub
release page for `latest_version`, `""` if unknown), and
`update_command` (`"sudo cloud-pulse-hub update"`). `GET /api/v1/hosts`
and `GET /api/v1/hosts/{id}` embed an optional `update` object per host
(`available`, `latest`, `self_update`, `command`) computed the same way —
see [Update notifications](#update-notifications). Since v0.5.0 both
also embed optional `containers_running`/`listening_ports` integers
(omitted, not `0`, when the host has never reported an inventory
snapshot at all — see [Inventory](#inventory-docker-services--listening-ports)).

A validation failure on any `POST`/`PUT` alerting endpoint (channels or
rules) responds `400` with `APIError.details` — a `field → message` map
(e.g. `{"error":"validation failed","details":{"metric":"must be one
of cpu, memory, disk, load1, egress_out_pct, egress_in_pct,
host_down"}}`) — so the Settings UI can highlight the offending form
field directly instead of showing only a generic error string.

## Security model

- **Agent authentication**: bearer token compared in constant time
  (SHA-256 both operands, then `crypto/subtle.ConstantTimeCompare`) —
  never a plain `==`/`strings.Compare`.
- **Network access control**: an IP allowlist checked against
  `http.Request.RemoteAddr` only; `X-Forwarded-For` and other
  client-supplied headers are never trusted, so a reverse proxy in front
  of the hub is seen as its own IP, not the original client's. The
  allowlist is now editable live from the dashboard, but a change that
  would exclude the requesting admin's own address is always rejected
  before being applied — see [Network settings](#network-settings).
- **Dashboard sign-in required, always**: every read/write/admin route
  (other than `/healthz`, static assets, agent endpoints, and
  `/auth/login` itself) requires either a valid session bearer token or
  the optional `CP_UI_TOKEN`. There is no more "no `CP_UI_TOKEN` ⇒ open
  read API" or `admin_disabled` mode from v0.3.x — see
  [Sign-in and accounts](#sign-in-and-accounts).
- **Password hashing**: PBKDF2-HMAC-SHA256, 600,000 iterations, a fresh
  random 16-byte salt per hash (Go standard library `crypto/pbkdf2`, no
  third-party crypto dependency). Verification always performs the full
  computation, including for an unknown username or a missing/malformed
  stored hash, so there is no timing signal distinguishing "wrong
  username" from "wrong password for a real username."
- **Sessions are hashed at rest**: the database stores only
  `sha256(token)`, never the bearer token itself — a stolen database
  backup does not expose usable session tokens. Sessions slide forward
  on activity (7-day idle window) but expire absolutely 30 days after
  creation regardless of activity.
- **Login/password-change rate limiting**: 5 failures from the same
  client IP (host only, port stripped) within 15 minutes locks that
  client out, starting at 1 minute and doubling per further lockout up
  to 15 minutes; a fleet-wide 30-failures/minute threshold locks out
  every client for 60 seconds. At most 2 PBKDF2 computations run
  concurrently, bounding CPU usage under a login burst. See
  [Sign-in and accounts](#sign-in-and-accounts).
- **Must-change-password gate**: a session whose account still has the
  default (or otherwise flagged) password can reach only
  `/api/v1/auth/me`, `/api/v1/auth/password`, and `/api/v1/auth/logout`
  — every other route responds `403 password_change_required` until the
  password is changed.
- **Well-known default password, by design, with mitigations**: a fresh
  hub always bootstraps `admin`/`changeme` so first-run setup never
  requires an out-of-band secret exchange — but this is a real exposure
  if the hub is reachable before the operator signs in. Mitigations:
  the must-change gate above forces a password change on the very first
  login before any other route is usable; the hub logs a warning on
  every startup while the default is still in effect, escalated to an
  error-level warning when `CP_ALLOWED_CIDRS` also allows every address
  (i.e. the default password is reachable from anywhere the hub is
  exposed); and the default network allowlist restricts access to
  Tailscale/loopback ranges only, so on a stock install the exposure
  window is bounded to the tailnet, not the public internet. Operators
  who widen the allowlist should treat signing in and changing the
  password as the very next step, not an eventual one.
- **Optional API token**: `CP_UI_TOKEN`, when set, authenticates
  scripts/CI as `api_token` with full read/admin access, bypassing the
  sign-in flow and the must-change gate — but it never gates the
  dashboard's own login, and it is not a substitute for changing the
  default admin password.
- **Network settings changes are reversible by construction**: no
  listen/allowlist change can be applied in a way that locks out the
  admin making it — see the lock-out/bind-failure/pending/auto-revert
  rules in [Network settings](#network-settings).
- **Security headers** on every response: CSP (`script-src 'self'`, no
  inline scripts, `frame-ancestors 'none'`), `X-Content-Type-Options:
  nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`.
- **Hardened systemd units**: `NoNewPrivileges`, `ProtectSystem=strict`,
  `ProtectHome=read-only`, `PrivateTmp`, `PrivateDevices`,
  `ProtectKernelTunables`, `ProtectControlGroups`, `RestrictSUIDSGID`,
  `LockPersonality`, empty capability set, `RestrictAddressFamilies=AF_INET
  AF_INET6 AF_UNIX` on the agent (network metrics are read from `/proc`,
  not netlink sockets) and `RestrictAddressFamilies=AF_INET AF_INET6
  AF_UNIX AF_NETLINK` on the hub (the extra `AF_NETLINK` is required by
  `net.Interfaces()` for the Network settings page's adapter list).
- **No secrets logged**: tokens, webhook URLs, and passwords are logged
  only as `(set)`/`(not set)` or never at all (failed logins log the
  remote address only), including in `-check-config`
  output.
- **Notify-channel secrets stored in the hub's SQLite database, not
  separately encrypted**: a channel's `bot_token`/`access_token`/
  `webhook_url` is persisted as plain JSON inside the `notify_channels`
  table (same file as every other hub table, `cloud-pulse.db`) — there
  is no separate secrets store or at-rest encryption layer, matching the
  project's "one embedded SQLite file, no external dependency" design
  (see [D-005](DECISIONS_LOG.md)). The database file's confidentiality
  therefore rests entirely on **filesystem permissions**: `CP_DATA_DIR`
  is created (if missing) with mode `0750`, and the systemd-installed
  hub runs as the unprivileged `cloud-pulse` system user, so only that
  user (and root) can read `cloud-pulse.db` on a stock install. A
  `cloud-pulse-hub reset-password`/`reset-network` invocation run as
  root restores the data directory owner's ownership on the DB file (and
  any `-wal`/`-shm` sidecars) afterward for the same reason — see
  [Sign-in and accounts](#sign-in-and-accounts). Anyone with read access
  to the data directory (a root shell, a misconfigured backup, a copied
  `data/` directory) can read every stored notify-channel secret in
  plaintext; treat the data directory with the same care as `hub.env`.
- **Secret redaction is applied only at the API boundary, never at
  rest**: `NotifyChannel.Redacted()` (`internal/models/alerting.go`)
  swaps every `Type.SecretFields()` value to `"***"` before a channel is
  ever serialized into an HTTP response (`GET/POST/PUT
  /api/v1/alerts/channels*`) — but the underlying `Store` methods
  (`ListNotifyChannels`, `GetNotifyChannel`) always return the
  *unredacted* row; every handler is responsible for calling
  `.Redacted()` itself before writing a response. A `PUT` request that
  omits a secret field, or sends back the literal string `"***"`, is
  detected by `mergePreservedSecrets` and the previously stored value is
  kept rather than being overwritten with the redaction placeholder
  (`internal/hub/alertroutes.go`) — this is what lets the Settings UI's
  edit form round-trip a channel without ever re-transmitting a secret
  it can't see.
- **SSRF guard on Discord/Telegram/WhatsApp notify channels**: by
  default, each of these three senders validates its configured
  endpoint against a **fixed allowlist of official hosts**
  (`discord.com`/`discordapp.com`, `api.telegram.org`,
  `graph.facebook.com`) and requires `https://`
  (`internal/notify/ssrf.go`'s `validateEndpointHost`) — a channel
  cannot be pointed at an arbitrary internal-network URL, which matters
  because the hub's alert-delivery HTTP client runs with hub-level
  network access, not the browser's. Telegram/WhatsApp additionally
  accept a channel-config `api_base` override (for pointing at a
  self-hosted Bot API relay or an enterprise Graph API gateway) but only
  when `CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS=1` is set in the hub's own
  environment — with it unset (the default), an `api_base` override is
  rejected outright at channel-save time, and Discord has no override
  field at all (its webhook URL already fully determines the endpoint).
  The generic `webhook` channel type has no fixed-host allowlist (it is
  explicitly user-configurable to point anywhere) but still requires
  `https://` unless the same env var relaxes it. **This variable exists
  for tests and CI fakes, not production use** — flipping it on a real
  hub with real credentials configured removes the one guard preventing
  a compromised or careless channel config from turning the hub into an
  arbitrary internal HTTP client.

## Development

```bash
make hooks   # git config core.hooksPath .githooks (run once after cloning)
make build   # build bin/cloud-pulse-hub + bin/cloud-pulse-agent
make test    # go test ./... (CGO_ENABLED=0)
make lint    # full pre-commit check suite on demand
make css     # rebuild web/assets/app.css from web/src/input.css via Tailwind CLI
```

Conventions (Go style, error handling, Clean Architecture layering, shell
script rules, commit format) are documented in
[CODING_CONVENTIONS.md](CODING_CONVENTIONS.md). Design/architecture
decisions and their rationale are in [DECISIONS_LOG.md](DECISIONS_LOG.md).
Contributing (setup, the pre-commit hook, test conventions, how to add a
notifier/metric/page, commit format) is documented in
[CONTRIBUTING.md](CONTRIBUTING.md).

Other useful scripts:

- `bash scripts/smoke.sh` — end-to-end hub+agent smoke test (real
  binaries, real HTTP, real SQLite; covers alerting/notify channels,
  chart-image delivery, and the inventory endpoint alongside the pre-
  v0.5.0 host/egress/bucket assertions).
- `bash scripts/test-install.sh` — sandboxed install-script test suite
  (checksum verification, injection/RCE regression tests,
  upgrade/uninstall/purge, systemd unit validation, incl. the
  `--docker` flag's group-membership + unit-line assertions).
- `python3 scripts/demo-seed.py --hub <url> --token <token> [--db <path>]` —
  seed a running hub with demo hosts and bucket stats for local UI
  development/screenshots.

## Releasing

Push a tag matching `vX.Y.Z` (or run the Release workflow manually with a
`tag` input). The release workflow builds all 8 target platforms
(`linux/{amd64,arm64,armv7}`, `darwin/{amd64,arm64}`,
`windows/{amd64,arm64}`, `freebsd/amd64`), generates `checksums.txt`, and
publishes them as flat release assets — no archives.

## Comparison / inspiration

cloud-pulse takes direct inspiration from [Beszel](https://github.com/henrygd/beszel),
a lightweight server monitoring hub/agent written in Go. The main
differences: cloud-pulse agents **push** metrics over HTTP(S) with a
pre-shared token instead of the hub pulling over SSH (simpler behind
NAT/CGNAT, especially on a Tailscale mesh), and cloud-pulse adds
first-class cloud egress accounting and S3/R2 object-storage monitoring
that Beszel doesn't target.

## License

[MIT](LICENSE)
