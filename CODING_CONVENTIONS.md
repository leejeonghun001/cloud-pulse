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
- **Use `net/http/httptest`** for HTTP handler and client tests instead of
  binding real sockets where avoidable.

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

- **Constant-time comparison** for bearer tokens (`crypto/subtle`), never
  `==` or `strings.Compare`.
- **CIDR allowlist checks `http.Request.RemoteAddr` only.** Never trust
  `X-Forwarded-For` or similar client-supplied headers for access control.
- **`http.MaxBytesReader`** on every endpoint that accepts a request body.
- **Security headers** (CSP, `X-Content-Type-Options`, `Referrer-Policy`,
  `X-Frame-Options`) are applied by middleware to every response, not
  per-handler.
- **No secrets in the repo.** Tokens, keys, and credentials are supplied via
  environment variables only; nothing resembling a secret is ever committed,
  including in test fixtures (use obviously-fake values).
- **Admin endpoint rule**: any endpoint that can reveal a secret (the
  agent token) or make a state-changing settings/limit change is an
  *admin* endpoint, wrapped with `requireAdmin` (not `requireUIToken`).
  `requireAdmin` must check `CP_UI_TOKEN == ""` **first** and respond
  `403 {"code":"admin_disabled"}` in that case, before ever inspecting
  the request's bearer header — there is no configuration in which an
  admin endpoint falls back to an open/unauthenticated read just because
  `CP_UI_TOKEN` happens to be unset. Only when `CP_UI_TOKEN` is
  configured does a missing/wrong token get `401`. New admin routes must
  use `requireAdmin`, not `requireUIToken`, even if they're a `GET`.

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
