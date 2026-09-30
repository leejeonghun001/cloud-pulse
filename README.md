# cloud-pulse

Ultra-lightweight, CGO-free server monitor for small fleets — a
Beszel-inspired hub + agent, one static binary each, no runtime
dependencies, an embedded SQLite database, and a dashboard baked into
the binary.

[![CI](https://github.com/leejeonghun001/cloud-pulse/actions/workflows/ci.yml/badge.svg)](https://github.com/leejeonghun001/cloud-pulse/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/leejeonghun001/cloud-pulse)](https://github.com/leejeonghun001/cloud-pulse/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25%2B-00ADD8)](go.mod)

![cloud-pulse fleet overview](docs/screenshots/overview.png)

More screenshots: [host detail](docs/screenshots/host-detail.png) ·
[mobile](docs/screenshots/mobile.png) ·
[settings](docs/screenshots/settings.png)

## Features

- **15-second host metrics**: CPU, memory (true "available"), disk
  usage + IO, network rates, load average — pushed by a single-binary
  agent over HTTP(S). Runs on **Linux, macOS, and Windows**.
- **Outbound and inbound egress accounting**, tracked and alerted on
  separately, with provider-aware free-tier defaults (AWS 100 GB, OCI
  10 TB), overridable per host from the hub.
- **Hub-synchronized, aligned sampling**: a whole fleet's samples share
  identical timestamps regardless of each host's own clock drift.
- **Dashboard sign-in**: a single `admin` account, PBKDF2-hashed
  password, server-side sessions, per-IP + fleet-wide login rate
  limiting, and a `reset-password` recovery CLI.
- **Settings UI**: reveal the agent token and copy a ready-to-run
  install command (per OS), edit per-host egress/ingress limit
  overrides, manage notification channels, and configure the hub's own
  listen address(es) and access allowlist.
- **Cloud object storage monitoring**: Amazon S3 (via CloudWatch),
  Cloudflare R2 (via GraphQL Analytics), and — since v0.7.0 — personal
  **Google Drive / Dropbox** quota via OAuth.
- **Cloud cost polling**: AWS Cost Explorer / OCI Usage API via the
  `aws`/`oci` CLI, plus a configurable network-egress cost estimate
  independent of real billing.
- **Alerting** with chart images to Discord, Telegram, and WhatsApp:
  configurable rules (CPU/memory/disk/load average/egress/host-down/
  storage-usage) through a persistent state machine.
- **Inventory**: Docker/Podman containers and listening ports per host.
- **Remote agent updates**: trigger `update` on a batch of hosts from
  the dashboard (Linux >= v0.6.0, macOS/Windows >= v0.7.0), opt-in only,
  request-only from the hub's side.
- **Single static binaries**, `CGO_ENABLED=0` always, no AWS SDK
  (hand-written SigV4), embedded SQLite with rollups and automatic
  retention pruning.
- **Embedded dashboard** — vanilla ES modules + vendored uPlot charts +
  prebuilt Tailwind CSS, no Node.js needed to run the hub, no CDN calls
  at runtime, responsive down to a 360px viewport, light/dark themes.

## Architecture

Agents push metrics to the hub over HTTP(S) authenticated with a
pre-shared bearer token — no inbound access to agents is required, so
it works behind NAT/CGNAT (a [Tailscale](https://tailscale.com) mesh is
recommended). See [docs/architecture.md](docs/architecture.md) for the
full diagram and package layering.

## Quick start

### Hub (Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-hub.sh \
  | sudo bash
```

Run at a real terminal with no flags for an interactive menu; pipe a
flag (e.g. `-- --install --yes`) for non-interactive/CI use. See
[docs/install.md](docs/install.md) for every flag and both paths in
detail.

### Agent — Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh \
  | sudo bash -s -- --hub-url http://<hub-ip>:8090 --token <printed-token>
```

Same script auto-detects Linux (systemd) vs. macOS (launchd, stock
`/bin/bash` 3.2 compatible).

### Agent — Windows

Run as Administrator in PowerShell:

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.ps1))) -Install -HubUrl http://<hub-ip>:8090 -Token <printed-token> -Yes
```

Registers `cloud-pulse-agent` as a Windows service under a virtual
service account. See [docs/install.md](docs/install.md) for paths,
permissions, and the `-RemoteUpdate` opt-in.

### Upgrade

```bash
sudo cloud-pulse-hub update
sudo cloud-pulse-agent update       # Linux/macOS
```

```powershell
cloud-pulse-agent.exe update        # Windows, elevated
```

Upgrade the hub first, then agents. Full legacy-version migration path
(pre-v0.3.1) and per-OS notes: [docs/upgrade.md](docs/upgrade.md).

### Uninstall

```bash
curl -fsSL .../install-hub.sh | sudo bash -s -- --uninstall [--purge]
curl -fsSL .../install-agent.sh | sudo bash -s -- --uninstall [--purge]
```

```powershell
& ([scriptblock]::Create((irm .../install-agent.ps1))) -Uninstall [-Purge] -Yes
```

Details: [docs/install.md](docs/install.md#uninstall).

## Documentation

| Doc | Covers |
|---|---|
| [docs/install.md](docs/install.md) | Every install flag, every platform, uninstall |
| [docs/upgrade.md](docs/upgrade.md) | Upgrade flows (current and legacy), update notifications |
| [docs/configuration.md](docs/configuration.md) | Every `CP_*` env var and CLI flag, hub and agent |
| [docs/security.md](docs/security.md) | Sign-in, sessions, network settings, the full security model |
| [docs/billing.md](docs/billing.md) | AWS/OCI cloud cost polling, network cost estimates, KRW display |
| [docs/storage.md](docs/storage.md) | Google Drive/Dropbox OAuth setup, app registration, privacy |
| [docs/alerting.md](docs/alerting.md) | Alert rule model, state machine, chart images |
| [docs/notifications.md](docs/notifications.md) | Discord/Telegram/WhatsApp/webhook setup, real-account E2E verification |
| [docs/remote-updates.md](docs/remote-updates.md) | Batch agent updates, security model, per-platform mechanics |
| [docs/api.md](docs/api.md) | Full REST API reference |
| [docs/architecture.md](docs/architecture.md) | Package layering, time sync, egress accounting, inventory |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Common problems and how to diagnose them |

Contributing (setup, adding a metric/notifier/storage provider/page,
release process): [CONTRIBUTING.md](CONTRIBUTING.md). Architecture and
security conventions: [CODING_CONVENTIONS.md](CODING_CONVENTIONS.md).
Design decisions and their rationale: [DECISIONS_LOG.md](DECISIONS_LOG.md).
Security vulnerability reporting: [SECURITY.md](SECURITY.md).

```bash
make hooks   # git config core.hooksPath .githooks (run once after cloning)
make build   # build bin/cloud-pulse-hub + bin/cloud-pulse-agent
make test    # go test ./... (CGO_ENABLED=0)
make lint    # full pre-commit check suite on demand
```

## Comparison / inspiration

cloud-pulse takes direct inspiration from
[Beszel](https://github.com/henrygd/beszel), a lightweight server
monitoring hub/agent written in Go. The main differences: cloud-pulse
agents **push** metrics over HTTP(S) with a pre-shared token instead of
the hub pulling over SSH (simpler behind NAT/CGNAT, especially on a
Tailscale mesh), and cloud-pulse adds first-class cloud egress
accounting, S3/R2/Google Drive/Dropbox storage monitoring, and cloud
cost polling that Beszel doesn't target.

## License

[MIT](LICENSE)
