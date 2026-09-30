# Install

cloud-pulse ships single static binaries (`CGO_ENABLED=0`) for the hub
and agent, plus platform-specific installer scripts that handle
download, checksum verification, service registration, and menu-driven
or non-interactive setup. See [../README.md](../README.md) for the
one-line quick-start commands; this page covers every option and every
supported platform in detail.

## Hub (Linux only)

The hub currently installs only on Linux via systemd:

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-hub.sh \
  | sudo bash
```

Run at a real terminal with no flags to get an interactive menu (all
prompts read from `/dev/tty`, never from stdin):

```text
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
- **2) Reinstall** is the upgrade/migration path — see
  [upgrade.md](upgrade.md).
- **3) Uninstall** confirms, then asks separately whether to also purge
  config/tokens/data.

Piping any flag, or running with no tty (CI, `curl | bash -s --
<flag>`, cron), skips the menu and never blocks:

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-hub.sh \
  | sudo bash -s -- --install --yes
```

Flags: `--install` (fails if already installed), `--reinstall` (fails if
not installed), `--uninstall` (`--purge` to also remove config/data),
`-y`/`--yes` (assume default confirmation answers), `--dry-run` (print
actions without changing anything), `--ui-token TOKEN`,
`--generate-ui-token` /
`--rotate-ui-token` (see [configuration.md](configuration.md#settings-ui-token)).
Run `install-hub.sh --help` for the full list.

The installer downloads the correct `cloud-pulse-hub` release binary
for your OS/arch, verifies its sha256 against the release's
`checksums.txt`, installs it under `/usr/local/bin`, creates an
unprivileged `cloud-pulse` system user, generates `CP_AGENT_TOKEN`,
writes `/etc/cloud-pulse/hub.env` (mode `0640`), and installs+starts a
hardened systemd unit. At the end it prints an agent one-liner with the
hub's Tailscale/LAN IP and the agent token filled in.

## Agent — Linux (systemd)

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh \
  | sudo bash -s -- --hub-url http://<hub-ip>:8090 --token <printed-token>
```

Same menu/non-interactive split as the hub installer. Flags:
`--install`, `--reinstall`, `--hub-url URL` (required for a fresh
install), `--token TOKEN` (required for a fresh install), `--host-id
ID`, `--interval DURATION`, `--provider auto|aws|oci|other`,
`--egress-limit-gb N`, `--docker` (see
[storage.md](storage.md#docker--podman-inventory-privilege-tradeoff)
for its root-equivalent privilege tradeoff — this is about Docker
container inventory, unrelated to Google Drive/Dropbox storage
accounts), `--remote-update` (opt in to hub-triggered updates, see
[remote-updates.md](remote-updates.md)), `--version vX.Y.Z`, `--prefix
DIR`, `--uninstall`, `--purge`, `-y`/`--yes`, `--dry-run`.

## Agent — macOS (launchd)

Same script, auto-detected platform:

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh \
  | sudo bash -s -- --hub-url http://<hub-ip>:8090 --token <printed-token>
```

`install-agent.sh` runs correctly under macOS's stock `/bin/bash` (3.2)
and detects `darwin-launchd` automatically. It creates a dedicated
hidden user `_cloudpulse` (via `dscl`, reusing an existing one if
present) and installs:

| Path | Owner | Mode |
|---|---|---|
| `/usr/local/bin/cloud-pulse-agent` | root | 0755 |
| `/usr/local/etc/cloud-pulse/agent.env` | root:_cloudpulse | 0640 |
| `/Library/Application Support/cloud-pulse-agent/` (update request) | _cloudpulse | 0750 |
| `/Library/Application Support/cloud-pulse-agent-update/` (update result) | root | 0755 |
| `/Library/LaunchDaemons/com.cloudpulse.agent.plist` | root | 0644 |
| `/Library/Logs/cloud-pulse-agent.log` | _cloudpulse:_cloudpulse | 0640 |
| `/Library/Logs/cloud-pulse-agent-update.log` | root | 0640 |

The LaunchDaemon `com.cloudpulse.agent` runs as `_cloudpulse` with
`KeepAlive`/`RunAtLoad`, logging to `/Library/Logs/cloud-pulse-agent.log`.
Because launchd has no `EnvironmentFile=` equivalent, the plist passes
`--env-file /usr/local/etc/cloud-pulse/agent.env` directly to the agent
binary, which parses it itself (`internal/config.ParseEnvFile` — plain
`KEY=VALUE`, no shell interpretation, CRLF-tolerant). Passing
`--remote-update` additionally installs `com.cloudpulse.agent-update`
(root, `WatchPaths` on the update-request file), which writes helper
output to `/Library/Logs/cloud-pulse-agent-update.log` — see
[remote-updates.md](remote-updates.md).

Service control uses `launchctl bootstrap system <plist>` /
`bootout` / `kickstart -k`.

## Agent — Windows (service)

PowerShell 5.1+, run as Administrator:

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.ps1))) -Install -HubUrl http://<hub-ip>:8090 -Token <printed-token> -Yes
```

With no flags at all, it shows the same 1/2/3 interactive menu as the
Bash installers (reading from the console, never from piped stdin).
Flags: `-Install`, `-Reinstall`, `-Uninstall`, `-Purge`, `-Yes`,
`-HubUrl`, `-Token` (or `-TokenFile <path>` to avoid the token appearing
in shell history, or omit both for an interactive
`Read-Host -AsSecureString` prompt), `-HostId`, `-RemoteUpdate`,
`-Version vX.Y.Z`.

Paths and permissions:

| Path | Access |
|---|---|
| `C:\Program Files\cloud-pulse\cloud-pulse-agent.exe` | Administrators/SYSTEM |
| `C:\ProgramData\cloud-pulse\agent.env` | SYSTEM/Administrators full; service SID read-only |
| `C:\ProgramData\cloud-pulse-agent\` (update request) | service SID can write |
| `C:\ProgramData\cloud-pulse-agent-update\` (update result) | SYSTEM/Administrators full; service SID read-only |
| `C:\ProgramData\cloud-pulse\logs\agent.log` | rotating agent service log (5 MiB current + `.1`) |
| `C:\ProgramData\cloud-pulse\logs\updater.log` | rotating LocalSystem updater log (5 MiB current + `.1`) |

The agent runs as service `cloud-pulse-agent` under the virtual account
`NT SERVICE\cloud-pulse-agent` (implemented on
`golang.org/x/sys/windows/svc`/`svc/mgr`), with a 5-second restart-on-
failure recovery policy. `-RemoteUpdate` additionally installs and
starts `cloud-pulse-agent-updater` (LocalSystem, automatic start),
which polls the update-request file every 30 seconds — Windows has no
filesystem-watch trigger as simple as launchd's `WatchPaths` or
systemd's path units, so polling is the deliberately simplest safe
choice here. See [remote-updates.md](remote-updates.md).

Manual service control: `cloud-pulse-agent.exe service
install|uninstall|start|stop|status [--env-file PATH] [--bin-path PATH]`.

Running `install-agent.sh` from Git Bash/MSYS on Windows prints a
redirect message pointing at `install-agent.ps1` and exits — it does
not attempt anything on that platform.

## FreeBSD and other platforms

The agent builds for `freebsd/amd64` (see the release asset list) but
FreeBSD has no installer script or service integration in this release
— run the binary manually with environment variables set directly, and
manage the process with whatever init system you already use.
Remote updates are unsupported on FreeBSD regardless of agent version
(see [remote-updates.md](remote-updates.md#limitations)).

## Uninstall

| Platform | Command |
|---|---|
| Hub (Linux) | Interactive menu **3) Uninstall**, or `install-hub.sh --uninstall [--purge]` |
| Agent (Linux/macOS) | Interactive menu **3) Uninstall**, or `install-agent.sh --uninstall [--purge]` |
| Agent (Windows) | `install-agent.ps1 -Uninstall [-Purge]` |

Without `--purge`/`-Purge`, the binary and service registration are
removed but `hub.env`/`agent.env` and any data directory are kept (so a
later reinstall picks the same configuration back up). With
`--purge`/`-Purge`, config and data are removed too — this is
irreversible for local data (the hub's SQLite database, agent's env
file); back up `/var/lib/cloud-pulse` (Linux hub) or the platform's
`ProgramData`/`Application Support` config paths first if you might
need them again.

## Dry run

Every installer accepts `--dry-run`/`-Install ... -WhatIf`-equivalent
(`-Install` plus `$env:CP_INSTALL_ROOT` sandboxing is the closer
analog on Windows — see the script's own `-InstallRoot` parameter) to
print the actions that would be taken without changing anything.
