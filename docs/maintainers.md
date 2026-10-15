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

## Regenerating documentation screenshots

Use an owned sandbox with a fresh repository-local, git-ignored data directory,
dummy credentials and an unused loopback test port, never a live hub or its
data. Set its password via `reset-password --password-stdin` **before** starting
it, then seed only demo data with `scripts/demo-seed.py`. Keep temporary files,
browser profiles and captures in private repository-local scratch; stop only
processes you started. Time-relative labels and demo charts vary between runs;
pixel-identical recaptures are not promised.

`scripts/capture-screenshots.mjs` supports a reviewed shots JSON plan,
a password file, an output directory, and per-shot `lang`, theme and viewport settings.
Capture into private scratch first, not directly into `docs/screenshots`.
Do **not** blindly run the default plan: its legacy `diagnosis` actions click
**Send test**. Remove send/save/refresh actions and never configure real
notification destinations or cloud credentials for documentation capture.
Interactive notification and billing examples require browser responses stubbed
**before dispatch**, with unmatched writes/outbound requests denied; ordinary
demo seeding does not reproduce those states. Do not run installers, reveal or
copy tokens, upload files, or access original photos during capture.

The eight refreshed images have these specific fixture boundaries:

| Image | Caption / evidence boundary |
|---|---|
| `overview.png`, `ko/ko-overview.png` | Isolated demo fleet; not live measurements or provider truth. |
| `host-detail.png` | Demo host metrics and inventory, not a real host observation. |
| `alerts.png` | Historical alert-event fixtures, not real incidents or proof of delivery. |
| `mobile-overview.png` | Browser-only empty host-list GET over seeded data; the backend is not entirely empty. |
| `add-agent.png` | Masked install guide; no command or token exposed or executed. |
| `notifications.png` | Browser-stubbed test acceptance; no send/delivery verification, recorded health unchanged. |
| `costs.png` | Illustrative billing fixtures, not actual provider charges or live billing. |

Earlier images, including `mobile.png`, were not refreshed with this set.
Review every intended public image for visible secrets and personal markers;
DOM scrubbing and PNG metadata stripping are not a visual privacy signoff.
Publish only approved unchanged capture bytes after hash/PNG metadata checks.
Local capture reports are bookkeeping, not portable GitHub evidence; keep them
untracked. Run the repository checks after documentation changes:

```bash
CGO_ENABLED=0 go test ./...
npm test
node scripts/check-i18n.mjs --json
python3 scripts/check-docs.py --check-flags
python3 scripts/prepublish-check.py
git diff --check
```

Tree findings (including PNG metadata and personal markers) are hard failures.

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

## Reviewed historical detector fixtures

`prepublish-check` accepts only exact, source-context-reviewed synthetic
historical `internal.host_ip` or `internal.home_path` findings listed in
`.prepublish-historical-fixtures.json`. Each entry binds a full commit,
repository-relative path, line, rule, and SHA-256 of that historical UTF-8
line (excluding its newline), with bounded plain-text rationale. The manifest
must not store matched values or matched-value hashes. Schema, provenance,
reachability and line content are validated; every entry must match exactly
once during a full history scan. This does not exempt a file, line, test,
current-tree finding, secret, local marker, or hygiene finding.

`history findings=0` means zero unresolved historical findings, not zero
detector matches. Accepted fixtures remain visible and separately counted;
the current manifest accounts for nine reviewed findings. The existing
`.prepublish-baseline.json` policy and D-084 are unchanged: known historical
disclosures still warn, and new findings still fail. A baseline cannot rescue
invalid fixture metadata or turn an accepted fixture into a warning.
Tree-only runs validate fixture targets but explicitly skip full-history
match-count validation and make no zero-history claim.

## Dashboard design system

Phase 1 establishes shared styling; it does not rearrange the server grid,
host detail, or Activity. API bindings, counts, and chart samples remain owned
by the existing page modules. Never add invented measurements to fill a UI.

- `web/src/tokens.css` owns visual colors, type sizes/leading, weights,
  tracking, radii, elevation, motion, and focus. Page layout geometry remains
  local. Dark surfaces are neutral; accent blue identifies interaction,
  while status hues identify health.
- Complete `--color-cp-*` values are used as `var(--color-cp-*)` for opaque
  CSS colors. For translucency, use semantic triplets such as
  `rgb(var(--status-ok) / 0.15)` or `rgb(var(--accent) / 0.1)`.
  Never wrap a hex token in `rgb()`. Shared status styles avoid `color-mix()`
  so existing computed-color checks can measure them.
- `components/typography.css`, `card.css`, `status.css`, and `meter.css`
  each own their shared classes. Pages may add scoped modifiers, not another
  base definition. Keep legacy names (`cp-dot-up/down`, `cp-chip-other`)
  until their callers migrate.
  Card titles remain 16px; body/helper type remains 14px/12px. Named sizes
  require explicit leading. Legacy badges/chips intentionally inherit their
  context's line height to preserve table and toolbar density.
- Base and stat cards share border, fill, radius, and elevation. Compact and
  stacked cards are opt-in. Dots are 10px (large: 12px); tracks are 6px
  (large: 8px). Unknown states use hollow dots or empty/striped frames, not
  a fabricated zero. Status meaning must also appear in text or an icon.
- Body/secondary text, links, status text on supported 10–20% tints, and
  white text on opaque badges are tested at 4.5:1 in both themes. Chart series
  and focus rings are tested at 3:1 against the four named surfaces. These
  are token/component checks, not a claim that every dashboard state is
  fully audited. Decorative borders, grid lines, distribution segments,
  bright dots, and usage fills are not covered by that contrast guarantee;
  the latter require adjacent labels/values.
- Chart tokens remain complete six-digit hex strings for the existing
  canvas alpha-fill code. `SERIES_COLORS` keeps enumerable, read-only
  properties with dynamic getters. Existing chart recreation on theme change
  picks up new colors; it does not change data inputs or chart lifecycle.
- Use keyboard-visible focus, gate decorative hover effects to hover-capable
  devices, and use shared motion tokens. Reduced motion globally collapses
  transitions and prevents repeating pulses. Preserve Korean labels through
  wrapping in shared card/meter components rather than truncation.

Run `make css` after source changes; `web/assets/app.css` is generated.
Run `make test-js` for syntax and source regressions. The isolated
`scripts/check-design-system.mjs` gallery provides browser evidence without
API data or authentication. It covers compact/stacked cards, legacy and large
status indicators, badges/chips, empty meters, long English/Korean labels,
isolated form controls, and selected-navigation pointer hover. Its controls
do not submit or save anything. Screenshots capture the whole document.
Run it on an unused loopback test port (default `18090`; `8090` is refused).
All reports, screenshots, Firefox profiles and browser temporary files stay
under repository-local `.verification/phase1`; locally exclude
`/.verification/` in `.git/info/exclude` without replacing existing entries.
Use `TMPDIR="$PWD/.verification/phase1/tmp"` for verification commands.
Live deployment and authenticated page audits are separate maintainer tasks,
not part of this styling check. Token tests and gallery evidence do not certify
every production dashboard state.

The overview grid uses `pages/hostgrid.js` for its card composition while
sharing the table's existing filtering, sort accessors and persisted view.
Grid sort controls use that same sort state. Cards are single native links,
not nested action menus. Resource percentages, capacities, byte rates, load,
uptime and monthly traffic come directly from `HostSummary`; missing numeric
fields stay unknown and offline samples are labeled as last reported.
Progress tracks are bounded, with original exceeded usage retained in text
and `aria-valuetext`. Do not add decorative trend data to these cards.

Alerts → Activity is the existing alert-event history, not admin audit data.
Its date groups use each occurrence's `started_at` in the browser's local
timezone and preserve API order/cursors. A resolved occurrence shows its
actual resolution time; it is not duplicated into an invented event. State,
rule/host snapshots, value/threshold and delivery outcomes remain grounded
in the existing API. There is no inferred severity or new mute/host-filter
control. Refresh reloads the newest page; Load more follows the API cursor.

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

## Static quality-matrix workflow check

Run `python3 scripts/check-quality-matrix.py` to check the supported static
profile of `.github/workflows/quality-matrix.yml`: manual dispatch, read-only
contents permission, CGO-disabled Go builds for the six declared targets,
native Go vet/tests, and Node 22/npm tests. It uses installed PyYAML's
`BaseLoader`; if PyYAML is unavailable the command fails clearly rather than
skipping validation. Duplicate mappings, merges, explicit tags, and unsupported
workflow shapes are rejected. This is not actionlint or complete GitHub
expression validation; it does not check action-version availability and does
not execute workflow jobs, builds, or native-platform tests.
