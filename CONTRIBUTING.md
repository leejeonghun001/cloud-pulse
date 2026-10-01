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

## Adding or changing UI text / adding a language

Every user-visible dashboard string goes through `t(key, params)` from
`web/assets/js/core/i18n.js` — never a hard-coded string literal in
`text:`/`textContent`/`innerText`/`title`/`aria-label`/`placeholder`/
`alt`/`label:` (in an `el()`/`dropdownItem()`/`selectField()` call),
`showToast({ message: ... })`, `createDialog({ title, description })`,
a command-palette `group:`, or `confirm()`/`alert()`.
`scripts/check-i18n.mjs` enforces this (wired into `.githooks/pre-commit`
and CI's `web` job) by scanning `web/assets/js/**` for these patterns,
including template literals with static text (`` `Delete ${name}?` ``)
and both branches of `key: cond ? "A" : "B"`. It is line-based, so a
sentence returned from a helper (`return \`...\``) is not detected —
reviewers still check those.

**Adding/changing a string:**

1. Add the key to **both** `web/assets/i18n/en.json` and
   `web/assets/i18n/ko.json`, nested under the right namespace
   (`common`, `nav`, `login`, `overview`, `host`, `alerts`, `costs`,
   `rollout`, `settings`, `health`, `errors`, ...). Keep the two files'
   key sets and `{placeholder}` sets identical — `check-i18n.mjs`
   fails on any drift.
2. Write natural Korean, not a machine-literal translation. Product
   names and API/technical terms (`cloud-pulse`, `Discord`, `AWS`,
   HTTP status names, etc.) stay in English in both catalogs.
3. Use `t("namespace.key", { param: value })` at the call site.
   `{name}`-style placeholders substitute from the `params` object; a
   `count` param selects a `{one, other}` plural pair for simple
   English/Korean plural handling (see `core/i18n.js`'s header comment).
4. If the string is genuinely not user-visible (a `console.*` message,
   an internal-only identifier the detector's heuristics mis-flagged),
   add a reasoned entry to `scripts/i18n-lint.allow.json` instead of a
   catalog key — every entry requires a non-empty `reason`.
5. `scripts/i18n-lint.pending.json` lists files not yet converted to
   `t()` (findings there don't fail the lint). Since v0.8 it is empty;
   keep it that way — new code is written with `t()` from the start.

**Adding a third language:** extend `SUPPORTED_LANGUAGES` and the
`getLang`/`detectAutoLang`/`intlLocale` resolution in
`web/assets/js/core/i18n.js`, add `web/assets/i18n/<lang>.json` with
every key from `en.json`, add the option to the language selector
(`web/assets/js/ui/language-select.js`), and extend `check-i18n.mjs`'s
parity check and `scripts/ui-audit.mjs --langs` to include it. Chart
PNGs (`internal/alerting/chart`) render text in a bitmap ASCII-only
font and intentionally stay English regardless of `notify_language` —
see DECISIONS_LOG.md.

Server-rendered notification text (Discord/Telegram/WhatsApp message
bodies, chart titles) is localized separately through the Go
`internal/i18n` package (`T(lang, key, params)`, catalogs under
`internal/i18n/catalog/*.json`), selected by the hub-wide
`notify_language` setting (`en`/`ko`, default `en`) — independent of
any signed-in admin's own dashboard language preference. Admins change
it in Settings → General → Notification language. Every alert, rollout
and channel-health message goes through it; add new message text there,
never as a Go string literal.

## Releasing (maintainers)

Release notes are generated deterministically from Conventional Commits, never hand-written and never `--generate-notes`:

```bash
scripts/release-notes.sh vX.Y.Z > /tmp/notes.md
```

`release.yml` already calls this script itself and feeds its output to `gh release create --notes-file` — the command above is for previewing the notes before tagging, or for regenerating them if a tag needs to be re-released.

After tagging and pushing a release, prepend the same output to `CHANGELOG.md` (newest release on top, directly under the `# Changelog` header) in the same pull request that bumps any release-adjacent documentation. `scripts/tests/test_release_notes.py` covers the script's own output format (section grouping, breaking-change detection, upgrade/checksum blocks) against a temporary git fixture — it does not regenerate `CHANGELOG.md` itself, which stays a manual (but scripted) step per release.

## Pull requests

Explain the problem, implementation, tests run, compatibility/security considerations, and screenshots for dashboard changes. Do not commit generated binaries, local databases, screenshots unrelated to the change, or credentials.
