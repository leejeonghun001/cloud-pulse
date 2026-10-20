# Alerting

Alerting is a configurable rule engine (`internal/alerting`): any
number of **rules** evaluate a **metric** against a **threshold**,
optionally sustained for a **duration**, and notify one or more
**channels** (Discord, Telegram, WhatsApp, or a generic webhook) with a
rendered chart image attached. See [notifications.md](notifications.md)
for channel setup and real-account verification.

## Rule model

An `AlertRule` has:

- **Metric**: `cpu` | `memory` | `disk` | `load1` | `egress_out_pct` |
  `egress_in_pct` | `host_down` | `storage_usage_pct` |
  `agent_outdated` | `update_batch_failed` (SPEC-v0.8 §3-1's rollout
  alerts) | `cost_mtd` | `cost_forecast` (cost budgets) — the last four
  have their own sections below.
- **Severity**: `warning` or `critical`. Omitted severity defaults to
  `critical` for `host_down` and `update_batch_failed` and `warning` for
  every other metric. Upgrading keeps what each rule was already delivered
  as, except the seeded `Outbound traffic 95% (critical)` and
  `Outbound traffic 100% (exceeded)` rules, which become `critical` to
  match their names while those names are unchanged. An event
  snapshots its rule severity when it fires, so later edits do not alter
  alert history.
- **Scope**: a specific host (or account, for `storage_usage_pct` — see
  [storage.md](storage.md#polling-and-alerting); or billing provider
  `aws`/`oci`, for the cost metrics), or `""` for "every
  host/account/provider." `update_batch_failed` has no scope at all — it always
  applies to every remote-update batch.
- **Operator/threshold**: `>` or `>=` against the metric's own unit
  (percent for cpu/memory/disk/egress/storage; a raw load-average
  number for `load1`; ignored for `host_down`/`agent_outdated`/
  `update_batch_failed`, which are duration- or event-based rather than
  threshold-based).
- **Duration** (`duration_sec`): the sustained window the condition
  must hold for. `0` fires immediately on the first breaching sample.
  For `host_down`, the effective offline threshold is
  `CP_OFFLINE_AFTER + duration_sec`. For `agent_outdated`, it's "behind
  the latest release for at least this long" (default 14 days when
  left at `0` — see below). Not used for `egress_*`/
  `storage_usage_pct`/`update_batch_failed`.
- **Cooldown** (`cooldown_sec`, default `3600`): the minimum time
  between re-notifications while a rule keeps firing on the same
  host/account. `0` means notify once on firing and never again until
  it resolves and re-fires.
- **Notify resolved**: whether a resolved transition also notifies.
- **Channels**: the list of notify-channel IDs this rule delivers to.

## Sustained-window semantics

A rule with `duration_sec > 0` (cpu/memory/disk/load1 only) only fires
once **every raw sample** in `[now - duration_sec, now]` satisfies the
condition, and that window is actually covered by history. A host that
started reporting less than `duration_sec` ago cannot fire a
duration-gated rule on partial data.

## State machine

Each `(rule, host)` pair (or `(rule, account)` for storage) is tracked
independently, moving through:

```text
ok -> pending -> firing -> resolved -> ok
```

- **ok -> pending**: the condition is newly satisfied but hasn't yet
  been sustained for `duration_sec`.
- **pending -> firing**: the condition has now held for the full
  sustained window (or `duration_sec == 0`) — an `AlertEvent` row is
  created and the initial notification is sent.
- **firing -> firing** ("still firing"): re-notifies only once
  `cooldown_sec` has elapsed since the last notification.
- **firing -> resolved**: the condition is no longer satisfied. A
  notification is sent only if `notify_resolved` is set.
- **pending -> ok**: the condition dropped before ever sustaining long
  enough to fire — no event, no notification, silent.

The engine runs after every agent report and on a 30-second scheduler
tick. `POST /api/v1/alerts/rules/{id}/preview` runs the same condition
check read-only, against every current host/account.

## Egress and storage rules: once-per-period dedupe

`egress_out_pct`/`egress_in_pct` rules notify **at most once per
rule+host+calendar-month combination**, regardless of `cooldown_sec`,
and never send a resolved notification even if `notify_resolved` is
set — only a new UTC calendar month resets eligibility.
`storage_usage_pct` rules use the general state machine above but
default to a once-per-day re-notification cadence via
`cooldown_sec`.

## Cost budget alerts

`cost_mtd` fires when a billing account's month-to-date cost reaches
the threshold; `cost_forecast` when its month-end forecast does. The
scope is `""` (every account), a provider (`aws`, `oci`: each of its
accounts) or one account (`oci:<tenancy OCID>`, `aws:<account ID>`);
each account fires and resolves on its own. The
threshold is an amount in that provider's own billing currency (USD for
AWS, the tenancy's currency for OCI) — figures are never converted
between currencies, so a rule scoped to "every provider" compares each
provider's own amount against the same number. `duration_sec` is
ignored and the default `cooldown_sec` is one day. Rules are checked
whenever new figures are stored (a hub poll or an agent's OCI report)
and on every 30-second alert tick, so a rule created between polls
fires within a tick. A provider that has never collected successfully
is skipped. Preview (`POST /api/v1/alerts/rules/{id}/preview`) returns
a provider-keyed map for these rules.

## Rollout alerts (SPEC-v0.8 §3-1)

Two rule kinds tied to the [Rollout dashboard](remote-updates.md#rollout-dashboard-spec-v08-3-1)
rather than a host's own metrics:

### `agent_outdated`

Host-scoped, using the same ok → pending → firing → resolved state
machine every other host-scoped metric uses (state.go): a host is
"satisfied" once it has been behind the fleet's latest known release
for at least `duration_sec` (default 14 days, applied when left at
`0` — mirrors `host_down`'s own default-application convention). Fires
once per host, respects `cooldown_sec` for "still outdated" reminders,
and resolves (with an optional notification, per `notify_resolved`)
once the host catches up. Preset: "Agent behind latest release for 14
days."

The "how long has this host been outdated" figure comes from the
Rollout dashboard's own in-memory tracking (see
[remote-updates.md](remote-updates.md#outdated-status-and-behind-for)),
not a stored history table — a hub restart resets the clock for a host
still outdated at that point, exactly like `agent_outdated`'s own
sustained-window check has no separate persistence of its own beyond
the regular `alert_state` row.

### `update_batch_failed`

Batch-scoped, not host- or account-scoped — `host_id`/`threshold`/
`duration_sec`/`cooldown_sec`/`notify_resolved` are all ignored. Fires
**exactly once** when a remote-update batch finishes (every job
terminal) with at least one failure; a batch that finishes with zero
failures, or is still in progress, never fires. There is no "resolved"
state for this metric — a finished batch's outcome is immutable, so
once notified it is never re-evaluated again (idempotency is tracked
via the same `alert_state` table every other metric uses, keyed by a
synthetic scope ID equal to the batch ID — this survives a hub restart
without a dedicated table). Preset: "Remote update batch has a
failure."

The notification message lists every failed host and its failure
reason (see [remote-updates.md](remote-updates.md#statuses-and-failure-reasons)
for the reason vocabulary); the attached image is a stacked
success/failure bar chart (`internal/alerting/chart.RenderBars`),
grouped by OS/failure-reason, rather than the time-series line chart
every other metric's chart image uses (there is no time series for a
batch outcome — see the next section).

### Evaluation timing

`agent_outdated` is evaluated on the same 30-second scheduler tick as
every other host-scoped metric. `update_batch_failed` runs on its own
1-minute scan independent of that tick (batch completion isn't tied to
any single host's report), scanning every batch's job list for one
that just finished with a failure it hasn't notified yet.

## Chart images

Every firing/resolved notification for a series-backed metric (cpu,
memory, disk, load1 — egress, host_down, storage_usage_pct, and
agent_outdated have no queryable time series and correctly ship with
no image) includes an 800x400 PNG line chart (`internal/alerting/chart`,
standard library `image/png` only, no font or charting dependency): a
dark background, the metric's line over `max(duration_sec * 3, 1h)` of
history, a dashed red threshold line, a shaded band over the breach
window, axis ticks, and a title like `HOSTNAME · CPU 93.4% > 90% for
5m`. `update_batch_failed`'s notification instead attaches a stacked
success/failure **bar** chart (`chart.RenderBars`, same PNG pipeline
and bitmap font, no time axis — see its own section above). Both
renderers are deterministic (covered by golden-hash tests hashing
decoded pixels, not encoded bytes, since PNG's zlib output isn't
stable across Go toolchains).

Chart text (axis labels, legends, titles) is always rendered in
English, regardless of the hub's `notify_language` setting (SPEC-v0.8
§3-3) — the renderer uses a fixed bitmap ASCII font with no Korean
glyph coverage. Only the notification message body around the
attached image is localized. See DECISIONS_LOG.md's chart-text entry.

## Default rules on upgrade

Three enabled `egress_out_pct` rules are seeded on every hub (new or
upgrading from pre-v0.5.0), replicating the old fixed 80/95/100%
outbound-egress behavior:

| Name | Threshold |
|---|---|
| Outbound traffic 80% (warning) | `>= 80` |
| Outbound traffic 95% (critical) | `>= 95` |
| Outbound traffic 100% (exceeded) | `>= 100` |

If a webhook URL was already configured at the time this migration
runs, a `"Default webhook"` notify channel is created and attached to
all three automatically.

## API

`GET/POST /api/v1/alerts/rules`, `PUT/DELETE
/api/v1/alerts/rules/{id}`, `POST /api/v1/alerts/rules/{id}/preview`
(all admin). `GET /api/v1/alerts/events?state=&host=&limit=&before=`
and `GET /api/v1/alerts/active` (any signed-in user). A validation
failure on any rule/channel `POST`/`PUT` responds `400` with
`APIError.details` — a `field -> message` map — so the Settings UI can
highlight the offending form field directly.
Rule `PUT` is a full replacement: omitting `severity` applies the same
metric-based default as `POST` rather than preserving the prior value.

## Channel-failure fallback notification

When a notification channel's weekly synthetic test fails, or 7-day
delivery success drops below the failing threshold (see
[notifications.md](notifications.md#health-report) for the full
health-state model), the hub sends a "channel X is failing" message
through a **different** channel — never back through the failing
channel itself. Selection (`internal/channelhealth.SelectFallbackChannel`)
is deterministic and excludes:

- the failing channel itself,
- disabled channels,
- any other channel currently also classified `failing`,

picking the lowest-ID remaining eligible channel. If no eligible
channel exists, no notification is sent — instead, `GET
/api/v1/alerts/channels/health`'s `banner_needed`/
`banner_channel_names` fields surface it as a dashboard banner in the
app shell instead.

The fallback fires **once per incident**: a channel that stays failing
across several scheduler ticks (or several consecutive delivery
failures) is only notified about once, tracked by a
`channel_failure_incidents` row that closes when the channel's health
state recovers to healthy/degraded. Recovery optionally sends a second
"channel X has recovered" message through the same fallback channel
that carried the original failure notice. Both message bodies are
built via `internal/i18n`'s `channel_health.channel_down`/
`channel_recovered` keys, honoring the hub's `notify_language` setting
exactly like every other notify_language-aware message (SPEC-v0.8
§3-3).
