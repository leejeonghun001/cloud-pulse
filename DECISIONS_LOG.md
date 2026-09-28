# Decisions Log

ADR-lite entries for cloud-pulse. Each entry: ID, date, context, decision,
consequences. Append new entries at the bottom of the `## Log` section —
never rewrite history here, only add to it.

## Log

### D-001 — Module path uses real GitHub account

- **Date**: 2026-09-29
- **Context**: The module needs a stable, importable path. Placeholder
  paths like `github.com/yourusername/...` are common in templates but are
  wrong for a real public repo.
- **Decision**: Use `github.com/leejeonghun001/cloud-pulse`, the actual
  GitHub account this project is published under.
- **Consequences**: Every import path, ldflags `-X` target, and doc
  reference uses this path. Renaming the account or repo later requires a
  repo-wide import path rewrite.

### D-002 — Go 1.25 minimum

- **Date**: 2026-09-29
- **Context**: The spec originally called for Go 1.22+. `modernc.org/sqlite`
  v1.59.0 (our pinned SQLite driver, see D-question below) requires Go 1.25.
- **Decision**: Set `go 1.25.0` in `go.mod` as the minimum toolchain,
  overriding the spec's 1.22+ floor.
- **Consequences**: Contributors and CI need Go ≥1.25. The local dev
  machine runs go1.27.1, which satisfies this. Older LTS distros may need a
  manually installed toolchain.

### D-003 — Agent PUSH model over HTTP(S) with PSK bearer token

- **Date**: 2026-09-29
- **Context**: Beszel uses a hub-pull model over SSH, which requires the
  hub to have inbound SSH reachability to every monitored host.
- **Decision**: Agents push metrics to the hub over HTTP(S), authenticated
  with a pre-shared bearer token (`CP_AGENT_TOKEN`), instead of the hub
  pulling over SSH.
- **Consequences**: Works behind NAT/CGNAT without port forwarding or
  reverse tunnels. Simpler to run over a Tailscale mesh (agent just needs
  outbound connectivity to the hub's Tailscale IP). Trade-off: the hub
  can't remotely diagnose an agent it isn't hearing from; it can only
  observe "last seen" staleness.

### D-004 — Tailscale isolation via default CIDR allowlist

- **Date**: 2026-09-29
- **Context**: The hub's HTTP API should not be casually reachable from
  the public internet even if it's accidentally exposed on a public
  interface.
- **Decision**: Default `CP_ALLOWED_CIDRS` to Tailscale's CGNAT range
  (`100.64.0.0/10`), Tailscale's IPv6 ULA range (`fd7a:115c:a1e0::/48`), and
  loopback (`127.0.0.0/8`, `::1/128`). Checked against `RemoteAddr` only.
  Set `CP_ALLOWED_CIDRS='*'` to disable the allowlist entirely.
- **Consequences**: Out of the box, only Tailscale peers and localhost can
  reach the API — a safe default for a homelab/Tailscale-first deployment.
  Anyone deploying without Tailscale must explicitly set `*` or their own
  CIDR list.

### D-005 — modernc.org/sqlite + WAL + 3-tier rollups

- **Date**: 2026-09-29
- **Context**: Need a CGO-free embedded database, and raw 15s samples
  forever would grow unbounded.
- **Decision**: Use `modernc.org/sqlite` (pure Go, no cgo) with WAL mode,
  and store metrics in three tiers: raw (15s resolution, 26h retention),
  5-minute rollups (30d retention), and 1-hour rollups (400d retention).
  Rollup jobs are idempotent — re-running a rollup window produces the same
  rows (`INSERT OR REPLACE` keyed by `(host_id, ts)`).
- **Consequences**: Bounded disk usage with long-term trend data retained
  cheaply. Idempotent re-rolls mean a crashed/restarted rollup job is safe
  to simply re-run over the same window.

### D-006 — Egress = sum of physical-NIC TX deltas, per UTC calendar month

- **Date**: 2026-09-29
- **Context**: Cloud egress billing is provider-specific and not directly
  observable from inside a VM. We need an approximation the agent can
  compute locally.
- **Decision**: Egress is the sum of per-interval TX byte deltas on
  physical network interfaces only (virtual/tunnel interfaces like
  `docker*`, `veth*`, `tailscale*`, `tun*`, etc. excluded via
  `CP_NET_EXCLUDE`), accumulated per UTC calendar month, written
  idempotently keyed on `(host_id, ts)`.
- **Consequences**: This is a conservative approximation, not an exact
  billing figure — it also counts intra-VPC/private traffic that many
  providers don't bill for. We accept over-counting (safer to warn early)
  over under-counting (which could hide an actual overage).

### D-007 — Egress alert thresholds 80/95/100% with dual-format webhook

- **Date**: 2026-09-29
- **Context**: Users want to be warned before hitting a provider's free
  egress tier, not just informed after the fact.
- **Decision**: Alert at 80% (warning), 95% (critical), and 100%
  (exceeded) of the configured monthly egress limit. Webhook payload
  includes both `text` (Slack-compatible) and `content` (Discord-compatible)
  keys so one webhook URL works for either. Deduplicated via an
  `alerts_sent` table keyed on `(host_id, month, level)` so each threshold
  fires exactly once per host per month.
- **Consequences**: One notifier implementation serves both major chat
  webhook formats. Deduping means restarting the hub mid-month doesn't
  re-fire alerts that already went out.

### D-008 — No AWS SDK; hand-written SigV4 + CloudWatch Query API

- **Date**: 2026-09-29
- **Context**: The AWS SDK for Go is large and pulls in a wide dependency
  tree, working against the "ultra-lightweight" goal.
- **Decision**: Implement AWS SigV4 request signing by hand and call the
  CloudWatch `GetMetricData` Query API directly over HTTP/XML, with no
  AWS SDK dependency.
- **Consequences**: Smaller binary, zero AWS-related dependencies, but we
  own the signing correctness (verified against AWS's documented test
  vectors in unit tests). Note: S3 **request** metrics (`AllRequests`,
  `GetRequests`, etc.) require S3 Request Metrics to be explicitly enabled
  on the bucket, which is a paid feature — when unavailable, the collector
  degrades gracefully (`RequestMetricsAvailable=false`, not an error).

### D-009 — R2 via Cloudflare GraphQL Analytics API

- **Date**: 2026-09-29
- **Context**: Cloudflare R2 doesn't expose a CloudWatch-like metrics API;
  its usage data lives in Cloudflare's GraphQL Analytics API.
- **Decision**: Collect R2 storage and operations stats via Cloudflare's
  GraphQL Analytics API (`r2StorageAdaptiveGroups`,
  `r2OperationsAdaptiveGroups`), classifying operations into Class A/B per
  Cloudflare's published R2 pricing categories.
- **Consequences**: R2 collection depends on Cloudflare's GraphQL schema
  remaining stable; the collector must handle the GraphQL `errors` array
  explicitly since GraphQL returns 200 even on partial failure.

### D-010 — Flat release assets + checksums.txt, no archives

- **Date**: 2026-09-29
- **Context**: Simpler install scripts and fewer moving parts than
  per-platform tarballs/zips.
- **Decision**: Publish release binaries directly as flat files named
  `cloud-pulse-{hub,agent}-{os}-{arch}[.exe]`, plus a single
  `checksums.txt` (sha256sum format) covering all of them. No `.tar.gz`/
  `.zip` archives. Install scripts download the binary and `checksums.txt`
  and verify sha256 before installing.
- **Consequences**: No archive extraction step needed anywhere. Adding a
  new platform is just adding a new flat file + checksum line.

### D-011 — No `-race` in CI

- **Date**: 2026-09-29
- **Context**: Go's race detector requires cgo, which conflicts with the
  project's absolute CGO ban.
- **Decision**: CI never runs tests with `-race`. Race-sensitive changes
  may be tested locally with `-race` (which requires temporarily allowing
  cgo in that local run only) at a contributor's discretion, but this is
  never enforced or required.
- **Consequences**: We lose automated race detection in CI. Concurrency
  bugs must be caught via careful review, `go vet`, and design (e.g.
  avoiding shared mutable state per `CODING_CONVENTIONS.md`).

### D-012 — MIT license, public repo

- **Date**: 2026-09-29
- **Context**: Need a permissive, well-understood license for a public
  open-source homelab tool.
- **Decision**: MIT license, repository public from the start.
- **Consequences**: Maximum reuse freedom for downstream users; minimal
  obligations on contributors. No copyleft concerns for anyone embedding
  cloud-pulse elsewhere.

### D-013 — Frontend: uPlot over Chart.js; Tailwind v4 prebuilt

- **Date**: 2026-09-29
- **Context**: Need charting for host metrics and a CSS approach that
  doesn't force end users to run a JS build toolchain.
- **Decision**: Vendor uPlot (~50KB) instead of Chart.js for time-series
  charts. Vendor Tailwind v4's *output* — `web/assets/app.css` is prebuilt
  and committed via `make css` (using `npx @tailwindcss/cli`), so end users
  and CI never need Node.js installed to build or run the hub.
- **Consequences**: Much smaller JS payload than Chart.js. The tradeoff is
  that anyone changing Tailwind classes in templates must remember to run
  `make css` and commit the regenerated `app.css` — it's not rebuilt
  automatically at runtime or in CI.

### D-014 — Memory "used" = Total − Available

- **Date**: 2026-09-29
- **Context**: Linux's naive "used = total - free" wildly overstates
  actual memory pressure because it doesn't account for reclaimable
  page/buffer cache.
- **Decision**: Define `Sample.MemUsed` as `MemTotal - MemAvailable`, using
  the kernel's own "available" estimate (which already accounts for
  reclaimable cache), not `MemTotal - MemFree`.
- **Consequences**: Matches what tools like `free -h`'s "available" column
  and most modern monitoring dashboards consider the true used/available
  split. Slightly more expensive to compute than a naive free/total ratio
  but gopsutil exposes `Available` directly, so no extra cost in practice.

### D-015 — Subagent orchestration note: not needed

- **Date**: 2026-09-29
- **Context**: Considered whether this log needs an entry documenting the
  multi-subagent build process used to construct cloud-pulse itself.
- **Decision**: Skip. Orchestration/process metadata about *how* the repo
  was built is not a product/architecture decision and doesn't belong in
  an ADR log about the software's design.
- **Consequences**: None — this entry exists only to record that the
  question was considered and deliberately not acted on.

### D-016 — Integration stage found zero cross-package mismatches

- **Date**: 2026-09-29
- **Context**: Step 2 phase B (wiring) needed to verify that
  `internal/storage.DB`, `internal/cloud.S3Collector`/`R2Collector`, and
  `internal/hub.WebhookNotifier` — all written independently in parallel —
  actually satisfy `internal/hub`'s consumer-defined `Store`,
  `BucketCollector`, and `Notifier` interfaces.
- **Decision**: No source changes were needed in any of those packages.
  `cmd/hub/main.go` declares compile-time assertions (`var _ hub.Store =
  (*storage.DB)(nil)`, etc.) that compiled clean on the first attempt.
- **Consequences**: Validates the "consumer-defined interfaces" convention
  in practice — as long as every adapter package is handed the exact
  method signatures from SPEC.md, independent agents converge without a
  reconciliation pass. Kept as a compile-time (not just runtime) guarantee
  in `cmd/hub/main.go` so any future signature drift fails the build
  immediately instead of surfacing as a wiring panic.

### D-017 — `go mod tidy` pulls in modernc.org/sqlite's own build-tool graph

- **Date**: 2026-09-29
- **Context**: Running the final `go mod tidy` (deferred until the
  integration stage per D-015's sibling agents' notes, to avoid a
  go.mod race between parallel agents) needed to promote
  `github.com/shirou/gopsutil/v4` and `modernc.org/sqlite` from
  `// indirect` to direct requires now that `internal/agent` and
  `internal/storage` actually import them.
- **Decision**: Accepted the additional indirect entries `go mod tidy`
  added (`modernc.org/cc/v4`, `modernc.org/ccgo/v4`,
  `github.com/stretchr/testify`, `github.com/google/go-cmp`, etc.) — these
  are `modernc.org/sqlite`'s own transitive code-generation/test tooling
  dependencies declared in its go.mod, not new runtime dependencies of
  cloud-pulse. Verified none carry CgoFiles
  (`CGO_ENABLED=1 go list -deps -f '{{if and (not .Standard)
  .CgoFiles}}{{.ImportPath}}{{end}}' ./...` stayed empty) and that
  `github.com/shirou/gopsutil/v4 v4.26.8` / `modernc.org/sqlite v1.59.0`
  and the `go 1.25.0` line were unchanged by the tidy run.
- **Consequences**: `go.mod`'s direct `require` block now lists exactly
  the two pinned dependencies; everything else stays `// indirect`. Future
  `go mod tidy` runs may add/remove further indirect tooling deps of
  `modernc.org/sqlite` without that being a signal to re-review — only a
  version bump of the two pinned direct deps needs a DECISIONS_LOG entry.

### D-018 — Hub startup never logs token values, even in `-check-config`

- **Date**: 2026-09-29
- **Context**: `cmd/hub -check-config` and the normal startup log both
  need to communicate whether auth is configured, without ever risking a
  token leaking into logs, terminal scrollback, or CI output.
- **Decision**: Both paths print only presence (`"(set)"` / `"(not
  set)"`) for `AgentToken`, `UIToken`, and `AlertWebhookURL` — never their
  values, never a length, never a hash/prefix. The security warning for
  "CP_ALLOWED_CIDRS allows all AND CP_UI_TOKEN is empty" fires on startup
  (not just `-check-config`) so an operator running the real binary sees
  it too, not only someone who remembers to run the check-config flag.
- **Consequences**: Safe to pipe hub startup logs or `-check-config`
  output anywhere (bug reports, CI logs) without redaction tooling.

