# Maintainer checklist

Generic, repository-scoped tasks a cloud-pulse maintainer performs
outside of ordinary code review. This file intentionally contains no
personal data (hostnames, IP addresses, home directory paths, or
account names) — a maintainer's own operational notes for their
specific deployment belong in a private, un-tracked file outside this
repository, never here.

## Hub upgrades (own deployment)

Before upgrading a self-hosted hub, run the local verification kit
against a sandboxed copy of your real data directory, not the live
one:

1. `sudo scripts/hub-upgrade-check.sh pre` — captures service status,
   binary version, a verified SQLite backup (`PRAGMA integrity_check`),
   and an API/dashboard smoke test.
2. `sudo cloud-pulse-hub update --version vX.Y.Z` (pin an explicit
   version; do not upgrade to a moving `latest` target during a
   maintenance window).
3. `sudo scripts/hub-upgrade-check.sh post --expect vX.Y.Z` — compares
   against `pre.json`: service restarted, migrations applied, no data
   loss, default-password warning if still on `changeme`.
4. If `post` fails, `scripts/hub-upgrade-check.sh rollback` prints the
   exact commands to restore the pre-upgrade binary/unit/database
   (dry-run by default; `--apply` to execute after reviewing them).

See [docs/upgrade.md](upgrade.md) for the full runbook, expected
output at each step, and v0.3.x → v0.4+ specific notes (dashboard
sign-in introduced, `CP_UI_TOKEN` becomes an API-only token).

## Rotating/registering notify-e2e CI secrets

The `notify-e2e` GitHub Actions workflow authenticates against real
Discord/Telegram/WhatsApp test accounts using repository secrets
(`CP_VERIFY_*`, see [docs/notifications.md](notifications.md)). To
register or rotate them:

1. Obtain each credential from its own platform console (never share
   or paste a real credential anywhere outside the platform console
   and the command below).
2. Register it without it ever touching shell history or process
   argv:
   ```bash
   gh secret set CP_VERIFY_DISCORD_WEBHOOK_URL < /path/to/secret-file
   ```
3. Manually re-run the `notify-e2e` workflow once per rotated secret
   and confirm its job summary shows a successful send + read-back for
   the affected platform.
4. Delete the local secret file immediately after use.

Never paste a real credential into an issue, PR description, commit
message, or chat session.

## Cloud storage OAuth app registration

Google Drive and Dropbox usage monitoring (see
[docs/storage.md](storage.md)) require a maintainer to register an
OAuth application once per provider:

- **Google Drive**: create a Google Cloud project, configure the OAuth
  consent screen (test users only, unless publishing verification is
  pursued separately), create a client of type "TVs and Limited Input
  devices" (device-flow compatible), and record the client ID/secret.
- **Dropbox**: create an app in the Dropbox App Console with scoped
  access (App folder or Full Dropbox, per the deployment's own
  requirement) and the `account_info.read` scope; record the app key.

Neither client ID/secret nor app key is itself sufficient to access
data — the actual per-user OAuth grant happens from the hub's Settings
→ Storage accounts page, verified with `cloud-pulse-hub storage
verify`.

## `scripts/github-setup.sh --apply`

This script applies repository labels (from `.github/labels.yml`),
topics, and the repository description via the `gh` CLI. It defaults
to a **dry-run** (prints every `gh` call it would make; performs no
writes) and only mutates the real repository when passed `--apply`.

Before running with `--apply`:

1. Run without `--apply` first and review the full dry-run output.
2. Confirm `gh auth status` shows a token with `repo` scope for the
   target repository.
3. Re-run with `--apply` only once the dry-run output has been
   reviewed and matches intent.

This script is never invoked by CI — it is a deliberate, maintainer-run
action only.

## Public-repository final check (`scripts/prepublish-check.py`)

Before merging any change that touches test fixtures, documentation,
or screenshots, run:

```bash
python3 scripts/prepublish-check.py
```

- **Tree findings are always a hard failure.** Fix them before
  merging — replace a real address/path with a documentation-range
  placeholder ([RFC 5737](https://www.rfc-editor.org/rfc/rfc5737)/
  [RFC 3849](https://www.rfc-editor.org/rfc/rfc3849) ranges), or add an
  `allow-secret-scan: <reason>` comment for a deliberately fake
  fixture value.
- **History findings are checked against `.prepublish-baseline.json`.**
  A finding already recorded there is a warning (already public,
  consciously not scrubbed); a new one still fails. This repository
  does not rewrite published history to remove an old finding — a
  force-push is destructive to every existing clone and doesn't
  actually un-publish anything already fetched. If a genuinely new
  secret is found in history, rotate the credential immediately;
  whether to also attempt history rewriting is a case-by-case call for
  whoever holds the repository's admin rights, informed by the tool's
  markdown report (`--report FILE`).
- A maintainer's own real hostname/IP/email that should never be
  flagged (because it's already known to be sensitive and won't be
  committed) goes in a local, git-ignored
  `.prepublish-markers.local` file or the `CP_PREPUBLISH_MARKERS`
  environment variable — never in a tracked file. The local file is
  UTF-8, one marker per non-empty line; lines beginning with `#` are
  comments. The environment variable accepts the same markers separated
  by commas or newlines. Markers may be exact IPv4/IPv6 addresses, an
  IPv4 prefix ending in `.`, hostnames (word-boundary matching), emails,
  or arbitrary strings. The scanner reports a marker number and a
  redacted match length only; it never prints the marker value.

## Release checklist

1. Confirm CI is green on the release branch (all jobs, including
   `prepublish` and `hub-upgrade`).
2. Generate release notes: `scripts/release-notes.sh vX.Y.Z >
   /tmp/notes.md` and review them.
3. Tag and push: `git tag -a vX.Y.Z -m "cloud-pulse vX.Y.Z"` then `git
   push origin vX.Y.Z`.
4. Watch the `release.yml` workflow build and publish assets.
5. Prepend the same release notes to `CHANGELOG.md` in the same pull
   request that bumps any release-adjacent documentation.
6. Rehearse the upgrade path from the previous release in a sandbox
   before announcing (`scripts/hub-upgrade-check.sh` / SPEC upgrade
   kit), not against a live production hub.
