# Coding Conventions

This is the living conventions document for cloud-pulse. It exists so that
every contributor (human or agent) makes the same decisions the same way.

## Purpose & how to change it

- This document is the single source of truth for style, architecture, and
  process rules in this repo. When code review or a design discussion
  produces a new pattern, **update this doc in the same commit** as the
  code that introduces the pattern. A pattern that isn't written down here
  doesn't exist.
- Keep it crisp. Prefer one clear rule + one example over a paragraph of
  prose. If a rule can be enforced by a tool, say so (`ENFORCED BY: ...`).

## Non-negotiable rules

These are hard requirements. Each is enforced mechanically — if you think one
is wrong, change the enforcement and this doc together, don't bypass it.

- **`CGO_ENABLED=0` always.** No exceptions, no per-platform carve-outs.
  `ENFORCED BY: pre-commit, CI (build with CGO_ENABLED=0)`
- **`modernc.org/sqlite` only** as the SQLite driver. No `mattn/go-sqlite3`
  or anything else that links C.
  `ENFORCED BY: CI (go list -deps CgoFiles check)`
- **No `import "C"`.** `ENFORCED BY: pre-commit, CI (grep)`
- **No `#cgo` directives.** `ENFORCED BY: pre-commit, CI (grep)`
- **No non-std dependency that ships CGO files**, verified with:
  `go list -deps -f '{{if and (not .Standard) .CgoFiles}}{{.ImportPath}}{{end}}' ./...`
  must print nothing. `ENFORCED BY: CI`
- **gofmt clean.** `gofmt -l .` must print nothing.
  `ENFORCED BY: pre-commit, CI`
- **`go vet` clean.** `ENFORCED BY: pre-commit, CI`
- **errcheck clean** — no unhandled errors. An intentionally discarded error
  must use `_ =` with a comment justifying why it's safe to ignore, e.g.:
  ```go
  _ = w.Close() // best-effort close on the read-only path, nothing to recover
  ```
  `ENFORCED BY: pre-commit, CI (errcheck)`
- **No `panic(` in non-test code**, unless the line carries an escape-hatch
  comment explaining why panicking is correct there:
  ```go
  panic("unreachable") // allow-panic: switch is exhaustive over a closed enum
  ```
  `ENFORCED BY: pre-commit, CI (grep for panic( without allow-panic:)`
- **`os.Exit` / `log.Fatal*` only in `cmd/**/main.go`.** Library code returns
  errors; only the wiring layer decides to terminate the process.
  `ENFORCED BY: pre-commit, CI (grep)`
- **Conventional Commits** for every commit message.
  `ENFORCED BY: commit-msg hook, CI`

## Clean Architecture & directory layout

Dependencies point inward. Nothing inward-facing knows about anything
outward-facing.

```
cmd/hub/main.go          wiring only: config → storage → cloud collectors → hub.Server; signal handling
cmd/agent/main.go        wiring only: config → agent.Collector → agent.Reporter loop
internal/models/         pure domain types + pure domain funcs (NO imports besides std lib)
internal/version/        Version/Commit/Date vars set by -ldflags "-X"; String()
internal/config/         Hub/Agent config from env (lookup func injected) + validation
internal/agent/          collector (gopsutil behind small interfaces), delta math, reporter (HTTP push, buffer)
internal/storage/        SQLite (modernc) implementation of hub.Store; migrations; rollups; retention
internal/cloud/          sigv4.go, cloudwatch.go (Query API, XML), s3.go (S3Collector), r2.go (R2Collector GraphQL)
internal/hub/            HTTP API, middleware (CIDR allowlist, auth, security headers, recover, logging),
                         scheduler (rollup/prune/cloud), alerts (egress webhook). Defines interfaces Store,
                         BucketCollector, Notifier (consumer-defined interfaces).
web/                     embed.go (package web, //go:embed → exported FS embed.FS or func Assets() fs.FS),
                         index.html, assets/js/*.js, assets/app.css, assets/vendor/uplot.*
scripts/                 install-hub.sh, install-agent.sh, build-release.sh
.githooks/               pre-commit, commit-msg
.github/workflows/       ci.yml, release.yml
```

Dependency direction rules:

- `internal/models` imports **only the standard library**. It is the
  innermost layer; everything else may depend on it, it depends on nothing
  in this repo.
- `internal/hub` **defines** the consumer interfaces (`Store`,
  `BucketCollector`, `Notifier`) it needs. It does not import
  `internal/storage` or `internal/cloud`.
- `internal/storage`, `internal/cloud`, and `internal/agent` are **adapters**.
  They implement interfaces defined by their consumers; they don't define
  the interfaces themselves.
- `cmd/**` is wiring only — it constructs concrete adapters and injects them
  into consumers. No business logic lives in `cmd`.
- **Nothing imports `cmd`.** It's the outermost layer, a leaf.

## Go style

- **Naming**: short, idiomatic Go names (`ctx`, `db`, `h` for a handler
  receiver). Exported identifiers get doc comments starting with their own
  name. Package names are short, lowercase, no underscores.
- **Small interfaces defined by consumers**, not producers. If `hub` needs a
  store, `hub` declares the `Store` interface; `storage` just happens to
  satisfy it.
- **`context.Context` is always the first parameter** for any function that
  does I/O.
- **Every network call has a timeout** — either via context deadline or an
  `http.Client{Timeout: ...}`. No unbounded network calls.
- **Outbound calls to GitHub (or any third-party release/update service)
  must be optional and bounded.** A feature that phones out to GitHub
  (release-check background loop, `update` subcommand) is gated by an env
  var that fully disables it (`CP_UPDATE_CHECK=false` disables the hub's
  background check entirely — no timer started, not just a fast no-op
  per tick), and every individual request against it carries its own
  timeout distinct from — and no longer than — the timeout on whatever
  triggered it (`updateCheckTimeout` bounding a single background check,
  `updateTimeout` bounding the whole `update` subcommand). A GitHub
  outage or a slow/malicious mirror must never hang a background loop,
  block hub startup, or leave the `update` subcommand running
  indefinitely. See `internal/selfupdate` and
  `internal/hub/updates.go`'s `runUpdateCheckLoop`.
- **Agent time handling**: use the hub-corrected clock (`HubClock.Now()`,
  via the injected `timeSource`) for anything that becomes a *timestamp*
  leaving the process — a `Sample.Timestamp`, an aligned collection
  boundary. Use real monotonic elapsed time (`time.Since`/a prior
  `time.Time` captured locally) for anything that becomes a *rate or
  delta* — network byte/sec, CPU percent — never the difference between
  two hub-corrected boundaries. `Collector.CollectAt(ctx, ts)` takes the
  boundary as the sample's timestamp but still measures the interval for
  its internal rate math from the real time elapsed since the previous
  collection, so a missed/delayed boundary (e.g. suspend) never produces
  a distorted rate.
- **No global mutable state**, except the `version` package's build-time
  vars (`Version`, `Commit`, `Date`), which are set once via `-ldflags` before
  `main` runs and treated as read-only afterward.
- **Table-driven tests** for anything with more than one interesting case.
- **`t.Parallel()`** in tests and subtests where safe (no shared mutable
  state, no reliance on wall-clock ordering).
- **Test helpers call `t.Helper()`** so failures point at the caller.
- **No sleeps in tests** beyond what's strictly necessary (e.g. a single
  short warm-up for rate calculations). Prefer synchronization primitives,
  fake clocks (`Now func() time.Time`), or `httptest` over `time.Sleep`
  polling loops.
- **Test determinism — no timing-dependent tests.** A test must not rely
  on wall-clock sleep granularity or a real timing race to pass:
  - **Inject clocks, not `time.Now()`.** Any type whose behavior depends
    on the current time (the alert engine's sustained-window/cooldown
    checks, session TTLs, rate-limiter lockout windows) takes a `Clock
    func() time.Time` (or equivalent) field defaulting to `time.Now`,
    overridden by tests with a fixed or manually-advanced fake. See
    `internal/alerting.Options.Clock`, `internal/hub/ratelimit.go`.
  - **Sleep granularity is not portable.** `time.Sleep(1 * time.Millisecond)`
    takes roughly 15.6ms on Windows (its default timer resolution) —
    never assert on a sub-16ms sleep actually taking close to the
    requested duration, and never use a short sleep as a substitute for
    a real synchronization primitive or injected clock. Where a bounded
    poll loop is unavoidable (waiting for a real goroutine/listener to
    reach a state), poll against a real observable signal
    (`net.Dial` succeeding, a channel receive) with a generous deadline,
    not a fixed sleep duration — see `internal/listen/manager_test.go`.
  - **A fixed-duration sleep tied to a production timing constant must
    reference that constant, not repeat its value as a literal** —
    otherwise the test silently desyncs if the constant is later tuned.
  - **Never assert delivery-worker/goroutine completion via a sleep.**
    Use a `Wait()`/`drain()`-style method that blocks until a bounded
    queue has actually emptied (`internal/alerting.Engine.Wait`), or an
    injected hook/channel signaling completion — not "sleep long enough
    that it's probably done."
  `ENFORCED BY: code review; CI's 4-OS matrix (windows-latest included)
  surfaces a sleep-granularity assumption as a flaky failure`
- **Use `net/http/httptest`** for HTTP handler and client tests instead of
  binding real sockets where avoidable. Every notifier
  (`internal/notify`) and the alerting engine's delivery path are tested
  exclusively against `httptest` fakes — no test may contact a real
  Discord/Telegram/WhatsApp/webhook endpoint; see "Extension points"
  below for the fake-server pattern the notify/inventory tests use.

## Extension points

Adding a new instance of an existing extensible concept should not
require touching more than the files listed for that concept.

- **New notifier / notify-channel type** (`internal/notify`): add the
  `NotifyChannelType` constant and its `SecretFields()` entry in
  `internal/models/alerting.go` (the single source of truth for which
  Config keys are secret — see D-070), a `<platform>.go` implementing
  `Sender` (constructor `new<Platform>Sender(ch models.NotifyChannel,
  client *http.Client, opts clientOptions) (Sender, error)`, following
  the existing discord/telegram/whatsapp/webhook shape: validate Config,
  run any external URL through `validateEndpointHost`/the SSRF allowlist
  in `ssrf.go` if the platform has an official fixed host, build the
  platform's request shape in `Send`), a case in `New`'s type switch
  (`new.go`), and a `<platform>_test.go` using `httptest.NewServer` to
  assert the exact request shape (headers, multipart/JSON body,
  429/Retry-After handling) — never a real network call. If the
  Settings UI needs a step-by-step setup guide for it, add that under
  `web/assets/js/pages/settings/` (see the existing per-platform guide
  panels) — out of scope for a notify-only change if the UI update is
  deferred to a separate commit.
- **New alert metric** (`internal/alerting`): add the `AlertMetric`
  constant in `internal/models/alerting.go`, a case in
  `metricValue`/`seriesValueFor` (`evaluate.go`) resolving it from
  either `HostSnapshot` directly (like `host_down`/egress) or a
  `models.Series` column (like cpu/memory/disk/load1), and a case in
  `validAlertMetrics` (`internal/hub/alertroutes.go`) so the API accepts
  it. A metric backed by a real time series should just work with the
  existing sustained-window logic and chart rendering
  (`internal/alerting/chart.go`'s `isSeriesMetric`) without further
  changes; a metric with no queryable series (like `host_down`) must be
  added to that function's exclusion list so notifications correctly
  ship with no chart image instead of erroring.
- **New dashboard page** (`web/assets/js/pages/`): add
  `pages/<name>.js` exporting a `render(container)` (or the existing
  page-module shape — check a comparable existing page first, e.g.
  `pages/hosts.js`), register its route in `main.js`'s router table,
  and — if it needs pure formatting/computation logic — keep that logic
  in a DOM-free helper (`format.js` or a new pure module) so it's
  testable via `node --test` without a browser. See the JS conventions
  above for the DOM-builder/CSP rules every page must follow.

## Error handling

- Wrap errors with `%w` and a `pkg: action` prefix:
  ```go
  return fmt.Errorf("storage: insert samples: %w", err)
  ```
- Define sentinel errors for expected conditions:
  ```go
  var ErrNotFound = errors.New("not found")
  ```
- Check with `errors.Is` / `errors.As`, never string-match error text.
- **Never log-and-return the same error.** Either log it (because you're
  handling it and it stops here) or return it (because the caller will
  handle/log it) — not both, or callers see duplicate log lines for one
  failure.
- **Domain errors map to HTTP status codes only in `internal/hub`.** Storage
  and cloud adapters return domain/sentinel errors; only the HTTP layer
  knows that `models.ErrNotFound` means 404.
- **Graceful degradation**: a failing collector (cloud, disk, etc.) must
  never crash the process. Log the error, return partial results where
  possible, and keep serving the rest of the system.

## Logging

- `log/slog` only — no `fmt.Println`/`log.Println` debugging left in.
- Structured keys are `snake_case` (`host_id`, "sample_count", not
  `hostID`/`sampleCount`).
- **No secrets in logs.** Bearer tokens, API keys, and secret access keys
  are never logged, not even partially or hashed, unless explicitly needed
  for support and then only a fixed-length prefix behind a debug flag that
  doesn't exist yet — default is: never.

## Security

- **Constant-time comparison** for bearer tokens and passwords
  (`crypto/subtle`), never `==` or `strings.Compare`. This applies to
  the agent token, the optional `CP_UI_TOKEN`, and username comparison
  during login — not just password hash verification.
- **CIDR allowlist checks `http.Request.RemoteAddr` only.** Never trust
  `X-Forwarded-For` or similar client-supplied headers for access
  control. The same rule applies to the login rate limiter's per-client
  key (`clientIPForRateLimit`/`clientIP`): it must strip the ephemeral
  source port from `RemoteAddr` before using it as a map key — keying on
  the full `"ip:port"` string means every TCP connection gets a fresh
  key and a per-client counter never accumulates (a real cross-stage bug
  found only by live testing with real sockets, not a fixed-string
  `httptest` fake — see D-062). Any new code that needs "the requesting
  client's address" must reuse the existing helper rather than reading
  `r.RemoteAddr` directly.
- **`http.MaxBytesReader`** on every endpoint that accepts a request
  body, including auth and network settings endpoints (small, fixed
  caps — see `maxAuthBodyBytes`/`maxNetworkBodyBytes`).
- **Security headers** (CSP, `X-Content-Type-Options`, `Referrer-Policy`,
  `X-Frame-Options`) are applied by middleware to every response, not
  per-handler.
- **No secrets in the repo.** Tokens, keys, and credentials are supplied via
  environment variables only; nothing resembling a secret is ever committed,
  including in test fixtures (use obviously-fake values).
- **Admin endpoint rule**: any endpoint that can reveal a secret (the
  agent token) or make a state-changing settings/limit/network change is
  an *admin* endpoint, wrapped with `requireAdmin`. Since v0.4.0
  `requireAdmin` is a plain alias of `requireUser` — the hub has exactly
  one account, so there is no separate "is this caller an admin"
  question once they're authenticated at all. The old `admin_disabled`
  concept (a `403` returned whenever `CP_UI_TOKEN` was unset, regardless
  of any bearer header presented) is gone: the dashboard always requires
  either a valid session or the optional `CP_UI_TOKEN`, full stop. New
  admin routes must use `requireAdmin`, not a bespoke check, even if
  they're a `GET`.
- **Passwords and session tokens are never logged**, not even partially
  or hashed-and-truncated for "debugging." Failed logins log
  `slog.Warn` with the remote address only (see D-062). A stored
  password hash or session `id_hash` is fine to log (it's already a
  one-way digest, useless to an attacker on its own) but the plaintext
  password/token that produced it must never appear in a log line,
  error message, or panic value.
- **Sessions are hashed at rest.** Only `sha256(token)` is ever passed to
  a `Store` method or stored in SQLite; the plaintext token exists only
  in the HTTP response body at issuance and in the client's own storage
  afterward. Any new session-related code must look up sessions by hash,
  never store or compare a plaintext token server-side.
- **Password verification always runs the full hashing computation**,
  even for an unknown username or a malformed/missing stored hash (see
  `verifyPassword`'s synthetic-computation branch) — a fast-reject path
  that skips PBKDF2 for an invalid username is a timing side channel and
  must not be added, however tempting it looks as an optimization.
- **Network configuration changes must be reversible by construction.**
  Any code path that can change the hub's own listen address(es) or
  access allowlist must be structured so it cannot leave the requesting
  admin permanently locked out: validate against the *requesting
  client's own address* before applying anything (reject outright rather
  than apply-then-check), roll back automatically if no listener ends up
  bindable, and treat "binds fine but stops serving the client's own
  current connection" as a time-bounded **pending** state that
  auto-reverts if never confirmed — never an unconditional apply. See
  `internal/hub/networkroutes.go`'s `handlePutNetwork` state machine and
  D-064; a new network-affecting endpoint must follow the same
  lock-out/bind-failure/pending/auto-revert shape, not a simplified
  version of it.
- **Secrets are never passed via a child process's or the hub's own
  argv.** A bot token, webhook URL, access token, or any other
  credential must reach the code that needs it via an environment
  variable, a file the caller already controls the permissions of, or
  an HTTP request body/header — never as a command-line argument to
  `exec.Command`/`os.Args`, since argv is visible to every other process
  on the same host via `/proc/<pid>/cmdline` or `ps`. This applies to
  both directions: the hub building an `aws`/`oci` invocation (see
  "Exec rules" below, which passes credentials via `env`, never `args`)
  and `scripts/verify-notify.py`'s CLI, whose argument parser actively
  **rejects** any flag value that merely looks like a secret (a
  bot-token shape, a webhook URL, a long opaque token) rather than
  accepting and using it.

## Exec rules: privileged child processes

Any code that shells out to an external binary (`aws`, `oci`, or a
future addition) must follow all of these, together — see
`internal/billing/runner.go`'s `ExecRunner` for the reference
implementation:

- **Fixed argument array, never a shell string.** Build `exec.Command`/
  `exec.CommandContext`'s `args []string` as a literal, fully-formed
  slice — never `sh -c "... " + userInput` or any other string
  concatenated into a shell invocation. This is what makes command
  injection structurally impossible regardless of what a config value
  contains.
- **A bounded timeout on every call**, via `context.WithTimeout`
  wrapping the `exec.CommandContext` call — 60 seconds for the billing
  CLIs, documented per-caller if a different bound is ever needed. A
  hung child process (a stuck network call inside `aws`/`oci` itself)
  must never hang the calling goroutine indefinitely.
- **Output reads are capped**, via `io.LimitReader` (4 MiB for the
  billing CLIs) on both stdout and stderr — an unexpectedly large or
  runaway output must not be buffered without bound.
- **A minimal, explicit environment — never `os.Environ()`
  passthrough.** Build the child's `Env []string` from a fixed, named
  allowlist of variables the caller explicitly read from its own config
  (see [Billing](README.md#billing)'s "Where the hub looks for CLI
  config" for the exact list), plus a fixed `PATH` and a sandboxed
  `HOME` redirected under the hub's own data directory. The child must
  never inherit the hub process's full environment, which could contain
  unrelated secrets (session-signing material, other integrations'
  tokens) the child has no legitimate need to see.
- **A missing binary, bad credentials, or a timeout is a classified
  status, never a panic or an unhandled error surfaced to the caller as
  a 500.** See `internal/billing/classify.go`: `exec.ErrNotFound` →
  `not_installed`, a fixed set of stderr substrings → `auth_failed`/
  `permission_denied`, `context.DeadlineExceeded` → `error`, anything
  else → `error`. This is what SPEC-v0.6 §1 calls the "quiet-skip"
  status model.
- **Raw stderr/stdout is never logged.** Only the classified status
  string (`not_installed`, `auth_failed`, etc.) and a short, fixed,
  secret-free `StatusDetail` reach `slog` — the child's actual output
  could contain a credential echoed back by a misconfigured CLI or
  proxy, so it is inspected in-process for classification purposes only
  and then discarded.

## Audit logging

Any admin action that changes persisted, non-secret configuration
(pricing plans, host→plan assignments, the display-currency rate, the
billing polling interval, a remote-update batch) must be recorded to the
`audit_log` table via the shared `(*Server).recordAudit` helper
(`internal/hub/audit.go`), in addition to (not instead of) the action's
own `slog.Info` line:

- **Record the actor, remote address, action, entity, and full
  before/after values.** `recordAudit` resolves the actor
  (`"admin"`/`"api_token"`) from the existing session/API-token auth
  context via `auditActorFor`, and the remote address from the same
  `clientIP` helper the rate limiter uses (`RemoteAddr` host only, never
  a client-supplied header) — reuse these, don't re-derive either.
- **A no-op save is not logged.** If the marshaled before/after JSON is
  byte-identical, `recordAudit` skips the write entirely — a `PUT` that
  resends the same value the setting already had must not create a new
  audit row.
- **Never write a secret value into `before`/`after`.** Audit entries in
  this codebase are restricted to fields that are never credentials by
  construction (prices, plan assignments, an exchange rate, an interval)
  — if a future audited action's before/after *could* include a secret
  field, redact it the same way `NotifyChannel.Redacted()` does before
  it ever reaches `recordAudit`, don't add a parallel un-redacted path.
- **Define your own `models.AuditAction` constants in your own route
  file** (e.g. `AuditActionUpdateBatchCreate` in `updateroutes.go`,
  `AuditActionPlanCreate` in `pricingroutes.go`) — `AuditAction` is an
  intentionally open string type (`"<entity>.<verb>"`), not a shared
  enum requiring a central registry edit for every new action.
- **Retention**: audit entries older than `models.AuditRetentionDays`
  (400 days) are pruned on the same hourly retention sweep as alert
  events and rollups (`internal/hub/scheduler.go`'s `runPruneLoop`) —
  add a new audited action's cleanup to that existing sweep, don't start
  a second retention loop.

## Privileged-helper file handling

Any file exchanged between an unprivileged process and a privileged
helper it triggers (the remote-update agent's request/result files
being the current example) must defend against the unprivileged side
tampering with either file:

- **Read with `O_NOFOLLOW`** (`internal/agent/nofollow_unix.go`'s
  `unixNoFollowFlag()`, `0` on platforms with no such flag) so a
  symlink planted at the expected path is refused rather than
  transparently followed to an attacker-chosen target. This applies to
  *reading* a request file the privileged helper is about to act on,
  and to reading back a result file whose contents will be trusted.
- **Enforce a strict size cap before parsing** (4 KiB for an
  update-request file, 16 KiB for a result file) — read at most cap+1
  bytes and reject anything at or beyond the cap, never buffer an
  unbounded read into memory just to find out it's oversized.
- **Validate the full schema before acting on any field** — a
  request/result file's `job_id`, target/state strings, and any other
  field are checked against an explicit allowlist of valid values
  (e.g. a result file's `state` must be exactly `succeeded` or
  `failed`; an agent must never be allowed to self-report `queued`/
  `in_progress`) before any of it is trusted, not parsed permissively
  and patched up later.
- **The result file lives in a root-owned directory the unprivileged
  process cannot write to.** This is the actual privilege boundary: an
  unprivileged agent process can request an update (by writing to a
  directory it owns) but cannot forge the *outcome* of one, because it
  has no write access to the directory the privileged updater writes
  its result into. Don't collapse this into a single shared directory
  for convenience — the whole point is that request and result live in
  directories with different ownership.
- **A successfully consumed result file is deleted**; a
  malformed/oversized/symlinked one is left in place and logged, never
  silently removed — this keeps a rejected file diagnosable by whoever
  investigates it, rather than disappearing along with the evidence of
  what was wrong with it.
- **Write atomically**: temp file in the same directory, then rename
  over the final path — never a window where a partially written
  request/result file could be read half-formed by the other side.

## Database migrations

- **Migrations must preserve existing data.** A migration that changes a
  table's shape (adds a column to a composite primary key, changes a
  `NOT NULL`/default, etc.) copies existing rows into the new shape
  inside the same transaction as the schema change — never `DROP TABLE`
  without a preceding `INSERT INTO ... SELECT` that carries every
  pre-existing row forward (see `0002_limits_settings.sql`'s
  `alerts_sent` rebuild: create `_new` table, `INSERT ... SELECT` with an
  explicit default for the new column, `DROP` the old table, `RENAME`
  the new one into place).
- **Every migration that changes an existing table's shape needs an
  upgrade test**: open a database created by only the prior migration(s)
  with pre-existing rows in the affected table, then `Open` through the
  normal migration path and assert those rows are still present (with
  the new column's backfilled value) rather than lost or defaulted
  incorrectly. See `TestOpen_UpgradeFrom0001PreservesAlertsSent`.
- New tables/columns default to values that make the old behavior the
  effective one (e.g. `direction TEXT NOT NULL DEFAULT 'out'` on
  `alerts_sent`, since v0.1 only ever alerted on outbound) so an upgraded
  hub's existing alert history reads correctly under the new schema
  without a separate backfill step.

## Config / env-file backward compatibility

- **A config/env-file change must never break an existing, unmodified
  `hub.env`/`agent.env` written by an older installer or by hand.** New
  `CP_*` variables get a default that reproduces the pre-existing
  behavior when the variable is absent — the same additive-only
  contract D-048 applies to the JSON API applies here to on-disk config.
  Nothing in `internal/config` may require a variable that didn't exist
  before to be present for an old env file to keep loading.
- **Every value the installers write into an env file goes through
  `validate_env_value` first** (see the Shell scripts section above) —
  this is also what keeps `hub.env`/`agent.env` forward-compatible with
  `systemd-unit`'s `ParseExisting`/`Apply` (D-057/D-058): those only
  parse `ExecStart=`/`EnvironmentFile=`/`User=`/`Group=`/
  `ReadWritePaths=` out of the *unit* file, never the env file's
  contents, so an env file's own shape is never a factor in whether a
  future `systemd-unit apply` succeeds.
- **`update`'s `systemd-unit apply` (D-058) never touches the env
  file.** It only rewrites the unit file (`ExecStart=`, hardening
  directives, etc.); secrets/settings in `hub.env`/`agent.env` are
  untouched by any self-update path — the only way they change is an
  explicit installer flag or a manual edit.

## No secrets logged

## Dependencies

- Pin **exact versions** in `go.mod` (no `^`/range semantics — Go modules
  pin exact versions by default; don't bump without reason).
- Every new dependency requires a justification entry in
  `DECISIONS_LOG.md` before it's added.
- **No AWS SDK.** AWS API calls use hand-written SigV4 signing to keep the
  dependency graph and binary size minimal.

## Embedded frontend standards

- **File layout** (`web/assets/js/`): `main.js` (routing, page
  render/refresh loop, token dialog), `api.js` (`fetch` wrapper, 401
  handling), `format.js` (pure formatting helpers — bytes, percentages,
  durations — unit-tested independently of the DOM), `components.js`
  (shared DOM-builder helpers), `charts.js` (uPlot chart construction),
  `egress.js` / `buckets.js` (page-specific rendering for those
  sections). Keep pure/testable logic (`format.js`) free of DOM/`fetch`
  calls so it can be exercised by `node --test` without a browser.
- **Vanilla ES modules.** No bundler, no build-time transpilation for JS.
- **DOM builder rule**: build elements programmatically (`el(tag, attrs,
  children)`-style helpers in `components.js`) and set dynamic content
  via `textContent`, never `innerHTML` with interpolated/request-derived
  data. See `DECISIONS_LOG.md` D-023.
- **Pure JS logic is tested with `node --test`** (`web/test/*.test.mjs`),
  no browser/DOM required for those tests. Browser-dependent behavior
  (rendering, chart output, the token dialog flow) is verified manually
  with a headless browser during development, not via CI browser tests.
- **No CDN at runtime.** Every third-party asset (e.g. uPlot) is vendored
  under `web/assets/vendor/` with its license file committed alongside it.
- **Tailwind CSS is prebuilt and committed** (`web/assets/app.css`) via
  `make css`. End users and CI never need Node.js to build or run the hub;
  run `make css` and commit the regenerated file whenever a Tailwind
  class changes in `index.html` or any JS module.
- **CSP-compatible**: no inline `<script>` blocks, no inline event handler
  attributes (`onclick=` etc). All behavior lives in `assets/js/*.js`.
- **Accessibility**: semantic HTML elements over generic `div` soup;
  `role="progressbar"` + `aria-valuenow`/`aria-valuemin`/`aria-valuemax` on
  progress bars; visible keyboard focus states; text/background contrast
  meets WCAG AA; respect `prefers-reduced-motion` for any animation.

## Python scripts (`scripts/*.py`)

Used for dev/test tooling only (demo seeding, fake release/webhook
servers for `smoke.sh`/`test-update.sh`) — never shipped in a release
binary.

- **PEP 8**, `python3 -m py_compile scripts/*.py` clean.
  `ENFORCED BY: pre-commit (when python3 is on PATH), CI`
- **Type hints everywhere** (`from __future__ import annotations` +
  PEP 604 `X | Y` unions), module and function docstrings.
- **`@dataclass` for plain data records** (e.g. a demo host's fixed
  fields); `@staticmethod`/`@classmethod` only for methods that
  genuinely don't need instance state — a method that reads/writes
  `self` must be a plain instance method, not disguised as static.
- **`argparse` for CLI entry points**, never hand-rolled `sys.argv`
  parsing.
- **No bare `except:`.** Catch the narrowest exception type the call can
  actually raise (e.g. `except urllib.error.HTTPError` /
  `except urllib.error.URLError`, not a blanket `except Exception`
  unless re-raising or logging-and-continuing is genuinely correct for
  every possible exception there).
- **`pathlib.Path`** for any real filesystem path manipulation (joining,
  suffix checks, existence checks) — a bare `str` is fine for values
  that are only ever passed through unmodified (e.g. a SQLite connection
  string handed to `sqlite3.connect`).
- **`if __name__ == "__main__": sys.exit(main())`** guard in every
  script with a CLI entry point.
- **Unit tests** for pure logic (not the parts of a script that are
  inherently I/O, like an `http.server` handler) live in
  `scripts/tests/test_*.py`, run via `python3 -m unittest discover -s
  scripts/tests` (standard library only, no `pytest`/third-party test
  dependency) — matching the Go test suite's own stdlib-only convention.
  `ENFORCED BY: pre-commit (when python3 is on PATH), CI`
- A `Handler` class needing closure access to a `main()`-local variable
  (e.g. an `http.server.BaseHTTPRequestHandler` subclass reading a
  fixture directory or port chosen at runtime) may be defined locally
  inside `main()` rather than at module scope — an accepted, documented
  exception to PEP 8's usual module-level-class preference for a
  single-purpose test-fixture script, not a pattern to reach for in
  general-purpose code.

## JavaScript syntax checking

- **`node --check <file>` on every staged `.js` file** under
  `web/assets/js/` — a fast syntax-only check that runs in the
  pre-commit hook before `node --test` (which only CI runs, since it
  executes the full test suite) would catch the same error.
  `ENFORCED BY: pre-commit, CI (node --test also compiles every module)`
- See the "Embedded frontend standards" section above for the full set
  of JS rules (DOM-builder/CSP, vanilla ES modules, no CDN, pure-logic
  testing via `node --test`).

## Shell scripts (installers)

- Bash, with `set -euo pipefail` at the top of every script.
- Quote every variable expansion (`"$var"`, `"${arr[@]}"`).
- Scripts are idempotent — running them twice produces the same end state
  (re-running an installer is the upgrade path, see D-033).
- `bash -n script.sh` must be clean, and `shellcheck -S warning` must be
  clean on every `.sh` file (enforced by pre-commit/CI).
- **All top-level logic lives inside functions; `main "$@"` is the last
  line of the file.** A `curl | bash`-piped script that gets cut off
  mid-transfer must fail to parse, never execute a truncated fragment.
  See D-025. Do not add any top-level statement after `main "$@"`, or
  before it other than function/variable declarations.
- **Every value written into an env file goes through
  `validate_env_value VALUE FLAGNAME` first.** It rejects embedded
  newline/CR, a leading `#`/`export `, embedded whitespace, and embedded
  backslash — see D-028 for why each of these classes is dangerous.
  Never skip this for a new flag that ends up in `hub.env`/`agent.env`.
- **Never `source` or `eval` a file whose content ultimately originates
  from a CLI flag** (that includes `hub.env`/`agent.env`). Use
  `load_env_file_safe()`'s plain string-operation line parser and pass
  the result as literal `env -i` argv elements instead. See D-029.
- **Install binaries atomically**: `install -m 0755 SRC DST.new` then
  `mv -f DST.new DST`, never `install` directly onto the final path. See
  D-030.
- **Sandbox testability**: every real system path is computed once in a
  `setup_paths()`-style function with a sandbox-root prefix (empty
  string in real installs) so `scripts/test-install.sh` can exercise the
  full script as an unprivileged user via `CP_INSTALL_ROOT`. Only
  operations that inherently require root or a live systemd/user
  database (`useradd`, `chown`, `systemctl`) are replaced with a logged
  "sandbox: would run ..." line; everything else (download, checksum,
  file installation, unit rendering, `systemd-analyze verify`) runs for
  real against the sandboxed paths.
- **Interactive prompts (menu, confirmations, hidden token input) read
  from and write to `/dev/tty` only, never the script's own
  stdin/stdout.** A `curl | sudo bash` invocation's stdin is the script
  body itself — reading a prompt answer from stdin there would read
  installer source text, not a keystroke. Use the existing
  `prompt_tty`/`read_line_tty`/`read_hidden_tty`/`confirm_tty` helpers
  for any new prompt; gate whether the menu/prompts run at all on
  `tty_available()` (checks `CP_NONINTERACTIVE` and that `/dev/tty` opens
  for both read and write), never on whether stdin looks like a tty. See
  D-056. Any new non-interactive test case in `scripts/test-install.sh`
  must still set `CP_NONINTERACTIVE=1` so it can never block if it
  happens to run with a real terminal attached; new interactive cases
  drive a real pty via util-linux `script` so `/dev/tty` actually exists.
- **Systemd unit content is owned exclusively by
  `internal/systemdunit.Render`.** Neither installer script may hand-edit
  its `render_unit_fallback()` heredoc without making the identical
  change in `internal/systemdunit`'s `renderHub`/`renderAgent` first —
  the heredoc exists only as a fallback for a pre-v0.3.1 binary (or an
  explicit `--version` pin to one) and must stay byte-identical to
  `Render`'s output, verified by `scripts/test-install.sh`'s hard-diff
  drift test (`CP_INSTALL_FORCE_SCRIPT_UNIT=1`). See D-057. A change that
  touches one template without the other will fail that test, by
  design — do not soften the drift test's comparison to make such a
  change pass; fix the mismatched template instead.

## Test portability (cross-OS CI matrix)

CI runs `go test ./...` on `ubuntu-latest`, `ubuntu-24.04-arm`,
`macos-latest`, and `windows-latest`. Tests must pass on all four without
OS-specific skips, except where the behavior under test is itself
OS-specific by design.

- **Never hardcode a path separator in an expected value.** Build
  expected filesystem paths with `filepath.Join`/`filepath.ToSlash`, not
  string literals containing `/` or `\`.
- **Fixtures whose validity depends on OS-specific filtering rules
  (disk partitions, mountpoints, network interfaces) must be OS-aware.**
  Either construct a fixture that's valid under every target OS's rule
  set, or gate the test/fixture explicitly with a `runtime.GOOS` check
  when the behavior itself differs by OS (e.g. Darwin's disk-partition
  rule only allowing `/` and `/Volumes/*`).
- **Close any `*sql.DB`/file handle before a `t.TempDir()`-based test
  returns.** Windows cannot delete a directory containing an open file
  handle; letting `t.Cleanup`/deferred `Close()` run before the test
  function returns (not relying on process exit) avoids a "file in use"
  cleanup error on Windows runners.
- **Verification tools used during development** (not part of the
  enforced CI suite, but useful when adding tests):
  `CGO_ENABLED=1 go list -deps -f '{{if and (not .Standard)
  .CgoFiles}}{{.ImportPath}}{{end}}' ./...` to confirm no CGO leaked in;
  cross-`GOOS`/`GOARCH` `go build ./...` / `go vet ./...` for each of the
  8 release targets before assuming a change is portable; `go test -c -o
  /dev/null ./pkg/` to confirm a test binary at least *compiles* for an
  OS you can't run tests on locally (e.g. windows/amd64 from Linux).

## Git hooks setup

Run `make hooks` once after cloning (equivalent to
`git config core.hooksPath .githooks`). This points Git at the versioned
hooks in `.githooks/` (`pre-commit`, `commit-msg`) so the non-negotiable
rules above are checked locally before they hit CI.

## Commit message format

[Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short summary>

<optional body>

<optional footer>
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`,
`ci`, `chore`.

Examples:

```
feat(hub): add CIDR allowlist middleware

Checks http.Request.RemoteAddr against CP_ALLOWED_CIDRS before routing.
Never trusts X-Forwarded-For.
```

```
fix(agent): reset network delta to zero on counter rollover

Prevents a huge negative-then-positive spike in net_tx_bps when an
interface counter wraps.
```

```
docs(conventions): add errcheck justification example
```

## Patterns observed during integration (Step 2 phase B)

- **Consumer-defined interfaces get compile-time assertions in `cmd/**`,
  not just runtime wiring.** `cmd/hub/main.go` declares
  `var _ hub.Store = (*storage.DB)(nil)` (and similarly for
  `BucketCollector`, `Notifier`) right next to the imports. This catches
  method-set drift between independently-developed adapter and consumer
  packages at `go build` time instead of at wiring runtime, and documents
  the contract for readers without needing to open both packages.
- **`cmd/**` wiring wraps every phase of the process lifecycle in its own
  bounded step**: load config → open store (ctx-bound) → build adapters →
  construct the consumer → start background work in a goroutine → serve →
  on shutdown signal, `Shutdown(ctx)` the HTTP server with a timeout, stop
  the background context, wait for the background goroutine, wait for any
  in-flight async work (e.g. `(*hub.Server).Wait()` for alert
  notifications), then close the store. Each step has an explicit
  deadline; nothing blocks forever.
- **`-check-config` and startup logs redact by presence, not partial
  value.** Print `"(set)"`/`"(not set)"` for secret-shaped config fields,
  never a length, prefix, or hash. If a config struct grows a new secret
  field, add it to the redaction list in the same change.
- **Windows-safe SQLite tests**: `storage.Open` builds its DSN with
  `filepath.ToSlash(path)` specifically so the same test code produces a
  valid `file:` URI on both `/`-style and `\`-style paths; storage tests
  use `t.TempDir()` (never a hardcoded path) and close the `*DB` before
  the test returns so Windows can delete the temp directory during
  cleanup without a "file in use" error.
- **Fake `Store`/`BucketCollector`/`Notifier` in `internal/hub` tests**:
  rather than spinning up `internal/storage`'s real SQLite (which would
  make `internal/hub` depend on `internal/storage`, violating the
  dependency-direction rule), hub's own tests define minimal in-memory
  fakes satisfying the same interfaces it declares. This is the standard
  shape for testing any package that only depends on interfaces it
  defines itself.
- **Subcommand pattern: dispatch before flag parsing or config
  loading.** `cmd/hub/main.go` and `cmd/agent/main.go` both check
  `os.Args[1]` for a known subcommand name (currently just `"update"`)
  as the literal first statement in `main`, before `flag.Parse()` or
  `config.LoadHub`/`LoadAgent` ever runs, and call `os.Exit` directly
  with the subcommand's own return code (`os.Exit(runUpdate(os.Args[2:]))`).
  The subcommand builds its own `flag.NewFlagSet(name,
  flag.ContinueOnError)` rather than reusing the top-level `flag`
  package's default flag set, so its flags never collide with or get
  parsed alongside the binary's normal flags. This is what lets
  `cloud-pulse-hub update`/`cloud-pulse-agent update` work on a
  completely unconfigured install (no `CP_AGENT_TOKEN`/`CP_HUB_URL`
  required) — config loading simply never happens on that path. A
  subcommand's own logic lives in `cmd/*/<name>.go` (e.g.
  `cmd/hub/update.go`) and returns a plain `int` exit code rather than
  calling `os.Exit` itself, keeping `os.Exit` calls confined to
  `cmd/*/main.go` per the non-negotiable rule above. Any future
  subcommand follows the same shape: check `os.Args[1]` first, own
  flag set, return an `int` for `main` to pass to `os.Exit`.
