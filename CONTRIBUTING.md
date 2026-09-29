# Contributing to cloud-pulse

Thanks for contributing. cloud-pulse is a CGO-free Go hub/agent monitor; keep changes small, portable, and easy to review.

## Setup and validation

- Install Go 1.25+ and optional development tools: Node 22+ (dashboard tests) and Python 3 (script tests).
- Run `make hooks` once to use the versioned Git hooks.
- Use `make build`, `make test`, `make lint`, `make staticcheck`, `make test-py`, and `make test-js` during development. `make css` regenerates committed dashboard CSS after Tailwind-class changes.
- The complete local check is `CP_CHECK_ALL=1 bash .githooks/pre-commit`; it runs formatting, vet, CGO checks, errcheck, pinned staticcheck, shellcheck where available, JavaScript syntax checks, Python compilation/tests, and secret heuristics.

Never use CGO, add runtime dependencies casually, or add secrets to fixtures. Run tests with `CGO_ENABLED=0`; keep platform-sensitive tests deterministic and compatible with Linux, macOS, and Windows.

## Code and commits

`CODING_CONVENTIONS.md` is authoritative for architecture, error handling, logging, security, tests, shell, and frontend safety. In brief: dependencies point inward, `internal/models` stays standard-library-only, contexts come first for I/O, errors are wrapped with context, dynamic dashboard data uses DOM APIs/textContent, and ignored errors need an inline reason.

Use Conventional Commits, for example `feat(alerting): add pager sender` or `fix(storage): preserve session expiry`. Keep a change focused and include tests for observable behavior.

## Extending cloud-pulse

- **Notifier:** add a sender in `internal/notify`, validate its config and SSRF posture, redact any secret fields in `models.NotifyChannel.SecretFields`, test against `httptest`, and wire it through the command-layer factory without exposing credentials in logs.
- **Metric:** add the model and collection path, preserve old agent/hub JSON compatibility, persist/query it if charted, add deterministic collection and storage tests, then add its alert semantics and documentation.
- **Page:** place pure helpers under `web/assets/js/core` or `ui` with Node tests; use existing components and CSP-safe DOM construction; rebuild CSS with `make css` whenever Tailwind classes change.

## Pull requests

Explain the problem, implementation, tests run, compatibility/security considerations, and screenshots for dashboard changes. Do not commit generated binaries, local databases, screenshots unrelated to the change, or credentials.
