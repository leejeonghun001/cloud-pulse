# Upgrade

## v0.3.1 and later: reinstall once, then `update` forever

Starting with v0.3.1, `update` handles both the binary and the service
definition (systemd unit / launchd plist / Windows service), so
re-running the installer is not needed again once you've reached
v0.3.1. If you're upgrading an existing install of *any* pre-v0.3.1
version, run the installer once more — interactively choose **2)
Reinstall**, or pass `--reinstall`/`-Reinstall`:

```bash
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-hub.sh \
  | sudo bash -s -- --reinstall
curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh \
  | sudo bash -s -- --reinstall
```

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.ps1))) -Reinstall -Yes
```

This keeps every existing token/setting/data file untouched,
re-renders the service definition, restarts the service if already
running (or starts it if inactive), and prints `Upgraded vA -> vB`.
From that point on:

```bash
sudo cloud-pulse-hub update
sudo cloud-pulse-agent update       # Linux/macOS
```

```powershell
cloud-pulse-agent.exe update        # Windows, elevated PowerShell
```

Flags (identical across all three): `--check` (report only, exit `10`
if an update is available, `0` if current), `--version vX.Y.Z` (install
an exact tag, allows downgrade), `--no-restart` (skip the
service-restart step). Exit codes: `0` success/already current, `10`
(`--check` only) update available, `1` error.

What `update` does, in order: resolves the latest release tag (or
`--version`'s tag) via GitHub's `.../releases/latest` redirect (no API
rate limiting), downloads the matching asset + `checksums.txt`,
verifies sha256, runs the new binary's own `-version` to confirm the
tag before installing, atomically replaces the running binary, applies
any service-definition drift (systemd unit / launchd plist / Windows
service config) via the newly-installed binary's own
`systemd-unit apply` / `plist apply` / `service install`, then restarts
the service if it was already active (left stopped if it was inactive).

Recommended order for a fleet: **upgrade the hub first, then agents**.
Old agents keep working against a new hub unmodified, so there is no
requirement to upgrade every agent in lockstep.

## Upgrading to v0.7.0

A plain `sudo cloud-pulse-hub update` (no reinstall needed if you're
already on v0.3.1+) replaces the binary and applies migration
`0006_v07.sql` (storage accounts/snapshots + `update_jobs.platform`
column) automatically on next hub startup — existing hosts, alert
rules, notify channels, pricing plans, and audit log entries are
preserved untouched.

**Linux agents**: `sudo cloud-pulse-agent update` is enough to reach
v0.7.0. The remote-update capability gate widens from "Linux only" to
"Linux >= v0.6.0 OR macOS/Windows >= v0.7.0" — a Linux agent already
opted in via `CP_REMOTE_UPDATE=on` keeps working with no changes.

**macOS/Windows agents**: these platforms are new in v0.7.0. There is
no in-place "upgrade" from an earlier release on these platforms
because they were not previously supported — install fresh using
[install.md](install.md#agent--macos-launchd) /
[install.md](install.md#agent--windows-service). To opt a macOS/Windows
agent into remote updates once installed, reinstall with
`--remote-update`/`-RemoteUpdate`, or hand-edit `agent.env`
(`CP_REMOTE_UPDATE=on`) and restart the service — the hub can never
turn this on remotely.

**Storage accounts** (Google Drive/Dropbox) and the notify
`verify`/`storage verify` CLIs need no migration step to appear —
Settings → Storage simply shows no connected accounts until you
connect one (see [storage.md](storage.md)).

## Upgrade verification kit (`hub-upgrade-check.sh`)

`scripts/hub-upgrade-check.sh` is a standalone, general-purpose
verification tool for a Linux+systemd hub install — it works against
any current or historical version, not just the v0.3.x → v0.7.0 path
below, and is useful whenever you want firmer evidence than "the
`update` command printed success" before and after an upgrade.

It has three subcommands, meant to be run in this order:

```bash
sudo scripts/hub-upgrade-check.sh pre
sudo cloud-pulse-hub update --version v0.7.0
sudo scripts/hub-upgrade-check.sh post --expect v0.7.0
# only if post reports a failure:
sudo scripts/hub-upgrade-check.sh rollback            # dry-run, prints the plan
sudo scripts/hub-upgrade-check.sh rollback --apply     # asks to confirm, then executes it
```

### `pre` (run immediately before `update`)

Collects, into a timestamped, root-only (`0700`) directory under
`/var/lib/cloud-pulse/upgrade-backups/` by default:

- The current service state, binary version, unit file, and the list
  of keys set in `hub.env` (**never their values** — token contents
  are never printed or written anywhere by this tool).
- An **online SQLite backup** (`sqlite3.Connection.backup`, safe to run
  against a live, WAL-mode database) plus a `PRAGMA integrity_check`
  pass/fail, size, and sha256.
- Copies of the current binary, unit file, and `hub.env` for a later
  rollback.
- A JSON snapshot of `schema_migrations`, every known table's row
  count (tables absent on your version are skipped, so this works
  whether you're on v0.3.2 or v0.7.0), host IDs, and settings keys.
- An API/dashboard smoke check: `/healthz`, `/api/v1/hosts`,
  `/api/v1/version`, the main HTML page, and static assets.

Expected output: a PASS/FAIL table ending in a summary line and the
exact `update` command to run next. Takes a few seconds; no real
network access beyond the API calls against your own hub.

### `post` (run immediately after `update` and its restart)

Re-collects the same facts and diffs them against `pre`'s snapshot:

- The service **actually restarted** (its systemd start timestamp
  changed, not just that it's "active") — this specifically catches
  the class of bug where a unit reload succeeds but the running
  process is the old binary still (the v0.3.1-era restart-missed
  regression this tool is designed to catch a repeat of).
- The installed binary and `GET /api/v1/version` both report
  `--expect`'s version.
- The installed systemd unit contains every directive the *new*
  binary's own `systemd-unit print` would render — compared
  dynamically rather than against a hard-coded list, so this stays
  correct as the rendered unit changes across releases.
- Migrations are a superset of `pre`'s (never regress), row counts
  never decrease, no host or settings key present in `pre` is missing,
  and the database's `integrity_check` still reports `ok`.
- The default egress alert rules exist.
- Dashboard sign-in works (`CP_UI_TOKEN` if set, otherwise a
  `--password-file`/prompted password login) — a hub still on the
  default password gets a clear **warning to change it now**, but this
  tool never changes it for you.
- Every major API area responds (or 404s if genuinely absent on your
  version) with the same host count as `pre`, and the dashboard's
  static assets and security headers are intact.

If `post` reports any failure, it prints the exact `rollback` command
to run next.

### `rollback` (dry-run by default)

Prints the exact sequence of commands it would run — stop, move the
current database aside (never delete it), restore the backed-up
database/binary/unit, `daemon-reload`, start, verify — and does
nothing else unless you pass `--apply` (which then asks for
confirmation, or pass `--yes` too for non-interactive use). Any data
written between `pre` and the rollback (new metrics, hosts, setting
changes) is **not** merged back — it is preserved, untouched, under a
`rollback-saved-<timestamp>/` directory the output points you to, in
case you need it later.

### Troubleshooting during an upgrade

| Symptom | Likely cause | What to do |
|---|---|---|
| `post` fails "service did not restart" | The unit reloaded but the process didn't actually respawn | `sudo systemctl restart cloud-pulse-hub`, then re-run `post` |
| `post` fails "unit is missing directives" | `update` replaced the binary but the unit wasn't re-rendered | `sudo cloud-pulse-hub systemd-unit apply`, then `sudo systemctl restart cloud-pulse-hub` |
| Dashboard login stops working after upgrading past v0.3.x | v0.4+ requires signing in (`admin`/`changeme` on first login) | Sign in and change the password; use `cloud-pulse-hub reset-password` if you're locked out |
| The hub becomes unreachable on its old address | A network/listen setting changed | `cloud-pulse-hub reset-network` restores the env-file-configured listener |
| Any of the above doesn't resolve it | — | Run `rollback` (dry-run first to see the plan, then `--apply`) |

## v0.3.x → v0.4+ notes

Upgrading from any v0.3.x release introduces dashboard authentication
for the first time. Concretely, once you run `update` past v0.3.x:

- **Sign-in is required.** The dashboard and every `/api/v1/*` read
  route (other than `/healthz`) now require either a logged-in session
  or a bearer token, where a v0.3.x hub with no `CP_UI_TOKEN` set had
  none at all.
- **The first login is `admin` / `changeme`.** The hub sets this
  automatically on its first v0.4+ startup if no password is already
  configured. You are required to change it before doing anything else
  — every route except `/auth/me`, `/auth/password`, and `/auth/logout`
  responds `403 password_change_required` until you do. `pre`/`post`
  above surface this as a warning, not a failure, since changing the
  password on your behalf is out of scope for an automated tool.
- **`CP_UI_TOKEN` becomes a scripted-API token, not a login.** If you
  already had `CP_UI_TOKEN` set on a v0.3.x hub, it keeps working
  exactly the same way for scripts/`curl` after the upgrade — bearer
  auth was, and remains, independent of the dashboard's session login.
  The dashboard itself always uses the password login, never this
  token.
- **Settings moved.** The old flat `GET /api/v1/settings` admin
  endpoint is gone; hub.env-equivalent settings are now split across
  dedicated pages/endpoints (Settings → Network, → Notifications, →
  Storage, etc. — see [configuration.md](configuration.md) and
  [security.md](security.md)).

## Upgrading through older releases (legacy path, v0.1.x–v0.2.x)

Versions before v0.3.0 have no `update` subcommand at all — an old
binary given `update` as its first argument just fails during normal
config loading (`CP_AGENT_TOKEN is required`, etc.), since that
dispatch didn't exist yet. A v0.3.0 binary has `update`, but it only
replaces the binary, not the systemd unit (`internal/systemdunit`
didn't exist until v0.3.1).

To move any pre-v0.3.1 install onto the current release, run the
installer one-liner you used originally, once, choosing **2)
Reinstall** (or `--reinstall`/`-Reinstall`) — this is the same command
shown above. It preserves `hub.env`/`agent.env` untouched, re-renders
the service definition, and prints an `Upgraded vA -> vB` line. From
that point forward, `update` is all you need.

The dashboard's per-host update badge always shows the correct command
for that specific agent's version — the legacy installer one-liner for
an agent still below v0.3.0, or the plain `update` command once it's
past that line — so you don't have to track each host's version by
hand.

## Update notifications

- **Dashboard banner**: when `GET /api/v1/version` reports
  `update_available: true`, every dashboard page shows a dismissible
  banner with the `update_command` and a link to release notes.
  Dismissing a banner remembers that release tag until a newer one is
  published.
- **Per-agent badge**: each host card/detail page shows an "update
  available" badge with the exact command for that agent.
- **Disabling outbound checks**: `CP_UPDATE_CHECK=false` on the hub
  stops it from ever contacting GitHub for release information — no
  background check, no banner, no per-agent badges. Running `update` by
  hand still works and still contacts GitHub (or your configured
  mirror) on demand.
- **Mirrors / air-gapped installs**: `CP_UPDATE_LATEST_URL` and
  `CP_RELEASE_BASE_URL` redirect both the background check and every
  binary's `update` subcommand to a mirror or internal release feed.
  Set once per environment; hub and agent (all platforms) read the
  same two variables via `internal/selfupdate`.
- **Trust model**: updates are fetched over HTTPS and verified against
  a sha256 checksum published in the same release's `checksums.txt` —
  this proves the downloaded binary matches what the release server is
  currently serving for that tag, but it is **not a cryptographic
  signature**. Anyone who can tamper with the release assets (or a
  compromised/malicious `CP_RELEASE_BASE_URL` mirror) can tamper with
  both the binary and its checksum together.

## Remote agent updates

Since v0.6.0 (Linux) / v0.7.0 (macOS, Windows), an admin can trigger
`update` on a batch of hosts from the dashboard instead of connecting to
each one by hand. See [remote-updates.md](remote-updates.md) for the
full security model, per-platform mechanics, and failure-reason table.
