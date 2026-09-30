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
  `egress_in_pct` | `host_down` | `storage_usage_pct`.
- **Scope**: a specific host (or account, for `storage_usage_pct` — see
  [storage.md](storage.md#polling-and-alerting)), or `""` for "every
  host/account."
- **Operator/threshold**: `>` or `>=` against the metric's own unit
  (percent for cpu/memory/disk/egress/storage; a raw load-average
  number for `load1`; ignored for `host_down`).
- **Duration** (`duration_sec`): the sustained window the condition
  must hold for. `0` fires immediately on the first breaching sample.
  For `host_down`, the effective offline threshold is
  `CP_OFFLINE_AFTER + duration_sec`. Not used for
  `egress_*`/`storage_usage_pct`.
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

## Chart images

Every firing/resolved notification for a series-backed metric (cpu,
memory, disk, load1 — egress, host_down, and storage_usage_pct have no
queryable time series and correctly ship with no image) includes an
800x400 PNG chart (`internal/alerting/chart`, standard library
`image/png` only, no font or charting dependency): a dark background,
the metric's line over `max(duration_sec * 3, 1h)` of history, a
dashed red threshold line, a shaded band over the breach window, axis
ticks, and a title like `HOSTNAME · CPU 93.4% > 90% for 5m`. Rendering
is deterministic (covered by a golden-hash test).

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
