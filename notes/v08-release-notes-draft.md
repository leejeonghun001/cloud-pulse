# v0.8.0 Release notes — DRAFT

> Draft for Hun's review; the final text goes into the GitHub release and
> `CHANGELOG.md` (regenerate the commit lists with `scripts/release-notes.sh`
> once v0.8.0 is tagged). Base: v0.7.0 (e925870) → v0.8.0 (78 commits
as of this draft; the count moves with every pre-release commit —
regenerate at tag time).

## Highlights

**Fleet rollout dashboard.** A new Fleet → Rollout page shows how the
agent fleet is distributed across versions and OSes, which hosts are
behind, and what the recent remote-update batches did — with a
one-click "update every outdated host that supports it" action
(confirm dialog, selectable parallelism). Backed by two new APIs,
`GET /api/v1/fleet/versions` and `GET /api/v1/agents/updates/batches`.

**Update-failure alerts.** Two new alert-rule presets:
`update_batch_failed` (one notification per failed batch, with a
success/failure bar chart attached) and `agent_outdated` (per-host,
default 14-day threshold, with cooldown). Both reuse the existing
rule engine and channels; the chart images stay English-only by
design (bitmap font limitation) while message bodies follow the
notification language.

**Notification channel health.** Opt-in weekly synthetic tests per
channel (weekday + time, stored in UTC) run through the same send
path as `cloud-pulse-hub notify verify`, with read-back + cleanup on
Discord/Telegram. The new Settings → Notifications → Health tab shows
per-channel status badges (healthy/degraded/failing/unknown), 7/30-day
delivery success rates, avg/max latency, a 12-week synthetic-test
strip, and diagnosis cards. If a channel keeps failing — three
consecutive delivery failures or a failed synthetic test — cloud-pulse
notifies through a different healthy channel (never itself), and falls
back to a dashboard banner when no healthy channel exists.

**Dashboard in English and Korean.** Every screen, aria-label, dialog
and toast is localized (`web/assets/i18n/en.json`, `ko.json`), with a
per-browser language selector (Auto / English / 한국어) on the sign-in
page and in Settings → General. Server errors keep their English
messages but map to localized text by error code. A new hub-wide
notification language setting (Settings → General → Notification
language, or `PUT /api/v1/settings/notify-language`) picks the
language of alert and channel-health messages.

**/login and deep links fixed.** The v0.7-era router served 404s for
deep links such as `/login` or `/settings/network`; the hub now serves
the SPA for every known dashboard route.

## Fixes since v0.8.0-rc.2

- `POST /api/v1/agents/updates` now returns `400 hosts_not_found`
  when *every* requested host is unknown at creation time, instead of
  creating an empty job batch that nothing would ever run (batches
  where at least one host still exists keep the documented
  per-host skip behavior). Found in a post-deployment live-exercise
  round; covered by an HTTP-level regression test.
- `docs/maintainers.md` states the screenshot reproducibility
  expectation precisely: layout, labels and text reproduce run to run
  (dataless screens such as the sign-in page come out byte-identical),
  while screens embedding live metrics vary pixel-wise with seed time
  by design — so a pixel delta on regenerated shots is not read as UI
  drift.

## Upgrade

```bash
sudo cloud-pulse-hub update
sudo cloud-pulse-agent update
```

Upgrades from v0.3.x and v0.7.0 were rehearsed end to end with
`scripts/hub-upgrade-check.sh` (pre DB backup → update → post
preservation checks → rollback), including the negative cases
(deleted host, skipped restart, stale unit — each correctly FAILs).
Run `sudo scripts/hub-upgrade-check.sh pre` first and follow
`docs/upgrade.md` for the full runbook. v0.3.x → v0.8 adds dashboard
sign-in (admin/changeme, forced change on first login) and moves
`CP_UI_TOKEN` to API-token use only.

Database migrations 0001–0007 apply automatically on first start;
migration `0007_v08` adds the synthetic-test results, channel health
settings, notification language setting, and update-batch summary
indexes. The upgrade checks verify schema versions and row
preservation for you.

## Documentation

- New "Regenerating documentation screenshots" section in
  `docs/maintainers.md` (English + Korean sets, marker scrubbing,
  PNG metadata stripping).
- Korean representative screenshots reviewed per release
  (`docs/screenshots/ko/`, embedded in `docs/configuration.md`).
- `scripts/prepublish-check.py` (public-repo hygiene scanner) and
  `scripts/hub-upgrade-check.sh` (Part A kit) ship with this release.

## Verification summary

- Go: all packages pass (`CGO_ENABLED=0 go test ./...`), 32-bit
  (386/arm7) cross-build + vet clean.
- rc.3 live-check: the rc.3 build was exercised on a fresh test port
  (`/login`, `/settings/network`, `/healthz` all 200, SPA served for
  deep links) and the `hosts_not_found` rejection was re-verified
  against the running hub.
- Web: 469 node tests pass; i18n catalog parity + hard-coded-string
  lint clean.
- UI audit: 320 combinations (5 viewports × 2 themes × en/ko × 16
  routes) — 0 violations.
- Upgrade rehearsals: v0.7.0 → v0.8 (26 checks) and v0.3.2 → v0.8
  (25 checks) — all pass, rollback verified.
- CI: all gates green locally, including `prepublish` and
  `hub-upgrade` (see the release checklist for the CI-only jobs).

## Checksums

Every release asset's sha256 is published in this release's
`checksums.txt`; verify with `sha256sum -c checksums.txt` after
download.

## Full changelog

https://github.com/leejeonghun001/cloud-pulse/compare/v0.7.0...v0.8.0
