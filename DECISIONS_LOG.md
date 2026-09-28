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

### D-019 — Dashboard: vanilla ES modules, no bundler, no build step for JS

- **Date**: 2026-09-29
- **Context**: The dashboard ships embedded in the hub binary; requiring
  Node.js/webpack/esbuild to build it would contradict the "no Node.js
  needed to run the hub" goal and add a whole toolchain dependency for a
  UI that's small enough not to need one.
- **Decision**: `web/assets/js/*.js` are plain ES modules loaded directly
  by the browser (`<script type=module>`), imported via relative paths,
  no transpilation, no minification step for JS. Only CSS goes through a
  build step (Tailwind, see D-013), and its *output* is committed.
- **Consequences**: Anyone can read/edit the dashboard source and reload
  the browser with zero build step. Trade-off: no tree-shaking/minified
  JS payload, and browser compatibility is whatever each module's syntax
  requires natively (acceptable for a self-hosted homelab tool targeting
  current browsers).

### D-020 — uPlot y-axis: fixed pixel width when using a custom label formatter

- **Date**: 2026-09-29
- **Context**: uPlot's default axis label formatter renders raw
  byte/sec magnitudes as bare integers (e.g. `"687190690"`); a custom
  `values()` formatter producing unit-suffixed labels (`"655.6 MiB/s"`)
  triggered a uPlot layout-convergence bug where the internal
  `calcAxesRects()` label-width feedback loop occasionally painted
  glyphs from a stale, non-final layout cycle, producing non-monotonic
  tick labels. Reproduced consistently across fresh Firefox
  profiles/cache-busted loads, ruling out caching.
- **Decision**: Give the y-axis a fixed pixel `size` (84px) whenever a
  custom byte/percent formatter is used, removing the width-feedback
  loop entirely instead of letting uPlot auto-size the axis from label
  content.
- **Consequences**: Clean, monotonic axis labels on all six host-detail
  charts. A fixed width is slightly less adaptive to unusually
  wide/narrow label text than auto-sizing would be, but at the font
  size/label lengths this dashboard uses, 84px comfortably fits every
  formatted value. Worth revisiting upstream in uPlot if the bug
  resurfaces elsewhere.

### D-021 — Tailwind v4 output prebuilt and committed; no Node.js at runtime

- **Date**: 2026-09-29
- **Context**: Restated/confirmed alongside D-013 after the dashboard
  build: `web/assets/app.css` must be regenerated and committed whenever
  `web/src/input.css` or a Tailwind class in `index.html`/JS changes.
- **Decision**: `make css` (`npx --yes @tailwindcss/cli@4.3.3 -i
  web/src/input.css -o web/assets/app.css --minify`) remains the single
  source of truth for regenerating the committed CSS; CI's `web` job
  only runs `node --test web/test/`, it never invokes the Tailwind CLI.
- **Consequences**: A contributor who edits Tailwind classes but forgets
  `make css` will not be caught by CI (no CI job diffs the rebuilt
  output against the committed one). This is an accepted gap: adding a
  CSS-rebuild-and-diff CI job would require Node.js in CI purely to
  verify a file that rarely changes; flag in review instead.

### D-022 — Token entry via a native `<dialog>` + localStorage, no cookies

- **Date**: 2026-09-29
- **Context**: When `CP_UI_TOKEN` is set, the dashboard's `fetch` calls
  need a bearer token the browser doesn't have automatically (no
  server-side session/cookie mechanism exists, by design — the hub is
  stateless w.r.t. UI auth).
- **Decision**: A 401 response from any API call triggers a native HTML
  `<dialog>` prompting for the token; on submit, the token is stored in
  `localStorage` and attached as `Authorization: Bearer <token>` to all
  subsequent requests. No cookies, no server-side session state.
- **Consequences**: Token persists across reloads on the same
  browser/origin without any hub-side session storage. Trade-off:
  `localStorage` is readable by any script running on the same origin
  (mitigated by strict CSP — `script-src 'self'`, no inline scripts —
  which is the dashboard's actual XSS defense, not the storage choice
  itself).

### D-023 — XSS-safe DOM construction: no `innerHTML` with dynamic data

- **Date**: 2026-09-29
- **Context**: Host names, provider strings, and other agent-supplied
  fields are rendered into the dashboard; agent input is not a trusted
  source (an agent token grants ingestion, not implicit trust of every
  string field it sends).
- **Decision**: All dynamic content is set via `textContent` / DOM
  builder helpers (`el()` creating elements and assigning attributes
  programmatically), never `innerHTML` with interpolated data. Static,
  fully-authored markup fragments (none containing request data) are
  the only exception.
- **Consequences**: A malicious or buggy agent cannot inject markup into
  the dashboard via any field in `HostInfo`/`Sample`. Combined with the
  CSP's `script-src 'self'`, this closes both the injection and the
  execution vector.

### D-024 — Installer scripts are self-contained; no shared `scripts/lib/`

- **Date**: 2026-09-29
- **Context**: `install-hub.sh`/`install-agent.sh` are designed to be
  piped directly via `curl | bash`. A shared library file would need its
  own network fetch (a second failure mode) or would break the
  single-file "download once, verify once" trust model a reviewer relies
  on when auditing exactly the bytes they're about to pipe into root
  bash.
- **Decision**: Both installers duplicate their small (~60 line) set of
  helpers (fetch/sha256/arch-detection/validation) instead of sourcing a
  common `scripts/lib/common.sh`. `scripts/lib/` does not exist.
- **Consequences**: ~60 lines of duplication between the two scripts.
  Accepted because each script stays independently a single,
  fully-auditable file — the exact property that makes `curl | bash`
  installation trustworthy to review before running.

### D-025 — `main()` invoked only on the installer's last line

- **Date**: 2026-09-29
- **Context**: A `curl | bash` pipe can be cut off mid-transfer (network
  blip, killed connection). If top-level statements executed as the
  file streamed in, a truncated script could execute a partial,
  ill-defined fragment of logic.
- **Decision**: Every top-level statement in both installers lives
  inside a function; `main "$@"` is the literal last line of the file.
  Bash only begins executing `main`'s body once the entire file
  (including that final line) has been received and parsed.
- **Consequences**: A cut-off transfer fails to parse (or fails before
  `main` is ever reached) rather than partially executing. This is a
  hard rule for any future edit to either installer: no new top-level
  code before `main "$@"`.

### D-026 — sha256 checksum verification is mandatory, not optional

- **Date**: 2026-09-29
- **Context**: Both installers download a pre-built binary from a
  release URL and run it as root (via systemd). An unverified download
  piped into an installed, root-run binary is a supply-chain risk if the
  release host or network path is ever compromised or MITM'd.
- **Decision**: Every install downloads `checksums.txt` alongside the
  binary and aborts (non-zero exit, binary never installed) if the
  computed sha256 doesn't match the entry for that asset name. There is
  no flag to skip this check.
- **Consequences**: An operator who wants to install from a source that
  doesn't publish `checksums.txt` cannot use these scripts unmodified —
  intentional; the alternative (an opt-out flag) would make it too easy
  to silently disable the one supply-chain check these scripts do.

### D-027 — Sandbox mode via `CP_RELEASE_BASE_URL` + `CP_INSTALL_ROOT`

- **Date**: 2026-09-29
- **Context**: `scripts/test-install.sh` needs to exercise the full
  installer logic (download, checksum, user creation, unit rendering,
  `systemd-analyze verify`, upgrade/uninstall/purge) as an unprivileged
  CI user, without mocking the scripts' own code paths.
- **Decision**: `CP_RELEASE_BASE_URL` redirects asset downloads to a
  local `python3 -m http.server` instance; `CP_INSTALL_ROOT` prefixes
  every real system path (`$PREFIX/bin`, `/etc/cloud-pulse`,
  `/etc/systemd/system`, `/var/lib/cloud-pulse`) with a sandbox
  directory, and replaces only the operations that require real root
  (`useradd`, `chown`, `systemctl`) with logged "sandbox: would run ..."
  lines. Downloading, checksum verification, `install -m`, unit
  rendering, and `systemd-analyze verify` all still run for real against
  the sandboxed paths.
- **Consequences**: `test-install.sh` runs unprivileged in CI and
  locally, covering 66 real assertions against real installer logic —
  only the handful of operations that are inherently root/live-systemd
  only are stubbed, keeping the stubbed surface minimal and auditable.

### D-028 — `validate_env_value` rejects whitespace, newlines, backslash, `#`/`export`

- **Date**: 2026-09-29
- **Context**: An independent security review (installers-verify) found
  that unvalidated flag values written into `hub.env`/`agent.env` could:
  (1) inject a second, attacker-controlled `KEY=VALUE` line via an
  embedded newline, since systemd's `EnvironmentFile=` parser is
  last-line-wins; (2) silently truncate at a space when later
  word-split, corrupting a token with no visible error; (3) differ
  between what `-check-config` validates and what systemd's real
  `EnvironmentFile=` parser hands the running process, because systemd
  applies POSIX-style backslash-escaping that our own
  `load_env_file_safe()` does not replicate.
- **Decision**: Every flag value destined for an env file is passed
  through `validate_env_value` before being stored, rejecting (exit 1,
  naming the offending flag): embedded `\n`/`\r`, a value beginning with
  `#` or `export `, any embedded whitespace, and any embedded backslash.
- **Consequences**: All four classes of bug are now parse-time errors
  instead of silent corruption or a root RCE. Ten regression assertions
  in `test-install.sh` reproduce the reviewer's exact payloads and
  assert both a non-zero exit and that no env file/marker file was
  produced as a side effect.

### D-029 — No `source`/`eval` on env files; `load_env_file_safe` + `env -i`

- **Date**: 2026-09-29
- **Context**: The same security review found that an earlier revision's
  `-check-config` validation ran `env -i ... bash -c "set -a; source
  hub.env; ... -check-config"`. Bash's `source` performs full shell
  parsing (command substitution, backticks) on every line of the sourced
  file, and every value in `hub.env` ultimately originates from an
  installer flag — a payload like `--webhook-url
  'https://x/$(rm -rf /)'` would execute as root the instant
  `run_check_config` ran. The reviewer reproduced this live against the
  vulnerable revision.
- **Decision**: Replaced `source` with `load_env_file_safe()`, a pure
  string-operation line reader (`${line%%=*}`/`${line#*=}` + `case`
  matching, no `eval`, no `bash -c` interpolation) that validates each
  line matches `KEY=VALUE` and aborts on anything else. Parsed pairs are
  passed as literal argv elements to `env -i "PATH=$PATH"
  "${env_args[@]}" "$BIN_PATH" -check-config` — the same plain-assignment
  semantics systemd's own `EnvironmentFile=` parser uses.
- **Consequences**: `-check-config` validates byte-identical values to
  what systemd will load at real service start, with zero code path in
  between that could reinterpret file content as shell syntax.
  `install-agent.sh` was never exposed to this bug (`cmd/agent/main.go`
  has no `-check-config` flag; it runs `-once` unconfigured instead as
  its post-install sanity check).

### D-030 — Atomic binary install: `install` to `.new` then `mv -f`

- **Date**: 2026-09-29
- **Context**: GNU coreutils' `install(1)` unlinks the destination and
  recreates it (`O_CREAT|O_EXCL`) rather than write-then-rename, leaving
  a brief window where the destination binary is absent or partially
  written — confirmed via `strace` during security review.
- **Decision**: Both installers install to a sibling `${BIN_PATH}.new`
  path, then `mv -f` it over the final path. `mv` within the same
  filesystem is a single `rename(2)` syscall, so the swap is atomic.
- **Consequences**: No window where the binary path is missing or
  truncated, even mid-upgrade. Defense-in-depth rather than a fix for an
  observed failure — an already-running process holds its old binary
  open via its existing file descriptor regardless of this change.

### D-031 — Hardened systemd unit set; `AF_NETLINK` deliberately excluded

- **Date**: 2026-09-29
- **Context**: Needed to decide the systemd sandboxing directive set for
  both units, and specifically whether `RestrictAddressFamilies` needs
  `AF_NETLINK` for network metrics collection.
- **Decision**: Both units share `NoNewPrivileges`,
  `ProtectSystem=strict`, `ProtectHome=read-only`, `PrivateTmp`,
  `PrivateDevices`, `ProtectKernelTunables`, `ProtectControlGroups`,
  `RestrictSUIDSGID`, `LockPersonality`, empty
  `CapabilityBoundingSet=`/`AmbientCapabilities=`, and
  `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX` — deliberately
  *not* including `AF_NETLINK`. Verified by reading gopsutil v4's Linux
  source: `disk.IOCounters` reads `/proc/diskstats`, `net.IOCounters`
  reads `/proc/net/dev`, `cpu.Times`/`load.Avg` read `/proc/stat`/
  `/proc/loadavg` — none open a netlink socket. `PrivateDevices` was
  similarly verified safe: none of these calls touch `/dev/*`, and disk
  usage is a `statfs(2)` call that works under `ProtectSystem=strict`.
- **Consequences**: A materially smaller allowed-syscall/address-family
  surface than a "just in case" broader set would give. If a future
  metric ever needs netlink (e.g. switching to a netlink-based network
  stats source), this restriction must be revisited alongside that
  change, not silently widened.

### D-032 — Hub-only `StateDirectory=`; agent has no persistent state dir

- **Date**: 2026-09-29
- **Context**: The hub unit needs `/var/lib/cloud-pulse` to exist,
  writable, with correct ownership before `cloud-pulse-hub` starts; the
  agent has no on-disk state at all (in-memory ring buffer only).
- **Decision**: The hub unit uses `StateDirectory=cloud-pulse` (systemd
  creates/owns the directory under `ProtectSystem=strict`) in real
  installs; in sandbox mode (no real systemd), the rendered unit instead
  gets an explicit `ReadWritePaths=` entry pointing at the sandboxed data
  dir so `systemd-analyze verify` still sees a self-consistent unit. The
  agent unit has neither directive.
- **Consequences**: The hub never needs a manual `mkdir`/`chown` step in
  the installer for its data directory; systemd handles it. The agent
  unit stays minimal since it genuinely needs no writable path beyond
  what `ProtectSystem=strict`'s defaults already allow (none).

### D-033 — Idempotent re-run = upgrade; no separate `--upgrade` flag

- **Date**: 2026-09-29
- **Context**: Needed a model for "install once, then keep it current"
  without a separate installer mode to maintain and document.
- **Decision**: Re-running either installer script (with no flags, or
  with `--version` to pin a release) is the upgrade path: it
  re-downloads and re-verifies the binary, re-renders the systemd unit,
  and calls `systemctl enable --now` again (restarting an
  already-running service). `write_env_file` preserves existing
  secret/config values unless an explicit flag supplies a new one.
- **Consequences**: One mental model, one code path, for both "first
  install" and "upgrade to a newer/pinned version." An operator can run
  `install-hub.sh --version v0.2.0` to pin/roll back the binary without
  ever touching or regenerating the already-distributed agent token.

### D-034 — Hub JSON 405, not Go's default plain-text 405

- **Date**: 2026-09-29
- **Context**: Go 1.22's `http.ServeMux` auto-generates a plain-text 405
  for a registered pattern hit with the wrong method, but registering
  *any* bare catch-all pattern under `/api/` or `/` (even one with a
  method prefix on just one of the methods) can make that catch-all win
  over a more specific, method-scoped route for other methods —
  silently suppressing the built-in 405 for that route. Verified
  empirically with isolated `httptest` probes.
- **Decision**: No catch-all is registered for the bare `/api/` prefix.
  Only `GET /` is registered (for static assets, safe since nothing else
  lives directly under `/`); `handleStatic` explicitly checks for an
  `/api/` path prefix and returns a JSON 404 itself for genuinely
  unknown API paths, rather than relying on ServeMux's default handling
  for that case. A thin wrapper middleware additionally normalizes
  ServeMux's own 405 response body to JSON (`{"error": "..."}`) instead
  of Go's default plain-text `"405 Method Not Allowed"` body, keeping
  every hub response JSON regardless of status code.
- **Consequences**: Both properties — correct JSON 404 for unknown
  `/api/...` paths and correct JSON 405 (with `Allow` header preserved)
  for wrong-method requests on known routes — hold simultaneously,
  verified by `TestWrongMethod_405`/`TestUnknownAPIPath_404JSON`. Any
  future `/api/v1/*` route must be registered directly on the mux with
  an explicit method prefix (never via a shared prefix catch-all) or
  this property breaks again.

### D-035 — Cross-OS test portability: `filepath.Join` in expectations, OS-aware fixtures

- **Date**: 2026-09-29
- **Context**: CI runs the test matrix across `ubuntu-latest`,
  `ubuntu-24.04-arm`, `macos-latest`, and `windows-latest`. Early test
  runs failed on Windows (`TestLoadHub_DBPath/Defaults` hardcoded a
  forward-slash-joined expected path) and macOS
  (`TestCollector_DiskDedupeAndAggregate` used a disk-partition fixture
  that Darwin's `includePartition` rule — only `/` and `/Volumes/*` —
  correctly rejects, since the fixture wasn't OS-aware).
- **Decision**: Any test asserting a filesystem path builds the expected
  value with `filepath.Join`/`filepath.ToSlash`, never a hardcoded
  separator. Any test fixture whose validity depends on OS-specific
  filtering rules (disk partitions, mountpoints) is written to be valid
  under every target OS's rule set, or is explicitly gated with a
  `runtime.GOOS` check when the behavior itself is OS-specific by
  design.
- **Consequences**: The full `go test ./...` matrix passes on all four
  CI runners with no OS-specific skips beyond the ones that are
  intentionally testing OS-specific logic (e.g. Darwin's disk-partition
  rule itself). Any new test touching paths or disk/network fixtures
  must follow this rule from the start rather than being retrofitted
  after a CI failure.

### D-036 — Firefox via WebDriver BiDi for headless dashboard verification

- **Date**: 2026-09-29
- **Context**: Headless Chromium hangs on any `http(s)` navigation on
  the development Raspberry Pi 5 host (confirmed: `data:` URLs work,
  real navigations do not), making it unusable for visually verifying
  the embedded dashboard (screenshots, console/CSP error checks,
  in-page `eval`-driven interaction flows).
- **Decision**: Use Firefox via the WebDriver BiDi protocol
  (`tools/shot.mjs`) for all headless browser verification of the
  dashboard on this machine, never Chromium. This is a development/CI
  tooling choice, not a product requirement — the shipped dashboard has
  no Firefox-specific code and works in any modern browser.
- **Consequences**: Every dashboard screenshot and interaction-flow
  verification in this project's history was captured via Firefox BiDi.
  A known tool limitation was found and worked around in the same
  sessions: `shot.mjs --localStorage` pre-navigates to `<origin>/healthz`
  to seed `localStorage`, but cloud-pulse's `/healthz` returns
  `application/json`, and Firefox throws on `localStorage` access
  against a JSON-content-type document — so token-flow screenshots use
  `--eval` against the real HTML page instead (fill `#cp-token-input`,
  `requestSubmit()`) to drive the same round-trip in-page.

### D-037 — CI `install` job runs the sandboxed installer test suite per-arch

- **Date**: 2026-09-29
- **Context**: `scripts/test-install.sh` builds and tests real release
  assets for the *host* architecture only (via `uname -m`/
  `CP_TEST_UNAME_M`), so a single CI runner only ever exercises one
  architecture's asset-naming/installer path.
- **Decision**: `.github/workflows/ci.yml`'s `install` job runs on both
  `ubuntu-latest` (amd64) and `ubuntu-24.04-arm` (arm64) in a matrix, so
  both real architectures' asset names, checksum verification, and
  sandboxed install/upgrade/uninstall/purge flows are exercised in CI on
  every push, not just locally on this arm64 development machine.
- **Consequences**: CI catches an amd64-specific or arm64-specific
  installer regression that a single-arch runner would miss (e.g. an
  asset-name/arch-mapping typo that only breaks one architecture's
  download URL). Windows/macOS are correctly excluded from this job
  since the installers are Linux/systemd-only by design (`detect_os`
  rejects any non-Linux `uname -s`).


### D-038 — R2 free-tier storage uses decimal 10 GB (2026-09-29)

- **Context:** `models.R2FreeTier.StorageBytes` was `10 * GiB`. Cloudflare's R2 pricing page lists "10 GB-month" without defining GB as decimal or binary.
- **Decision:** Use `10 × 10⁹` bytes (`models.R2FreeStorageBytes`).
- **Consequences:** The dashboard free-tier bar reaches 100% about 7% earlier than with GiB, so it errs toward warning early.

### D-039 — Installer prints the explicit `--listen` host (2026-09-29)

- **Context:** With `--listen 127.0.0.1:8090` the hub only answers on loopback, but the printed agent one-liner used the detected Tailscale/LAN IP, which gave a URL that doesn't work.
- **Decision:** `print_summary` uses the bind host when it is not a wildcard (`""`, `0.0.0.0`, `::`, `[::]`); otherwise it keeps the Tailscale → `hostname -I` detection.
- **Consequences:** The one-liner is always reachable for the address the hub is actually bound to. Covered by a `test-install.sh` assertion (67 total).
