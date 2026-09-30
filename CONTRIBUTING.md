# Contributing to cloud-pulse

Thanks for contributing. cloud-pulse is a CGO-free Go hub/agent monitor; keep changes small, portable, and easy to review.

## Setup and validation

Install Go 1.25+ and optional development tools: Node 22+ (dashboard tests) and Python 3 (script tests).

Run `make hooks` once to use the versioned Git hooks:

<!-- contributing:run -->
```bash
make hooks
```

Build the hub and agent binaries:

<!-- contributing:run -->
```bash
make build
```

Run the Go test suite (`CGO_ENABLED=0` always):

<!-- contributing:run -->
```bash
make test
```

Compile and test the Python helper scripts:

<!-- contributing:run -->
```bash
make test-py
```

Syntax-check the dashboard JavaScript modules and run their Node tests:

<!-- contributing:run -->
```bash
make test-js
```

`make css` regenerates committed dashboard CSS after Tailwind-class changes — it requires network access to fetch the pinned `@tailwindcss/cli` package via `npx`, so it is not part of this file's automatically-run command blocks (see `scripts/test-contributing.sh`, which runs every `<!-- contributing:run -->` block against a fresh clone with an empty module/package cache and no guaranteed network access beyond what Go/npm themselves need for their own toolchains).

The complete local check (formatting, vet, CGO checks, errcheck, pinned staticcheck, shellcheck where available, JavaScript syntax checks, Python compilation/tests, and secret heuristics) is:

<!-- contributing:run -->
```bash
CP_CHECK_ALL=1 bash .githooks/pre-commit
```

Never use CGO, add runtime dependencies casually, or add secrets to fixtures. Run tests with `CGO_ENABLED=0`; keep platform-sensitive tests deterministic and compatible with Linux, macOS, and Windows.

## Code and commits

`CODING_CONVENTIONS.md` is authoritative for architecture, error handling, logging, security, tests, shell, and frontend safety. `docs/architecture.md` covers package layering, time sync, and egress accounting if you need the bigger picture before diving into a change. In brief: dependencies point inward, `internal/models` stays standard-library-only, contexts come first for I/O, errors are wrapped with context, dynamic dashboard data uses DOM APIs/textContent, and ignored errors need an inline reason.

Use Conventional Commits, for example `feat(alerting): add pager sender` or `fix(storage): preserve session expiry`. Keep a change focused and include tests for observable behavior.

## Extending cloud-pulse

- **Notifier:** add a sender in `internal/notify`, validate its config and SSRF posture, redact any secret fields in `models.NotifyChannel.SecretFields`, classify its failure modes through `notify.DiagnoseError` (add new `models.DiagnosisCode` values only for genuinely new failure shapes, citing the platform's real documented error format), test against `httptest`, and wire it through the command-layer factory (`internal/notify.New`) without exposing credentials in logs or argv. If the platform supports reading back a sent message, implement `notify.MessageReader`/`MessageDeleter` too so `cloud-pulse-hub notify verify` (see `docs/notifications.md`) can confirm delivery, not just acceptance.
- **Metric:** add the model and collection path, preserve old agent/hub JSON compatibility, persist/query it if charted, add deterministic collection and storage tests, then add its alert semantics (`internal/models/alerting.go`'s `AlertMetric` enum + `internal/alerting/evaluate.go`) and documentation (`docs/alerting.md`).
- **Storage provider** (Google Drive / Dropbox / a future addition): implement the `internal/poller.Provider` interface (`ID()`, `Kind()`, `Collect(ctx) (Snapshot, error)`, `Classify(err) Status`) in its own package under `internal/storageusage`, register it alongside the existing providers (`internal/storageusage/registry.go`), add an OAuth flow following the existing device-flow/PKCE pattern (never accept a raw password), keep the same quiet-skip status model (`not_configured`/`pending_oauth`/`auth_failed`/`permission_denied`/`error`/`ok`) and freshness tracking every other poller-backed provider uses, add fake-server-backed tests for both the OAuth exchange and the quota/usage parsing, and document the app-registration steps in `docs/storage.md`.
- **Page:** place pure helpers under `web/assets/js/core` or `ui` with Node tests; use existing components and CSP-safe DOM construction; rebuild CSS with `make css` whenever Tailwind classes change. A new Settings section additionally needs an entry in `web/assets/js/pages/settings/index.js`'s `SECTIONS` array (id, icon, label) — routing then follows automatically via `#/settings/<section>`.
- **Platform service integration** (a new OS beyond Linux/macOS/Windows for the agent's own service lifecycle): mirror the existing per-OS split — `internal/systemdunit` (Linux units) and `internal/launchd` (macOS plists) each expose a golden-tested `Render`/`ParseExisting`/`Apply`; `cmd/agent/service.go` (Windows) and `cmd/agent/plist.go`/`cmd/agent/systemdunit.go` dispatch from `cmd/agent/main.go`. `internal/selfupdate.RunFromRequest`'s platform gate (currently `linux|darwin|windows`) and each OS's own no-follow-symlink file open (`internal/selfupdate/nofollow_*.go`) both need a matching addition. Update `docs/install.md`/`docs/remote-updates.md` with the new platform's paths and mechanics.
- **Pricing plan / cloud billing provider:** builtin plans and provider CLIs are the exception to "small changes only" — see `CODING_CONVENTIONS.md`'s Exec rules and Audit logging sections before adding a new CLI-backed provider or a new builtin plan; any new admin-facing settings change (a rate, an interval, a plan edit) must call `(*Server).recordAudit` alongside its own `slog.Info` line.
- **Privileged agent-side helper** (anything analogous to remote update's request/result file exchange): follow `CODING_CONVENTIONS.md`'s "Privileged-helper file handling" checklist exactly — `O_NOFOLLOW` (or the Windows reparse-point-rejecting equivalent), a size cap, full schema validation before trusting a field, and a root-owned (or otherwise more-privileged) directory for anything the unprivileged side must not be able to forge.

## Releasing (maintainers)

Release notes are generated deterministically from Conventional Commits, never hand-written and never `--generate-notes`:

```bash
scripts/release-notes.sh vX.Y.Z > /tmp/notes.md
```

`release.yml` already calls this script itself and feeds its output to `gh release create --notes-file` — the command above is for previewing the notes before tagging, or for regenerating them if a tag needs to be re-released.

After tagging and pushing a release, prepend the same output to `CHANGELOG.md` (newest release on top, directly under the `# Changelog` header) in the same pull request that bumps any release-adjacent documentation. `scripts/tests/test_release_notes.py` covers the script's own output format (section grouping, breaking-change detection, upgrade/checksum blocks) against a temporary git fixture — it does not regenerate `CHANGELOG.md` itself, which stays a manual (but scripted) step per release.

## Pull requests

Explain the problem, implementation, tests run, compatibility/security considerations, and screenshots for dashboard changes. Do not commit generated binaries, local databases, screenshots unrelated to the change, or credentials.
