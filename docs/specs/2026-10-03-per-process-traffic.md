# DRAFT — Per-process network traffic ("top talkers")

> **Status: DRAFT — ROUND30 REAFFIRMS ROUND29: DEFERRED / NOT IMPLEMENTED.**
>
> This remains a dormant, unaccepted proposal, not a v0.9 feature or any
> version commitment. Round30 accepts no per-process collector, schema,
> configuration, API, UI, permission, or privilege change. The technical
> material below is retained only for possible reconsideration after the
> feasibility gates in §10; it is not an implementation contract.
>
> **Decision:** defer per-process attribution. `/proc` visibility is
> namespace-scoped and does not by itself establish process ownership;
> macOS `nettop` and Windows EStats have not been validated under the actual
> installed agent identities. PID reuse, protocol and byte scope, privilege,
> privacy, and boundedness also remain unresolved. Do not promise this for
> any release.
>
> The supported alternative remains host network-device RX/TX accounting
> and approximate network pricing, not per-process usage. See §1.3 for its
> sources and limitations.

---

## 1. Why this feature

### 1.1 The gap

cloud-pulse already reports host/interface byte deltas and rates
([`internal/models/sample.go`](../../internal/models/sample.go),
[`internal/agent/source.go`](../../internal/agent/source.go)'s `NetIOCounters`;
Linux uses `/proc/net/dev` through gopsutil). It also collects listening
ports (`internal/models/inventory.go`), with PID/name when accessible,
not guaranteed ownership for every socket. It does not attribute transferred
bytes to processes. The proposed operator question is:

> **Which program on this box is sending these bytes?**

This could complement the existing host-level egress and pricing views.
It does not yet establish which process or container caused a bill, and
this proposal makes no uncited claim about competitors' capabilities.

### 1.2 Current implementation check (Round 28)

| Area | Audited current behavior | Proposal only |
|---|---|---|
| Config | `internal/config` has no `CP_TRAFFIC` setting | Explicit local opt-in, default off (§6) |
| Models/report | `internal/models/sample.go` has host totals; no `TrafficSample` or per-process traffic report model | Traffic snapshot and report field |
| Agent | `internal/agent` has inventory collection, no traffic collector | Validated byte source and bounded accounting |
| Container identity | [`internal/models/inventory.go`](../../internal/models/inventory.go) has no container init PID; [`internal/agent/inventory.go`](../../internal/agent/inventory.go)'s `matchContainerPorts` matches published ports | PID/netns-to-container attribution is unresolved |
| Storage | Latest migration is [`internal/storage/migrations/0007_v08.sql`](../../internal/storage/migrations/0007_v08.sql); no `host_traffic` store | Proposed next migration `0008`, subject to intervening changes |
| API | [`internal/hub/inventoryroutes.go`](../../internal/hub/inventoryroutes.go) serves inventory; no per-process traffic read route | Proposed traffic endpoint (§5) |
| Web | [`web/assets/js/pages/settings/traffic.js`](../../web/assets/js/pages/settings/traffic.js) edits host-total traffic limits, unrelated to process accounting; no top-talkers feature | Future presentation only |

Existing navigation, README bullets and screenshots are not evidence that
this feature is implemented. None are changed or claimed by this proposal.

### 1.3 Round29 decision and supported alternative

Round29 defers per-process traffic collection. This decision neither
recommends nor rejects a Google/Azure OAuth device-flow investigation.
Existing Google Drive device-flow code is not evidence that cloud-billing
authentication can reuse it.

The existing supported view remains observed, host-level RX/TX monthly
accounting: NIC deltas and baseline/reset handling are in
`internal/agent/{collector,filter}.go`; UTC-month insertion and idempotence
are in `internal/storage/samples.go`; RX and TX limits remain independent in
`internal/models/{egress,limits}.go`; and `internal/billing/network.go`
calculates approximate pricing from configured ingress/egress rates and
pooling. These are host totals, not process attribution or provider-billed
usage. Private/intra-VPC traffic may be counted even when it is not
provider-billed; missed samples and resets limit observation. Provider
charges remain separate. Round30 changes none of this accounting or its
limitations.

### 1.4 The engineering caveat

**Per-process byte accounting is OS-specific, and feasibility is UNVALIDATED.**
The proposal requires an explicit status, method and collection scope.
`exact` must be reserved for a validated source that attributes transferred
bytes directly to the identified process within a stated protocol/window.
Namespace totals split among processes are estimates, not exact process
bytes. Queue occupancy is not transferred bytes; connection counts are not
byte rates. With no defensible byte source, report unavailable/unsupported
rather than manufacture a numeric rate.

---

## 2. Scope

> **DORMANT, UNACCEPTED PROPOSAL — not implemented or shipped.**

### 2.1 In scope

> The scope below is proposed only; it is not accepted implementation scope.

- Investigate agent-side per-process byte accounting on Linux, Windows and
  macOS; cross-platform support is a goal, not a feasibility promise.
- Interval-average rates and cumulative **observed deltas since each
  identity's baseline**, not process lifetime or complete since-agent-start
  totals. First discovery, resets and gaps limit coverage (§4.2).
- Reporting over the existing `AgentReport` → ingest path, with an explicit
  opt-in config flag and a self-describing capability block.
- Hub-side persistence (latest snapshot per host) and one read endpoint.
- A future host-detail top-talkers view with provenance and coverage copy;
  no UI implementation or layout decision in this round.

### 2.2 Explicitly out of scope

- **Privileged helpers and privilege expansion.** The Linux agent runs with
  an empty `CapabilityBoundingSet` and `NoNewPrivileges=yes`
  (`scripts/install-agent.sh`), and the systemd unit is hardened
  (`ProtectSystem=strict`, `PrivateDevices=yes`). Weakening that posture to
  collect a nice-to-have metric is not a trade this project makes. If
  stronger accounting later requires a helper or extra permissions, that
  needs a separate proposal and review — not an assumption in this draft.
- **Cumulative counters across agent restarts.** The agent is stateless by
  design (`internal/agent`'s ring buffer is memory-only). Totals reset to
  zero on restart; per-identity baseline times and coverage must also be
  reported, rather than labelling every total "since agent restart".
- **Command lines, arguments, environment, or remote-address reporting.**
  See §7 for the privacy boundary and transient local source identifiers.
- **Alert rules on per-process traffic** in v1. The metric is
  attribution-shaped, not threshold-shaped ("docker sends 40 GB"), and
  per-process identities are unstable (PIDs, short-lived containers).
  Revisit once the data model has survived a release.
- **Historical per-process time series.** The proposed v1 stores the latest snapshot per
  host only, exactly like `host_inventory`. See §8 for why, and what would
  have to change to add history.

---

## 3. Data model

> **DORMANT, UNACCEPTED PROPOSAL — not implemented or shipped.**

Illustrative future file `internal/models/traffic.go`, following the existing
inventory model's style. **No types below exist in implementation.** The
final schema, especially coverage and per-entry provenance, remains pending
feasibility work and review.

```go
// TrafficMethod is how a host's per-process byte counters were obtained.
// The value is reported to the dashboard verbatim so the UI can state the
// provenance of every number it renders; see
// docs/specs/2026-10-03-per-process-traffic.md §3.
type TrafficMethod string

const (
	// TrafficMethodExact requires validated direct process byte accounting
	// for the reported scope. Namespace totals do not qualify.
	TrafficMethodExact TrafficMethod = "exact"
	// TrafficMethodAttributed means byte totals were attributed using a
	// validated identity relationship; it is not a claim of exactness.
	TrafficMethodAttributed TrafficMethod = "attributed"
	// TrafficMethodEstimated requires a real byte source and a disclosed
	// allocation heuristic. Connection counts alone cannot supply bytes.
	TrafficMethodEstimated TrafficMethod = "estimated"
)

// TrafficStatus is the outcome of one collection attempt. Modelled on
// models.DockerStatus: every non-ok status carries remediation copy in the
// dashboard (web/assets/js/core/ports.js's DOCKER_STATUS_INFO pattern).
type TrafficStatus string

const (
	TrafficStatusOK               TrafficStatus = "ok"
	TrafficStatusDisabled         TrafficStatus = "disabled"
	TrafficStatusPermissionDenied TrafficStatus = "permission_denied"
	TrafficStatusUnavailable      TrafficStatus = "unavailable"
	TrafficStatusUnsupported      TrafficStatus = "unsupported"
	TrafficStatusError            TrafficStatus = "error"
)

// ProcessTraffic is one identified process's observed traffic over the
// stated interval, with the method and coverage disclosed.
type ProcessTraffic struct {
	// PID is the process id; unresolved ownership must not become a
	// falsely attributed process row.
	PID int32 `json:"pid"`
	// Name is a bounded process/executable name, never cmdline/arguments.
	Name string `json:"name"`
	// ContainerID is the 12-character container ID this process was
	// matched to by a validated relationship. Existing published-port
	// inventory alone does not establish this; omit if unresolved.
	ContainerID string `json:"container_id,omitempty"`
	// Method describes this row, not just the host's best available source.
	Method TrafficMethod `json:"method"`
	// RxBps/TxBps are the byte-per-second rates over the interval since
	// the previous valid observation of this identity's counters.
	// Baseline-only or invalid intervals must not emit a fabricated rate;
	// nullable fields/coverage representation remain an open schema choice.
	RxBps float64 `json:"rx_bps"`
	TxBps float64 `json:"tx_bps"`
	// RxBytes/TxBytes accumulate only valid observed deltas for this
	// identity, reset on agent restart or identity-state eviction.
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
	// Since is this identity's baseline time, not its process start time.
	Since int64 `json:"since"`
}

// TrafficInfo is a host's per-process traffic snapshot.
type TrafficInfo struct {
	Status TrafficStatus `json:"status"`
	// Method summarizes the host's source; mixed sources must not conceal
	// each process row's Method. Final mixed/coverage schema is pending.
	Method TrafficMethod `json:"method,omitempty"`
	// Error is a short, secret-free reason Status isn't "ok".
	Error string `json:"error,omitempty"`
	// CollectedAt is the unix-seconds time of this snapshot.
	CollectedAt int64 `json:"collected_at"`
	// IntervalSec states the nominal snapshot window, measured with a
	// monotonic elapsed clock. Different row windows require explicit
	// coverage or omission, not a misleading common interval.
	IntervalSec int `json:"interval_sec"`
	// Since is the collector session start. ProcessTraffic.Since, resets
	// and gaps determine each row's actual cumulative coverage.
	Since int64 `json:"since"`
	// Processes is the top-N list, sorted by (RxBps+TxBps) descending.
	// Encoded as [] rather than null when empty, capped at
	// MaxProcessTrafficEntries.
	Processes []ProcessTraffic `json:"processes"`
}
```

Proposed: `models.AgentReport` would gain one field, matching how `Inventory` and
`UpdateStatus` were added:

```go
// Traffic is the agent's per-process network traffic snapshot, omitted
// entirely when traffic collection is disabled or has never run (a hub
// older than this version ignores the field). See
// docs/specs/2026-10-03-per-process-traffic.md §4.
Traffic *TrafficInfo `json:"traffic,omitempty"`
```

Proposed output caps (separate from collector-state bounds in §4.2):

```go
// MaxProcessTrafficEntries caps the per-report process list, not collector
// identity maps or intermediate enumeration. The value is provisional.
MaxProcessTrafficEntries = 25

// MaxProcessTrafficNameBytes bounds a single process name, so a
// pathological or hostile name cannot inflate every report.
MaxProcessTrafficNameBytes = 64
```

---

## 4. Agent-side design

> **DORMANT, UNACCEPTED PROPOSAL — not implemented or shipped.**

### 4.1 Platform candidates — all UNVALIDATED

The existing `inventory_proc_linux.go` / `inventory_proc_other.go` split is
a possible code organization, not evidence of traffic support. A fakeable
source and common delta arithmetic are proposed; no framework choice is
made here.

| Platform | Candidate signal | What must be established before implementation |
|---|---|---|
| Linux | `/proc/<pid>/net/dev`, namespace identity, socket ownership | These are namespace/interface totals, **not process counters**. Access under the installed user and `/proc` restrictions is unvalidated; reading `comm` does not prove netns access. Interface exclusion must avoid double counting. |
| macOS | `nettop` process byte output | The default LaunchDaemon runs as **`_cloudpulse`, not root** ([`docs/install.md:107`](../install.md#agent--macos-launchd), `scripts/install-agent.sh`). Availability, permissions, process visibility, protocol coverage and counter continuity across separate invocations must be tested under that identity; do not promise host-wide or exact coverage. |
| Windows | TCP ownership table and `GetPerTcpConnectionEStats` | The agent runs as **`NT SERVICE\cloud-pulse-agent`, not LocalSystem** ([`docs/install.md:148`](../install.md#agent--windows-service), [`internal/agent/svcmgr_windows.go`](../../internal/agent/svcmgr_windows.go)). Access, any required enablement, counter lifetime and IPv4/IPv6 coverage are unvalidated. The LocalSystem updater is a different service, not permission for the agent. |

- **Linux namespace sharing:** a namespace may contain several processes
  or containers. An even split is an arbitrary estimate, not exact bytes
  for any process, even if the namespace total is valid. Host-network
  containers further complicate scope. No split is selected as a fallback.
- **Container identity:** inventory matches published ports only; it has no
  container init PID or established PID/netns relationship. A port match
  is insufficient to credit all process traffic to a container. Leave
  `container_id` unset until a relationship is independently validated.
- **Queue occupancy:** `rx_queue`/`tx_queue` describe bytes waiting, not bytes
  transferred over the interval. Weighting host totals by these queues is
  unvalidated and not adopted. No claim about another tool's algorithm is
  made without a citation.
- **Connection counts:** TCP/UDP counts cannot be converted into byte rates
  without an independent byte source. A failed byte collector must return
  a classified non-`ok` status, not invent estimated RX/TX. Windows TCP-only
  support is an open scope choice if its candidate source proves usable;
  this draft makes no blanket assertion that UDP accounting is impossible.
- **Subprocess safety:** if a macOS CLI source is selected, require fixed
  arguments, minimal environment, cancellation, timeout and bounded stdout.
  Do not log raw output or perform peer-name lookups. These are future test
  requirements, not a validated `nettop` invocation.

### 4.2 Proposed rate, baseline and state rules

Each rule needs a named test before implementation is accepted:

- **Identity:** use a validated process-start token with PID where available.
  PID/name alone must not silently join reused processes. If identity cannot
  be established, baseline again or report limited/unavailable coverage;
  connection and namespace identities also require reuse-safe handling.
- **First observation:** store the raw absolute counter as a baseline; add
  **zero** cumulative bytes and emit no rate until a valid second observation.
  Never use `raw - 0`, which would include bytes from before observation.
- **Counter decrease/reset:** baseline again and skip that interval's delta.
  Keep previously validated cumulative deltas, flag the coverage gap, and
  never emit negative rates or treat the new raw value as newly moved bytes.
- **Rate:** valid nonnegative delta divided by the identity's positive
  monotonic elapsed interval, not the hub's report interval. Reject zero or
  invalid intervals. Gaps must be disclosed rather than presented as a
  continuous 60-second series.
- **Cumulative:** add only validated observed deltas. Per-row baseline time,
  resets, missed short-lived processes and state eviction limit the total;
  collector session start is not a claim of complete since-start coverage.
- **All state must be bounded:** previous counters, cumulative RX/TX,
  last-seen/missed-poll bookkeeping, namespace/PID caches, connection maps
  and intermediate enumeration all need explicit hard limits, including
  within a single poll. Top-N output alone does **not** bound them.
  Three-consecutive-poll expiry may supplement, but cannot replace, those
  caps. Final limits and deterministic admission/eviction are open decisions;
  truncation must disclose reduced coverage and re-admission must baseline.
- **Ranking:** sort valid rows by `RxBps + TxBps`, cap output at the proposed
  `MaxProcessTrafficEntries`, and use deterministic identity tie-breaks.

### 4.3 Cadence and wiring

- **Current inventory:** `internal/agent/inventory_hook.go` attempts
  collection from `Reporter.Flush`, at most once per 60s; faster flushes
  reuse the snapshot. Transmission is separate: content changes or 10
  minutes since the last successful send. An unchanged inventory can
  therefore be collected every 60s yet transmitted only every 10 minutes;
  there is no guaranteed independent 60s inventory tick or UI refresh.
- **Proposed traffic:** collect at most once per 60s when enabled; send each
  newly collected snapshot on the next successful report, independently
  of whether inventory changed. Retry must not repeat cumulative additions.
  Delayed reporting must retain the actual collection time/window, not imply
  a fresh rate. This policy is proposed, not wired into the reporter.
- A future traffic provider could sit alongside `ReporterOptions.Inventory`
  (`InventoryProvider` interface) in `internal/agent/reporter.go`. Old-agent
  field omission and old-hub unknown-field behavior require compatibility
  tests; neither constitutes existing traffic support.

### 4.4 Provenance and incomplete coverage

A host-level method cannot describe mixed row sources. The illustrative
model proposes a per-row method instead of the former `Exact` boolean,
which incorrectly treated namespace sharing as exact. Final host summary,
protocol scope, baseline-only/rate availability and coverage-gap fields
remain open. No `exact` label may be assigned solely because a row is in
a container namespace.

### 4.5 Assumptions requiring evidence

- A source exposes meaningful transferred-byte counters to the installed
  agent identity without changing its privilege boundary.
- Counter scope, process identity and sampling windows can be established
  well enough to distinguish observed deltas from missing coverage.
- Useful data can be collected within explicit time, memory and payload
  bounds. None of these assumptions has runtime validation in this round.

---

## 5. Proposed hub behavior

> **DORMANT, UNACCEPTED PROPOSAL — not implemented or shipped.**

### 5.1 Persistence

Proposed latest snapshot: one row per host, following `host_inventory`.
There is no traffic store or migration in the current tree.

- The latest migration is `internal/storage/migrations/0007_v08.sql`.
  Proposed next migration **`0008`**, recheck numbering at implementation,
  would create
  `host_traffic (host_id TEXT PRIMARY KEY, collected_at INTEGER NOT NULL,
  method TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'error',
  json TEXT NOT NULL)`.
- Proposed `Store.SetHostTraffic` / `GetHostTraffic` would be added to the
  hub store interface and storage contract tests.
- Proposed `handleIngest` behavior: persist `report.Traffic` when non-nil, **after**
  `InsertSamples` succeeds and best-effort (a traffic-store failure must not
  fail an otherwise-good report of the host's metrics — same rule as
  `recordAudit`'s "logged but never fails the caller's request").
- Upsert bounds current rows per host, but does not settle host deletion,
  stale data, opt-out or privacy lifecycle. Review those semantics with the
  existing retention/deletion paths (`internal/storage/rollup.go`); do not
  claim that no cleanup is needed solely because this is a snapshot.

### 5.2 API

Proposed route only; **not registered today**:

```go
mux.HandleFunc("GET /api/v1/hosts/{id}/traffic", s.requireUser(s.handleGetHostTraffic))
```

Proposed `GET /api/v1/hosts/{id}/traffic` → `200` with a `models.TrafficInfo`, or
`404 {"error":"no traffic reported for this host","code":"no_traffic"}` when
the host is unknown or has never reported one — one `Store` call, the same
"unknown host and no data are indistinguishable" shape as
`handleGetHostInventory`, so this handler stays a single lookup.

### 5.3 Future presentation requirements (not a UI design)

There is no top-talkers card today. If implemented after approval, it must
distinguish measured bytes from estimates, interval averages from cumulative
observed deltas, and complete scope from partial/unavailable data. Names
must be escaped, container labels require validated identity, and baseline
or missing data must not appear as a measured zero. Remediation must be
specific to the validated source; Docker group membership is not a generic
fix for traffic permissions. English/Korean copy and empty/error/long-name
states need tests. Layout, navigation and screenshots are future UI work,
not decisions or deliverables in this documentation-only round.

---

## 6. Configuration

> **DORMANT, UNACCEPTED PROPOSAL — not implemented or shipped.**

**Proposed only: one new local agent variable, `CP_TRAFFIC`, default `off`.**
It is not read by current agent code. The existing
`scripts/check-docs.py` `ENV_VAR_ALLOWLIST` maps this name to this DRAFT
file; that exception is documentation policy, not implementation evidence,
and must be removed when the setting is implemented. No additional
unimplemented environment variables are introduced here.

```go
// Proposed Traffic setting: "off" (default; do not collect or report),
// "auto" (operator explicitly opts in; attempt validated platform methods
// and report non-ok status when no defensible byte source is available).
Traffic string
```

- Default/unset or `CP_TRAFFIC=off`: no collector, subprocess or traffic
  report field. Collection is **not enabled automatically** on upgrade.
- `CP_TRAFFIC=auto`: explicit operator opt-in, not a default. Attempt only
  methods validated for the installed identity; do not escalate privileges
  or invent byte rates on failure. The proposed v1 vocabulary is `off|auto`,
  with a fixed output cap; `top:<N>` is not selected for this draft.
- No hub-side setting, and no way for the hub to turn collection **on**
  remotely — the same one-way rule as `CP_REMOTE_UPDATE` (D-entry for
  v0.6 §2) and `CP_CLOUD_METADATA`. A hub must not be able to start
  collecting a new category of host data without the operator's config.

---

## 7. Privacy and security boundary

This is a feature that reports **what is running on someone's machine**, so
the constraints are part of the spec, not implementation detail:

- **Never collect a full command line.** Linux `cmdline`, Windows WMI
  `CommandLine`, and macOS `ps -o args` can contain secrets — API keys,
  bearer tokens, password
  arguments, signed URLs. `Name` is a **basename only**, capped at
  `MaxProcessTrafficNameBytes`, with control characters stripped (a
  process name is attacker-controlled: any local user can `exec` a binary
  with a newline in its name and forge dashboard rows or, without
  escaping, inject markup into the DOM).
- **Never report, persist or log remote peer addresses.** A validated
  source may require transient connection identifiers locally, but no peer
  address belongs in the report or debug output. Disabling DNS lookups
  does not itself remove numeric addresses from a tool's output; parse and
  discard any such fields with bounded in-memory lifetime.
- **Never collect the UID/username** of a process. It adds nothing to a
  traffic ranking and turns the report into a user-activity log.
- **Agent and user credentials have different authority.** The current
  `CP_AGENT_TOKEN` authenticates agent ingest (and agent time), not
  inventory reads (`internal/hub/server.go`,
  [`internal/hub/auth.go`](../../internal/hub/auth.go)). A
  stolen agent token can submit/spoof reports, including a future traffic
  payload; it does **not** by itself grant session/UI-token access to stored
  inventory or traffic. Do not conflate poisoning ingest with reading data.
- The proposed traffic read route must use `requireUser`, like inventory:
  an active permitted session or separate UI bearer token, not the agent
  token. It is a new hub read route subject to auth tests; no new endpoint
  on the agent is proposed. Opt-out and stale stored snapshots need an
  explicit lifecycle decision (§12).
- Audit logging: this is a **read-only** observation, not a config change,
  so it records nothing to `audit_log` — `CODING_CONVENTIONS.md`'s rule
  covers admin actions that *change persisted configuration*, and this
  changes none. Stated here so a later reviewer does not "fix" the
  omission.

---

## 8. Why no history in v1

Per-process history is genuinely valuable and genuinely expensive:

- **Cardinality.** PIDs churn; container IDs change when containers are
  recreated, not necessarily on every Compose invocation. A raw per-process time series is unbounded in
  the number of distinct identities, and `metrics_raw`'s 26-hour retention
  would still hold a day of a churny fleet's worth of rows.
- **Identity.** Aggregating "across time" requires a stable identity, and
  `(name, container_id)` is a heuristic — two different `python3`
  processes are one series, one `python3` re-deployed with different code
  is a continuing one. Getting this wrong silently produces a chart that
  lies.
- **Precedent.** `host_inventory` made exactly this trade and the dashboard
  reads fine with a latest-snapshot model.

The proposed v1 uses snapshots and bounded observed cumulative deltas,
not a complete "all afternoon" record: exited or evicted identities and
missed intervals are absent. History and stable aggregation identities
would need a separate proposal; no rollup architecture is selected here.

---

## 9. Budgets

> **DORMANT, UNACCEPTED PROPOSAL — every budget below is a target, not a
> measurement or an accepted implementation bound.**

| Item | Budget | Why |
|---|---|---|
| Per-entry/report JSON | Proposed 25 entries, bounded names; measure encoded size | Escaping, numeric widths and metadata invalidate the former ~110 B worst-case claim |
| `host_traffic` row | Proposed ≤ 8 KiB, pending final schema sizing | Must enforce an encoded byte bound as well as a list cap |
| Collection time | Proposed 5 s hard cap; typical cost unmeasured | Test cancellation and sample/report-loop isolation under slow sources |
| macOS CLI child | Proposed ≤ 1 per collection, 5 s cap, fixed env and output cap | Only if a source is validated for `_cloudpulse` |
| Agent memory | Explicit caps for **all** state/maps and scratch enumeration (§4.2); values pending | Top-N and age eviction are not a live-identity bound |
| Total report headroom | Existing `maxReportBytes` is 2 MiB; added size unmeasured | Test worst-case names/metadata with large sample batches before accepting a budget |

These are proposed acceptance targets, not measured performance. A future
collector must classify timeout/failure without blocking or failing other
metrics. The numbers in this table are not selected implementation limits,
proof that a bound is enforced, or evidence that a budget has been met. Before
reopening, provide finite numeric limits and enforcement tests for every
state/enumeration, subprocess output/time, total collection-time, and fully
encoded-payload bound under worst-case input. No runtime measurements are
claimed in this round.

---

## 10. Approval and feasibility gates

Per-process work may be reopened only after the following gates have
reproducible, reviewable evidence. Passing them would not itself approve
implementation. These are requirements for a future evidence package, not
claims that this draft or Round30 has passed any per-process gate. Existing
host-accounting test results, a design target, or a proposed budget are not
per-process evidence.

1. **Installed-equivalent identity and access:** on every proposed supported
   OS, run the candidate using the release-equivalent agent binary, service
   account, sandbox and installed configuration. Record the exact identity,
   OS and source access results; do not substitute an elevated developer,
   updater, root/admin account, or different namespace. Show that collection
   works without an unapproved permission or privilege change. `/proc`
   namespace visibility is not proof of process ownership; macOS `nettop`
   and Windows EStats remain unvalidated.
2. **Actual byte, protocol and attribution scope:** define and verify what
   each counter measures (including direction and byte-layer semantics),
   covered protocols/address families, and exclusions such as loopback or
   shared/ambiguous ownership. Compare representative known local workloads
   with observed counters on the installed-equivalent host. Do not infer
   process or container ownership from namespace/interface totals, socket
   counts, or host process observations alone.
3. **Identity reuse and counter lifecycle:** specify a reuse-safe identity
   and test first baselines, process/PID and connection/namespace reuse,
   resets, counter wrap/decrease, missed polls, restarts and gaps. Show that
   a new or re-admitted identity cannot inherit another identity's delta and
   that no rate or cumulative total bridges an unobserved interval without
   explicit coverage disclosure.
4. **Privacy lifecycle:** identify every collected and transient field and
   its minimization rule; prove that command lines, credentials, usernames,
   and peer addresses are not emitted or logged. Define and test local
   retention, user access, opt-out, failure/staleness, host deletion, and
   deletion/expiry behavior for stored snapshots and temporary identifiers.
5. **Compatibility:** test old/new agent and hub combinations, unknown or
   omitted report fields, upgrade/downgrade behavior, and safe behavior when
   collection is disabled or unsupported. Document any compatibility
   boundary before proposing a stored or wire contract.
6. **HARD resource and payload bounds:** state finite numeric hard caps and
   enforcement behavior for every retained state map and cumulative state,
   scratch/intermediate data, and enumeration in a single poll. Enforce
   subprocess count, wall-clock time including cancellation/cleanup, and
   stdout/stderr byte limits while reading; also bound total collection
   time and the fully encoded report/storage payload after escaping. Exercise
   adversarial worst-case enumeration and oversized output/payload, verify
   deterministic truncation or classified failure, and prove other report
   work is not blocked. Top-N output, age expiry, and unenforced target
   budgets do not satisfy these hard bounds.
7. **Container ownership proof:** if container attribution is claimed,
   demonstrate reliable ownership using evidence available to the installed,
   unprivileged agent. Listening ports or namespace totals alone are
   insufficient.
8. **Separate approval:** only after the evidence above, seek a new explicit
   decision and review the collector, config, schema, API, permissions and UI
   separately. Do not describe the feature as implemented or promise it for a
   release before implementation and validation.

---

## 11. Proposed test plan (not executed feature validation)

- **Accounting unit tests:** first observation with a large nonzero raw
  counter contributes zero; next observation adds only its delta; reset,
  PID/connection/namespace reuse, invalid intervals, gaps and re-admission
  do not fabricate rates or carry another identity's bytes. Cumulative
  RX/TX and per-identity baseline/coverage remain honest across resets.
- **Bounds unit tests:** exceed each collector map/scratch cap in a single
  poll, churn identities across polls, verify deterministic admission and
  expiry, and disclose truncated coverage. Output Top-N is tested separately.
- **Platform fixtures and later permitted runtime tests:** namespace totals
  are never exact process bytes; queues/counts never become bytes alone;
  inventory published-port matches do not establish process ownership.
  Validate macOS visibility/counter continuity as `_cloudpulse` and Windows
  EStats availability as `NT SERVICE\cloud-pulse-agent`, not as an elevated
  developer or updater. Permission failures must produce honest statuses.
- **Config/report:** unset/off starts no traffic work and omits the field;
  explicit auto enables only validated sources. Faster flushes do not
  recollect, unchanged inventory does not suppress new traffic, delayed
  reports preserve windows, and retries do not double-add counters.
- **Hub**: ingest stores and overwrites one row per host; a `nil` Traffic
  never touches the store; a store error never fails the report; the
  endpoint's 200/404 shapes; an older report without the field is accepted;
  agent token alone cannot read inventory/traffic, while user-session and
  UI-token authorization follow existing policy. Test opted-out/stale/deleted
  host behavior after its lifecycle contract is decided.
- **Privacy and budgets:** strip control characters and bound names; escape
  rendering; never emit/log arguments, usernames, secrets or peer addresses;
  subprocess environment/output/timeout limits and worst-case encoded
  report/storage size must be verified.
- **Web**: `node --test` for eventual sorting, byte/rate formatting,
  provenance and unavailable/baseline/gap copy, plus the
  existing `scripts/check-i18n.mjs` parity gate.
- **Future isolated smoke after approval:** an opted-in test agent reports
  traffic, and
  `GET /api/v1/hosts/{id}/traffic` returns a populated, correctly-ranked
  list with the actual serialized field checked on the wire and in the
  stored response. Increased total report size alone is not sufficient
  proof. No live agent or privileged test is run in this docs-only round.
- **Cross-compile**: `GOOS=linux/windows/darwin` builds must all pass —
  this is what catches a build-tag mistake that only shows up on a
  contributor's other laptop.

---

## 12. Open questions for Hun

These questions are dormant and apply only if the feasibility gates pass
and the proposal is explicitly reopened:

1. If reopened, should per-process traffic be considered for a separately
   approved implementation, or should it remain deferred?
2. If reopened, what opt-in and output scope should be proposed? The
   documented default `off`, `auto`, output cap, and no-privilege-expansion
   ideas remain unaccepted proposals, not shipped settings.
3. Should OAuth billing investigation be considered separately? This
   Round29 decision makes no choice about it.
4. **Feasible minimum:** what validated byte source and protocol/process
   coverage are useful enough under `_cloudpulse` and the Windows virtual
   account? Is clearly scoped TCP-only acceptable if validated? If Linux
   only yields namespace totals, defer process rows rather than call them exact.
5. **Contract and limits:** finalize baseline-only/rate availability, gaps,
   mixed per-row provenance, process-start identity, all-state caps and
   deterministic eviction before accepting the illustrative schema.
6. **Container attribution:** defer container labels until PID/netns mapping
   is validated? Published-port inventory is insufficient, including for
   host-network containers.
7. **Stored-data lifecycle:** define stale-snapshot presentation and cleanup
   on opt-out, host deletion and prolonged collector failure without adding
   a remote collection-on switch.
