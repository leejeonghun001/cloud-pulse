# Remote agent updates

Since v0.6.0 (Linux) / v0.7.0 (macOS, Windows), an admin can trigger
`update` on a batch of hosts from the dashboard instead of connecting
to each one individually — but the security model is deliberately
narrow: **the hub can only ask an agent to update to a specific
official release tag; it can never supply its own binary, URL, or
checksum.**

## Security model

- **Request-only**: the hub's request carries just a `job_id` and a
  target (`"latest"` or an exact `vX.Y.Z` tag) — nothing else. The
  agent's own env file (root/SYSTEM-owned, unreadable/unwritable by the
  agent's own unprivileged process) still determines *where* the
  update binary and checksums come from. A compromised hub can
  therefore only move an opted-in agent between versions of the
  already-trusted release feed that agent was configured with — it
  cannot redirect the agent to an arbitrary binary.
- **No downgrade**: a request whose target is not newer than the
  agent's current version is refused (`downgrade_refused`), except an
  exactly-equal target, treated as already succeeded
  (`already_up_to_date`).
- **Opt-in per agent, off by default**: `CP_REMOTE_UPDATE=off|on`
  (default `off`). The hub **cannot** turn this on remotely — it's set
  only from the agent's own side, either at install time
  (`--remote-update`/`-RemoteUpdate`, or the interactive installer's
  prompt) or by hand-editing the env file and restarting.
- **Version-gated per platform**: Linux agents `>= v0.6.0`; macOS and
  Windows agents `>= v0.7.0` (these platforms didn't support remote
  updates before v0.7.0). Any other OS, or an older agent even on a
  supported OS, reports `unsupported` with a manual-command fallback.
  The hub independently re-checks the version gate before ever handing
  out a request — an agent's optimistic self-reported capability is
  never trusted alone.
- **Privileged-helper file handling**: see
  [CODING_CONVENTIONS.md](../CODING_CONVENTIONS.md#privileged-helper-file-handling)
  for the no-follow-symlink + privileged-owned-result-directory rules
  that prevent the unprivileged agent process from tampering with its
  own update request or result, on every supported platform.

## How it works

Since agents are push-only (no inbound connection to them), the whole
flow rides on the existing report/response cycle:

1. Admin selects hosts on the dashboard (or `POST
   /api/v1/agents/updates {host_ids, target, max_parallel}` directly)
   — `max_parallel` (default 3) caps how many jobs are `in_progress` at
   once per batch. One `queued` job per host is created.
2. Within the parallel limit, the next matching agent's report response
   carries the request; the hub marks that job `in_progress` the
   instant it hands it out.
3. The agent (only if opted in) atomically writes an update-request
   file to a platform-specific location:
   - **Linux**: `/var/lib/cloud-pulse-agent/update-request.json`,
     picked up by a systemd path unit
     (`cloud-pulse-agent-update.path`) triggering a root oneshot
     service.
   - **macOS**: `/Library/Application Support/cloud-pulse-agent/update-request.json`,
     picked up by a `WatchPaths`-triggered root LaunchDaemon
     (`com.cloudpulse.agent-update`).
   - **Windows**: `C:\ProgramData\cloud-pulse-agent\update-request.json`,
     picked up by the `cloud-pulse-agent-updater` LocalSystem service,
     which polls every 30 seconds (Windows has no simple
     filesystem-watch trigger equivalent to launchd's `WatchPaths` or
     systemd's path units, so polling is the deliberately simplest safe
     choice).
4. The privileged helper runs `update --from-request <file>
   --result-dir <privileged-dir>` — the exact same download/
   checksum-verify/atomic-replace/service-definition-apply/restart
   pipeline the manual `update` subcommand already uses. The request
   file is read with a no-follow-symlink open and a strict size/schema
   check before anything else happens.
5. The result (`succeeded`/`failed` + version + error code, if any) is
   written to a privileged-owned result directory the unprivileged
   agent process cannot write to, so it can't forge its own
   success/failure report.
6. The restarted agent reads and reports that result on its next
   report; the hub marks the job `succeeded` if the reported version
   matches the target (or `already_up_to_date` -> `succeeded`) or
   `failed` otherwise. No response within 15 minutes of going
   `in_progress` marks the job `failed`/`timeout`.

## Statuses and failure reasons

| State | Meaning |
|---|---|
| `queued` | Waiting for its turn within the batch's `max_parallel` limit |
| `in_progress` | Handed to the agent, awaiting its result |
| `succeeded` | Agent reported the target version (or was already on it) |
| `failed` | See reason below |

| Failure reason | Meaning |
|---|---|
| `not_enabled` | Agent hasn't set `CP_REMOTE_UPDATE=on` |
| `unsupported` | OS/version below the platform's gate |
| `timeout` | No result within 15 minutes |
| `download_failed` | Couldn't fetch the release asset |
| `checksum_mismatch` | Downloaded asset didn't match `checksums.txt` |
| `verify_failed` | The new binary's own `-version` didn't report the target tag |
| `restart_failed` | Binary replaced but the service restart failed |
| `downgrade_refused` | Target is not newer than the current version |
| `unknown` | Anything else |

`POST /api/v1/agents/updates/{job_id}/retry` re-queues a fresh job for
a failed one — refused for `not_enabled`/`unsupported`, since retrying
without fixing the underlying issue would just fail identically. `POST
/api/v1/agents/updates/{batch_id}/cancel` cancels every still-`queued`
job in a batch; jobs already `in_progress` run to completion.

## Limitations

- **FreeBSD and other platforms**: no remote-update mechanism at all —
  those hosts always show the manual `update` command instead.
- `max_parallel` is tracked in-memory per batch, not persisted — a hub
  restart mid-batch falls back to the default of 3 for any batch still
  in flight.
- The dashboard's Rollout page (see below) shows a checkbox host list
  with version/eligibility badges, a target-version + parallelism
  picker, and an auto-refreshing progress panel.

## Rollout dashboard (SPEC-v0.8 §3-1)

Fleet → Rollout (`#/rollout`) is the fleet-wide view of agent version
health and remote-update history, replacing the old dedicated Updates
page (`#/updates` now redirects here; its agent-table/batch-creation/
progress-panel functionality is unchanged, just integrated into this
page's Agents section).

- **Summary cards**: up-to-date host count/percentage, outdated count,
  unknown-version count, and a breakdown of remote-update capability
  (capable / not opted in / unsupported) across the fleet.
- **Version distribution by OS**: a stacked bar per OS family, one
  segment per distinct agent version, with a full text-alternative
  table underneath (same data, screen-reader/print friendly) — see
  `GET /api/v1/fleet/versions`'s `distribution` field below.
- **Outdated hosts**: every host behind the fleet's latest known
  release, sorted most-behind-first, with its OS, version,
  "behind for" duration, remote-update capability, and — for a host
  that can't be remotely updated right now — its OS-aware manual
  update command (copy button included).
- **"Update all remotely-updatable outdated hosts"**: queues one batch
  targeting every outdated host currently capable of a remote update
  (`capability == "capable"`), after a confirm dialog showing the exact
  host count and a parallelism choice (1/3/5/10, default 3). Hosts that
  are outdated but not opted in or unsupported are never included —
  update those manually via the shown command.
- **Batch history**: the most recent batches (`GET
  /api/v1/agents/updates/batches`), each with its status, succeeded/
  failed/pending counts, duration, and a failure-reason breakdown;
  selecting a batch shows the same live progress panel batch creation
  does.

### Outdated status and "behind for"

A host counts as outdated when both its own reported `agent_version`
and the hub's currently-known latest release tag parse as valid
release tags and the host's version is strictly older — an unparsable
version (a local dev build, or a host that has never reported one) is
counted separately as "unknown," never as outdated, since there is
nothing valid to compare it against. The "behind for" duration is
computed from the moment the hub first observed a host as outdated (an
in-memory-only timestamp, not persisted — see the note on hub restarts
below) rather than any fixed schedule.

### API

`GET /api/v1/fleet/versions` — any signed-in user (read-only; see
[docs/api.md](api.md) for the full response shape). `GET
/api/v1/agents/updates/batches?limit=&before=` — same auth tier,
cursor-paginated by batch creation time.

`GET /api/v1/fleet/versions` response (`models.FleetVersionSummary`):

```json
{
  "latest_version": "v0.7.0",
  "total_hosts": 4,
  "up_to_date_count": 2,
  "outdated_count": 1,
  "unknown_count": 1,
  "capable_count": 2,
  "not_opted_in_count": 1,
  "unsupported_count": 1,
  "distribution": [
    { "os": "linux", "version": "v0.7.0", "count": 2 },
    { "os": "linux", "version": "v0.6.0", "count": 1 }
  ],
  "hosts": [
    {
      "host_id": "web-1",
      "hostname": "web-1",
      "version": "v0.6.0",
      "os": "linux",
      "outdated": true,
      "outdated_since": 1700000000,
      "capability": "capable",
      "manual_command": "sudo cloud-pulse-agent update"
    }
  ]
}
```

`capability` is one of `capable` (supported and opted in — a batch
targeting it would succeed) / `not_opted_in` (supported, but
`CP_REMOTE_UPDATE` is off) / `unsupported` (OS/agent-version below the
platform's gate). `outdated_since` is `0` for a host that isn't
currently outdated or has no tracked "since" yet.

`GET /api/v1/agents/updates/batches` response
(`{"batches": [models.UpdateBatchSummary, ...]}`):

```json
{
  "batches": [
    {
      "batch_id": "batch-abc123",
      "target": "latest",
      "target_count": 4,
      "succeeded": 3,
      "failed": 1,
      "pending": 0,
      "created_at": 1700000000,
      "finished_at": 1700000180,
      "duration_sec": 180,
      "failure_reasons": [{ "reason": "timeout", "count": 1 }]
    }
  ]
}
```

`before` (a `created_at` unix-seconds cursor) and `limit` (default 20,
max 200) page through batches newest-first; a batch still in progress
has `pending > 0` and omits `finished_at`/`duration_sec`.

### Limitations

- The "first observed outdated" timestamp behind each host's
  "behind for" figure is in-memory only, exactly like `max_parallel`
  above — a hub restart resets it to "just now" for every host still
  outdated at that point, rather than losing history entirely (the
  host is still correctly shown as outdated, just with a shorter
  reported duration until the next observation window has elapsed).
- Version-distribution grouping treats every unparsable version string
  (empty, `"dev"`, or a git-describe dev-build string) as a single
  `"unknown"` bucket per OS — it does not attempt to further
  distinguish between different dev builds.

