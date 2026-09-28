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
                         index.html, assets/app.js, assets/app.css, assets/vendor/uplot.*
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

## Dependencies

- Pin **exact versions** in `go.mod` (no `^`/range semantics — Go modules
  pin exact versions by default; don't bump without reason).
- Every new dependency requires a justification entry in
  `DECISIONS_LOG.md` before it's added.
- **No AWS SDK.** AWS API calls use hand-written SigV4 signing to keep the
  dependency graph and binary size minimal.

## Embedded frontend standards

- **Vanilla ES modules.** No bundler, no build-time transpilation for JS.
- **No CDN at runtime.** Every third-party asset (e.g. uPlot) is vendored
  under `web/assets/vendor/` with its license file committed alongside it.
- **Tailwind CSS is prebuilt and committed** (`web/assets/app.css`) via
  `make css`. End users and CI never need Node.js to build or run the hub.
- **CSP-compatible**: no inline `<script>` blocks, no inline event handler
  attributes (`onclick=` etc). All behavior lives in `assets/app.js`.
- **Accessibility**: semantic HTML elements over generic `div` soup;
  `role="progressbar"` + `aria-valuenow`/`aria-valuemin`/`aria-valuemax` on
  progress bars; visible keyboard focus states; text/background contrast
  meets WCAG AA; respect `prefers-reduced-motion` for any animation.

## Shell scripts

- Bash, with `set -euo pipefail` at the top of every script.
- Quote every variable expansion (`"$var"`, `"${arr[@]}"`).
- Scripts are idempotent — running them twice produces the same end state.
- `bash -n script.sh` must be clean (enforced by pre-commit/CI on every
  `.sh` file).

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
