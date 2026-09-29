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
- **Settings UI** (`#/settings`) to reveal the agent token and copy a
  ready-to-run install command, edit per-host egress/ingress limit
  overrides, and manage the alert webhook URL — gated behind
  `CP_UI_TOKEN`.
- **Cloud object storage monitoring**: Amazon S3 via CloudWatch, Cloudflare
  R2 via GraphQL Analytics, collected every 15 minutes by default.
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
6. If you widen `CP_ALLOWED_CIDRS` (e.g. to `*` for a non-Tailscale
   deployment), also set `CP_UI_TOKEN` so the read API and dashboard
   require a bearer token — otherwise the hub warns loudly at startup
   that it is unauthenticated and reachable from anywhere it's exposed.

## AWS S3 setup

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
| `CP_LISTEN` | `:8090` | HTTP listen address |
| `CP_DATA_DIR` | `./data` | Directory holding `cloud-pulse.db` |
| `CP_AGENT_TOKEN` | *(required)* | ≥16 chars; authenticates agent report ingestion |
| `CP_UI_TOKEN` | *(unset)* | ≥8 chars if set; authenticates read endpoints + dashboard |
| `CP_ALLOWED_CIDRS` | `100.64.0.0/10,fd7a:115c:a1e0::/48,127.0.0.0/8,::1/128` | Comma-separated CIDRs/IPs allowed to reach the hub; `*` disables the allowlist |
| `CP_OFFLINE_AFTER` | `60s` | Host reported "down" after this long since last-seen |
| `CP_CLOUD_INTERVAL` | `15m` (min `1m`) | Interval between S3/R2 collections |
| `CP_ALERT_WEBHOOK_URL` | *(unset)* | Slack- or Discord-compatible webhook for egress alerts |
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
output, checked by a drift test in `scripts/test-install.sh`).

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
`systemd-unit print`, see below), and prints `Upgraded vA → vB` plus,
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

**Why it requires `CP_UI_TOKEN`**: the agent token grants write access to
ingest metrics for any host, so it must never be exposed to an
unauthenticated request. All settings/token endpoints are **admin
endpoints**: they require `CP_UI_TOKEN` to be configured on the hub *and*
presented as a valid bearer token. If `CP_UI_TOKEN` is not configured,
they respond `403` with `{"code": "admin_disabled", ...}` rather than
ever falling back to an open read — there is no way to reveal the agent
token or change limits/webhook settings on a hub that hasn't opted into
UI authentication.

**Enabling it on an existing install**: choose **2) Reinstall** from the
menu (prompts "The web Settings page is disabled (no UI token). Enable
it now?" when one isn't already set), or pass a flag non-interactively:

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
  is printed exactly once in the final summary (`Web UI token: ...`);
  an unchanged existing token instead prints a hint to read it from
  `hub.env` (`sudo grep CP_UI_TOKEN /etc/cloud-pulse/hub.env`) rather
  than ever re-printing its value.

Manual alternative (no installer re-run): generate a token with
`cloud-pulse-hub -gen-token`, add `CP_UI_TOKEN=<token>` to
`/etc/cloud-pulse/hub.env`, then `sudo systemctl restart cloud-pulse-hub`.
Once set, every read endpoint and the dashboard itself will prompt for
that token, and the Settings page becomes reachable.

## REST API

All responses are JSON. Read endpoints (`GET`, except `/healthz` and static
assets) require `Authorization: Bearer <CP_UI_TOKEN>` only when
`CP_UI_TOKEN` is set. **Admin** endpoints additionally require
`CP_UI_TOKEN` to be configured at all — see [Settings UI](#settings-ui).

| Method & path | Purpose | Auth |
|---|---|---|
| `POST /api/v1/agent/report` | Ingest a batch of samples; response includes `server_time_ms` | Agent token |
| `GET /api/v1/agent/time` | Hub wall clock, for agent NTP-style offset estimation | Agent token |
| `GET /api/v1/hosts` | List all hosts with status, latest sample, outbound + inbound egress usage | UI token (if set) |
| `GET /api/v1/hosts/{id}` | One host's summary | UI token (if set) |
| `GET /api/v1/hosts/{id}/metrics?range=1h\|6h\|24h\|7d\|30d` | Time series for charts (default `1h`) | UI token (if set) |
| `GET /api/v1/egress?month=YYYY-MM` | Per-host outbound + inbound egress usage for a month (default current) | UI token (if set) |
| `GET /api/v1/buckets` | Latest S3/R2 stats, 24h history, collector status | UI token (if set) |
| `GET /api/v1/version` | Build version/commit/date + self-update check status (see [Update notifications](#update-notifications)) | UI token (if set) |
| `GET /api/v1/settings` | Hub config summary + per-host limits | Admin |
| `GET /api/v1/settings/agent-token` | Reveal the agent token + a ready-to-run install command | Admin |
| `PUT /api/v1/hosts/{id}/limits` | Set/clear a host's outbound/inbound limit overrides | Admin |
| `PUT /api/v1/settings/alerts` | Set/clear the hub-side alert webhook URL override | Admin |
| `POST /api/v1/settings/alerts/test` | Send a test notification to the effective webhook URL | Admin |
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
see [Update notifications](#update-notifications).

## Security model

- **Agent authentication**: bearer token compared in constant time
  (SHA-256 both operands, then `crypto/subtle.ConstantTimeCompare`) —
  never a plain `==`/`strings.Compare`.
- **Network access control**: an IP allowlist checked against
  `http.Request.RemoteAddr` only; `X-Forwarded-For` and other
  client-supplied headers are never trusted, so a reverse proxy in front
  of the hub is seen as its own IP, not the original client's.
- **Optional UI token**: `CP_UI_TOKEN` gates all read endpoints and the
  dashboard when set.
- **Admin endpoints require `CP_UI_TOKEN` to be configured at all**:
  settings/limits/webhook/agent-token endpoints respond `403
  {"code":"admin_disabled"}` when `CP_UI_TOKEN` is unset, and `401` for a
  missing/wrong bearer token when it is set — there is no unauthenticated
  path to the agent token or write access to limits/webhook settings.
  Bearer-header auth only, no cookies, so there's no CSRF surface.
- **Security headers** on every response: CSP (`script-src 'self'`, no
  inline scripts, `frame-ancestors 'none'`), `X-Content-Type-Options:
  nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`.
- **Hardened systemd units**: `NoNewPrivileges`, `ProtectSystem=strict`,
  `ProtectHome=read-only`, `PrivateTmp`, `PrivateDevices`,
  `ProtectKernelTunables`, `ProtectControlGroups`, `RestrictSUIDSGID`,
  `LockPersonality`, empty capability set, `RestrictAddressFamilies=AF_INET
  AF_INET6 AF_UNIX` (neither binary needs `AF_NETLINK` — network metrics
  are read from `/proc`, not netlink sockets).
- **No secrets logged**: tokens and webhook URLs are logged only as
  `(set)`/`(not set)`, never their value, including in `-check-config`
  output.

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

Other useful scripts:

- `bash scripts/smoke.sh` — end-to-end hub+agent smoke test (real
  binaries, real HTTP, real SQLite).
- `bash scripts/test-install.sh` — sandboxed install-script test suite
  (67 assertions: checksum verification, injection/RCE regression tests,
  upgrade/uninstall/purge, systemd unit validation).
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
