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

This downloads the correct `cloud-pulse-hub` release binary for your
OS/arch, verifies its sha256 against the release's `checksums.txt`,
installs it under `/usr/local/bin`, creates an unprivileged `cloud-pulse`
system user, generates `CP_AGENT_TOKEN`, writes `/etc/cloud-pulse/hub.env`
(mode `0640`), and installs+starts a hardened systemd unit. At the end it
prints a ready-to-copy agent install one-liner with the hub's Tailscale/LAN
IP and the generated token filled in:

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh \
  | sudo bash -s -- --hub-url http://<hub-ip>:8090 --token <printed-token>
```

- **Upgrade**: re-run the same installer command; the binary and systemd
  unit are replaced, `hub.env`/`agent.env` secrets are preserved unless you
  pass an explicit flag to change them.
- **Uninstall**: add `--uninstall` (stops/disables the service, removes the
  binary and unit; config and data are kept). Add `--purge` as well to also
  remove `/etc/cloud-pulse/*.env` and (hub only) the data directory.
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

**Enabling it on an existing install** (one that was set up without
`CP_UI_TOKEN`):

1. Generate a token: `cloud-pulse-hub -gen-token`.
2. Add `CP_UI_TOKEN=<token>` to `/etc/cloud-pulse/hub.env`.
3. `sudo systemctl restart cloud-pulse-hub`.

(Or re-run `install-hub.sh` with a flag that supplies/generates a UI
token, per its `--help` output.) Once set, every read endpoint and the
dashboard itself will prompt for that token, and the Settings page
becomes reachable.

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
| `GET /api/v1/version` | Build version/commit/date | UI token (if set) |
| `GET /api/v1/settings` | Hub config summary + per-host limits | Admin |
| `GET /api/v1/settings/agent-token` | Reveal the agent token + a ready-to-run install command | Admin |
| `PUT /api/v1/hosts/{id}/limits` | Set/clear a host's outbound/inbound limit overrides | Admin |
| `PUT /api/v1/settings/alerts` | Set/clear the hub-side alert webhook URL override | Admin |
| `POST /api/v1/settings/alerts/test` | Send a test notification to the effective webhook URL | Admin |
| `GET /healthz` | Liveness check | None |
| `GET /` and static assets | Embedded dashboard | None |

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
