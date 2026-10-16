// alerts.js — pure helpers for SPEC-v0.5 §D alerting UI: metric/operator
// display metadata, rule presets, a human-readable rule summary
// sentence, and rule-form validation. No DOM access here so this module
// is fully covered by node --test without a browser.
import { t } from "./i18n.js";

/**
 * METRICS lists every models.AlertMetric value with display metadata:
 * label, unit suffix appended after the threshold ("%" or "" for
 * load1), and whether the metric supports a per-host scope selector
 * (host_down and every metric here do — HostID == "" always means "all
 * hosts", this flag is purely about whether showing a per-host badge in
 * lists makes sense; kept simple: currently all metrics support scope).
 *
 * `label` is a getter so it re-resolves against the current language
 * on every read, since this module's exports are static top-level data
 * evaluated once at import time.
 * @type {{value: string, label: string, unit: string}[]}
 */
export const METRICS = [
  { value: "cpu", get label() { return t("alerts.metric_cpu"); }, unit: "%" },
  { value: "memory", get label() { return t("alerts.metric_memory"); }, unit: "%" },
  { value: "disk", get label() { return t("alerts.metric_disk"); }, unit: "%" },
  { value: "load1", get label() { return t("alerts.metric_load1"); }, unit: "" },
  { value: "egress_out_pct", get label() { return t("alerts.metric_egress_out_pct"); }, unit: "% of limit" },
  { value: "egress_in_pct", get label() { return t("alerts.metric_egress_in_pct"); }, unit: "% of limit" },
  { value: "host_down", get label() { return t("alerts.metric_host_down"); }, unit: "" },
  { value: "storage_usage_pct", get label() { return t("alerts.metric_storage_usage_pct"); }, unit: "% of quota" },
  { value: "agent_outdated", get label() { return t("alerts.metric_agent_outdated"); }, unit: "" },
  { value: "update_batch_failed", get label() { return t("alerts.metric_update_batch_failed"); }, unit: "" },
  { value: "cost_mtd", get label() { return t("alerts.metric_cost_mtd"); }, unit: "currency" },
  { value: "cost_forecast", get label() { return t("alerts.metric_cost_forecast"); }, unit: "currency" },
];

/**
 * isCostScopedMetric reports whether metric is a provider-cost budget
 * metric, whose AlertRule.HostID holds a billing provider ("aws"/"oci",
 * "" for every provider) and whose threshold is an amount in that
 * provider's billing currency. Its duration is ignored by the hub.
 * @param {string} metric
 * @returns {boolean}
 */
export function isCostScopedMetric(metric) {
  return metric === "cost_mtd" || metric === "cost_forecast";
}

/** COST_PROVIDERS are the billing providers a cost rule can scope to. */
export const COST_PROVIDERS = [
  { value: "aws", name: "AWS" },
  { value: "oci", name: "OCI" },
];

/**
 * isStorageScopedMetric reports whether metric is
 * "storage_usage_pct" — the one metric whose AlertRule.HostID field is
 * repurposed to hold a storage account ID instead of a host ID (see
 * models.AlertMetricStorageUsagePct's doc comment). Callers use this to
 * decide whether a rule's scope selector should list storage accounts
 * instead of hosts.
 * @param {string} metric
 * @returns {boolean}
 */
export function isStorageScopedMetric(metric) {
  return metric === "storage_usage_pct";
}

/**
 * isThresholdlessMetric reports whether metric's condition is purely
 * duration/event-based rather than a threshold crossing — host_down
 * (duration-only) and SPEC-v0.8 §3-1's two rollout metrics:
 * agent_outdated (duration-only, like host_down) and
 * update_batch_failed (event-only, no duration either). Callers use
 * this to decide whether the rule editor's threshold field/payload
 * value applies at all.
 * @param {string} metric
 * @returns {boolean}
 */
export function isThresholdlessMetric(metric) {
  return metric === "host_down" || metric === "agent_outdated" || metric === "update_batch_failed";
}

/**
 * defaultRuleSeverity returns the severity selected for a new rule when the
 * editor has not been explicitly changed. Host-down and failed update batch
 * alerts have always been notified as critical; every other metric starts as
 * a warning.
 * @param {string} metric
 * @returns {"warning"|"critical"}
 */
export function defaultRuleSeverity(metric) {
  return metric === "host_down" || metric === "update_batch_failed" ? "critical" : "warning";
}

/**
 * eventSeverity is the one compatibility boundary for alert-event severity.
 * Older hubs did not include the snapshot field, so preserve their historical
 * host-down behavior while treating every other missing/unknown value as a
 * warning.
 * @param {Object} event
 * @returns {"warning"|"critical"}
 */
export function eventSeverity(event) {
  if (event?.severity === "critical") return "critical";
  if (event?.severity === "warning") return "warning";
  return defaultRuleSeverity(event?.metric);
}

/**
 * setDraftMetric keeps an untouched editor severity coupled to its metric.
 * Once the severity selector is changed, later metric changes retain that
 * deliberate choice.
 * @param {{metric: string, severity: "warning"|"critical", severityTouched: boolean}} draft
 * @param {string} metric
 */
export function setDraftMetric(draft, metric) {
  draft.metric = metric;
  if (!draft.severityTouched) draft.severity = defaultRuleSeverity(metric);
}

/** OPERATORS lists every models.AlertOperator value with a display
 * label; a getter for the same reason as METRICS' label above. */
export const OPERATORS = [
  { value: ">", get label() { return t("alerts.operator_above"); } },
  { value: ">=", get label() { return t("alerts.operator_at_or_above"); } },
];

/**
 * metricByValue looks up a METRICS entry by its value, returning
 * undefined for an unknown metric.
 * @param {string} metric
 * @returns {{value: string, label: string, unit: string}|undefined}
 */
export function metricByValue(metric) {
  return METRICS.find((m) => m.value === metric);
}

/**
 * metricLabel returns the display label for a metric value, falling
 * back to the raw value for an unrecognized one.
 * @param {string} metric
 * @returns {string}
 */
export function metricLabel(metric) {
  return metricByValue(metric)?.label ?? metric;
}

/**
 * operatorLabel returns the display label ("above"/"at or above") for
 * an operator value, falling back to the raw value.
 * @param {string} operator
 * @returns {string}
 */
export function operatorLabel(operator) {
  return OPERATORS.find((o) => o.value === operator)?.label ?? operator;
}

/**
 * formatThreshold renders a rule's threshold with its metric's unit
 * suffix, e.g. formatThreshold("cpu", 90) -> "90%",
 * formatThreshold("load1", 4) -> "4", formatThreshold("egress_out_pct", 80)
 * -> "80% of limit". host_down ignores the threshold entirely (its
 * condition is purely duration-based), returning "".
 * @param {string} metric
 * @param {number} threshold
 * @returns {string}
 */
export function formatThreshold(metric, threshold) {
  if (isThresholdlessMetric(metric)) return "";
  const unit = metricByValue(metric)?.unit ?? "";
  const num = Number.isFinite(threshold) ? threshold : 0;
  const numText = metric === "load1" ? num.toFixed(2).replace(/\.?0+$/, "") || "0" : String(num);
  if (unit === "% of limit") return t("alerts.threshold_pct_of_limit", { value: numText });
  if (unit === "% of quota") return t("alerts.threshold_pct_of_quota", { value: numText });
  if (unit === "%") return t("alerts.threshold_pct", { value: numText });
  if (unit === "currency") return t("alerts.threshold_amount", { value: numText });
  return numText;
}

/**
 * formatMetricValue renders an observed alert value with only the unit the
 * metric definition guarantees. Threshold prose ("of limit" / "of quota")
 * belongs to the rule threshold, not the reading itself. Unknown metrics
 * deliberately retain their bare numeric value rather than receiving a
 * guessed unit.
 * @param {string} metric
 * @param {number} value
 * @returns {string}
 */
export function formatMetricValue(metric, value) {
  const num = Number.isFinite(value) ? value.toFixed(1) : "–";
  if (num === "–") return num;
  const unit = metricByValue(metric)?.unit;
  if (unit === "currency") return value.toFixed(2);
  return unit?.startsWith("%") ? `${num}%` : num;
}

/**
 * formatDurationShort renders a duration in seconds as a compact human
 * string tuned for rule summaries/labels, e.g. 300 -> "5 minutes", 60
 * -> "1 minute", 30 -> "30 seconds", 0 -> "0 seconds", 86400 -> "1 day"
 * (days added for SPEC-v0.8 §3-1's agent_outdated preset, whose 14-day
 * default would otherwise render as an unwieldy "336 hours"). Unlike
 * core/format.js's formatDuration this always spells out a single unit
 * with its full word (never abbreviated to "5m"), matching the
 * human-sentence style SPEC-v0.5 §D asks for. Korean has no
 * grammatical plural distinction, so ko.json's "one"/"other" pair for
 * each unit is identical text — see core/i18n.js's plural doc comment.
 * @param {number} seconds
 * @returns {string}
 */
export function formatDurationShort(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0));
  if (s % 86400 === 0 && s >= 86400) {
    return t("alerts.duration_days", { count: s / 86400 });
  }
  if (s % 3600 === 0 && s >= 3600) {
    return t("alerts.duration_hours", { count: s / 3600 });
  }
  if (s % 60 === 0 && s >= 60) {
    return t("alerts.duration_minutes", { count: s / 60 });
  }
  return t("alerts.duration_seconds", { count: s });
}

/**
 * hostScopeLabel renders a rule's host scope for display: "any host"
 * for an empty HostID, or hostnameByID(hostID) (falling back to the raw
 * id) otherwise.
 * @param {string} hostID
 * @param {(id: string) => string|undefined} [hostnameByID]
 * @returns {string}
 */
export function hostScopeLabel(hostID, hostnameByID) {
  if (!hostID) return t("alerts.scope_any_host");
  return hostnameByID?.(hostID) || hostID;
}

/**
 * scopeLabel renders a rule's scope for display, dispatching between
 * host and storage-account naming depending on the rule's metric: "any
 * host"/hostname for every metric except storage_usage_pct, "any
 * connected storage account"/account name for storage_usage_pct (see
 * isStorageScopedMetric).
 * @param {string} metric
 * @param {string} scopeID rule.host_id (a host ID, or a storage
 *   account ID formatted as a decimal string, depending on metric)
 * @param {(id: string) => string|undefined} [hostnameByID]
 * @param {(id: string) => string|undefined} [storageAccountNameByID]
 * @returns {string}
 */
export function scopeLabel(metric, scopeID, hostnameByID, storageAccountNameByID) {
  if (isCostScopedMetric(metric)) {
    if (!scopeID) return t("alerts.scope_any_provider");
    return COST_PROVIDERS.find((p) => p.value === scopeID)?.name || scopeID;
  }
  if (isStorageScopedMetric(metric)) {
    if (!scopeID) return t("alerts.scope_any_storage_account");
    return storageAccountNameByID?.(scopeID) || scopeID;
  }
  return hostScopeLabel(scopeID, hostnameByID);
}

/**
 * channelsLabel renders a rule's channel list for display, e.g.
 * "Discord #ops, Telegram". Returns "no channels" when channelIDs is
 * empty, or when none resolve via channelNameByID.
 * @param {number[]} channelIDs
 * @param {(id: number) => string|undefined} channelNameByID
 * @returns {string}
 */
export function channelsLabel(channelIDs, channelNameByID) {
  const names = (channelIDs || []).map((id) => channelNameByID(id)).filter(Boolean);
  return names.length > 0 ? names.join(", ") : t("alerts.no_channels");
}

/**
 * ruleSummarySentence builds the SPEC-v0.5 §D human sentence describing
 * a rule, e.g. "Alert when CPU usage on any host stays above 90% for 5
 * minutes → Discord #ops, Telegram", or for host_down: "Alert when a
 * host goes offline for 2 minutes → Discord #ops". Pure function of the
 * rule's fields plus two name-lookup callbacks so it doesn't need to
 * fetch anything itself.
 *
 * The whole sentence (not fragments glued with string concatenation)
 * is a single t() template per case, since Korean word order for "on X
 * stays above Y for Z" differs substantially from English — see
 * alerts.rule_sentence_* in the catalogs for the exact per-language
 * phrasing.
 * @param {Object} rule a models.AlertRule-shaped object (metric,
 *   host_id, operator, threshold, duration_sec, channel_ids)
 * @param {Object} [lookups]
 * @param {(id: string) => string|undefined} [lookups.hostnameByID]
 * @param {(id: number) => string|undefined} [lookups.channelNameByID]
 * @returns {string}
 */
export function ruleSummarySentence(rule, { hostnameByID, channelNameByID = () => undefined, storageAccountNameByID } = {}) {
  const scope = scopeLabel(rule.metric, rule.host_id, hostnameByID, storageAccountNameByID);
  const channels = channelsLabel(rule.channel_ids, channelNameByID);
  const duration = formatDurationShort(rule.duration_sec);

  if (rule.metric === "host_down") {
    const subject = rule.host_id ? scope : t("alerts.a_host");
    return t("alerts.rule_sentence_host_down", { subject, duration, channels });
  }

  if (rule.metric === "agent_outdated") {
    const subject = rule.host_id ? scope : t("alerts.a_host_agent");
    const behindDuration = formatDurationShort(rule.duration_sec || AGENT_OUTDATED_DEFAULT_DURATION_SEC);
    return t("alerts.rule_sentence_agent_outdated", { subject, duration: behindDuration, channels });
  }

  if (rule.metric === "update_batch_failed") {
    return t("alerts.rule_sentence_update_batch_failed", { channels });
  }

  const label = lowerFirstWord(metricLabel(rule.metric));
  const op = operatorLabel(rule.operator);
  const threshold = formatThreshold(rule.metric, rule.threshold);
  if (rule.duration_sec > 0 && !isCostScopedMetric(rule.metric)) {
    return t("alerts.rule_sentence_sustained", { label, scope, op, threshold, duration, channels });
  }
  return t("alerts.rule_sentence_immediate", { label, scope, op, threshold, channels });
}

/**
 * lowerFirstWord lowercases a label's first character for mid-sentence
 * use, except when that character starts an all-caps acronym (e.g.
 * "CPU usage" stays "CPU usage" rather than becoming "cPU usage") —
 * detected as 2+ leading uppercase letters.
 * @param {string} s
 * @returns {string}
 */
function lowerFirstWord(s) {
  if (s.length === 0) return s;
  if (/^[A-Z]{2,}/.test(s)) return s;
  return s[0].toLowerCase() + s.slice(1);
}

/** AGENT_OUTDATED_DEFAULT_DURATION_SEC mirrors the hub's default (see
 * internal/alerting/rollout.go's AgentOutdatedDurationDefaultSec):
 * SPEC-v0.8 §3-1's "at least N days behind" default of 14 days,
 * applied when an agent_outdated rule's duration is left at 0. Defined
 * ahead of PRESETS below since it's referenced there and JS const
 * bindings are not hoisted the way function declarations are. */
export const AGENT_OUTDATED_DEFAULT_DURATION_SEC = 14 * 24 * 60 * 60;

/**
 * PRESETS lists the SPEC-v0.5 §D rule presets shown in the rule editor
 * as one-click starting points. Each preset's `rule` is a partial
 * models.AlertRule (missing name/id/timestamps get filled by the
 * caller); `name` doubles as the preset's own label and the rule's
 * default Name — both localized, since a preset's rule name is stored
 * as-is on save and there's no reason to force it to stay English on a
 * Korean-language hub.
 *
 * `label` and `rule.name` are getters for the same reason as METRICS'
 * label above. NOTE: two more presets (update-failure-related) are
 * appended to this array by the rollout stage (SPEC-v0.8 §3-1) — this
 * stage does not own those two entries.
 * @type {{id: string, label: string, rule: Object}[]}
 */
export const PRESETS = [
  {
    id: "cpu-90-5m",
    get label() { return t("alerts.preset_cpu_90_5m"); },
    get rule() { return { name: t("alerts.preset_cpu_90_5m"), metric: "cpu", host_id: "", operator: ">", threshold: 90, duration_sec: 300 }; },
  },
  {
    id: "memory-90-10m",
    get label() { return t("alerts.preset_memory_90_10m"); },
    get rule() { return { name: t("alerts.preset_memory_90_10m"), metric: "memory", host_id: "", operator: ">", threshold: 90, duration_sec: 600 }; },
  },
  {
    id: "disk-90",
    get label() { return t("alerts.preset_disk_90"); },
    get rule() { return { name: t("alerts.preset_disk_90"), metric: "disk", host_id: "", operator: ">", threshold: 90, duration_sec: 0 }; },
  },
  {
    id: "egress-out-80",
    get label() { return t("alerts.preset_egress_out_80"); },
    get rule() { return { name: t("alerts.preset_egress_out_80"), metric: "egress_out_pct", host_id: "", operator: ">", threshold: 80, duration_sec: 0 }; },
  },
  {
    id: "egress-in-80",
    get label() { return t("alerts.preset_egress_in_80"); },
    get rule() { return { name: t("alerts.preset_egress_in_80"), metric: "egress_in_pct", host_id: "", operator: ">", threshold: 80, duration_sec: 0 }; },
  },
  {
    id: "host-down-2m",
    get label() { return t("alerts.preset_host_down_2m"); },
    get rule() { return { name: t("alerts.preset_host_down_2m"), metric: "host_down", host_id: "", operator: ">", threshold: 0, duration_sec: 120 }; },
  },
  {
    id: "storage-usage-90",
    get label() { return t("alerts.preset_storage_usage_90"); },
    get rule() { return { name: t("alerts.preset_storage_usage_90"), metric: "storage_usage_pct", host_id: "", operator: ">", threshold: 90, duration_sec: 0 }; },
  },
  {
    id: "agent-outdated-14d",
    get label() { return t("alerts.preset_agent_outdated_14d"); },
    get rule() { return { name: t("alerts.preset_agent_outdated_14d"), metric: "agent_outdated", host_id: "", operator: ">", threshold: 0, duration_sec: AGENT_OUTDATED_DEFAULT_DURATION_SEC }; },
  },
  {
    id: "cost-forecast-budget",
    get label() { return t("alerts.preset_cost_forecast_budget"); },
    get rule() { return { name: t("alerts.preset_cost_forecast_budget"), metric: "cost_forecast", host_id: "", operator: ">=", threshold: 10, duration_sec: 0, cooldown_sec: COST_DEFAULT_COOLDOWN_SEC }; },
  },
  {
    id: "update-batch-failed",
    get label() { return t("alerts.preset_update_batch_failed"); },
    get rule() { return { name: t("alerts.preset_update_batch_failed"), metric: "update_batch_failed", host_id: "", operator: ">", threshold: 0, duration_sec: 0 }; },
  },
];

/**
 * presetByID looks up a PRESETS entry by its id.
 * @param {string} id
 * @returns {{id: string, label: string, rule: Object}|undefined}
 */
export function presetByID(id) {
  return PRESETS.find((p) => p.id === id);
}

/** DEFAULT_COOLDOWN_SEC mirrors the hub's default (see
 * alertroutes.go's alertingDefaultCooldownSec), applied by the rule
 * editor's form default when creating a new rule. */
export const DEFAULT_COOLDOWN_SEC = 3600;

/** COST_DEFAULT_COOLDOWN_SEC mirrors the hub's once-a-day re-notify
 * default for cost rules: a budget breach doesn't need hourly pings. */
export const COST_DEFAULT_COOLDOWN_SEC = 86400;

/**
 * validateRuleForm validates a rule-editor form's field values,
 * returning a {field: message} map of errors (empty object = valid).
 * Mirrors the hub's own validateRuleRequest (internal/hub/alertroutes.go)
 * so the UI can show the same errors before submitting.
 * @param {Object} form
 * @param {string} form.name
 * @param {string} form.metric
 * @param {string} form.operator
 * @param {number} form.threshold
 * @param {number} form.durationSec
 * @param {number} form.cooldownSec
 * @param {string} [form.hostID]
 * @returns {Object<string,string>}
 */
export function validateRuleForm(form) {
  const errors = {};
  if (!form.name || form.name.trim() === "") {
    errors.name = t("alerts.error_name_required");
  }
  if (!METRICS.some((m) => m.value === form.metric)) {
    errors.metric = t("alerts.error_choose_metric");
  }
  if (!OPERATORS.some((o) => o.value === form.operator)) {
    errors.operator = t("alerts.error_choose_operator");
  }
  if (!isThresholdlessMetric(form.metric) && (!Number.isFinite(form.threshold) || form.threshold < 0)) {
    errors.threshold = t("alerts.error_threshold_non_negative");
  }
  if (!Number.isFinite(form.durationSec) || form.durationSec < 0) {
    errors.durationSec = t("alerts.error_duration_non_negative");
  }
  if (!Number.isFinite(form.cooldownSec) || form.cooldownSec < 0) {
    errors.cooldownSec = t("alerts.error_cooldown_non_negative");
  }
  return errors;
}

/**
 * durationSecFromParts converts a {value, unit} duration input (unit
 * one of "seconds"|"minutes"|"hours") into whole seconds, clamped to
 * >= 0. Used by the rule editor's duration/cooldown number+unit input
 * pairs.
 * @param {number} value
 * @param {"seconds"|"minutes"|"hours"} unit
 * @returns {number}
 */
export function durationSecFromParts(value, unit) {
  const n = Math.max(0, Number(value) || 0);
  switch (unit) {
    case "hours":
      return Math.round(n * 3600);
    case "minutes":
      return Math.round(n * 60);
    default:
      return Math.round(n);
  }
}

/**
 * bestDurationParts picks the largest whole unit ("hours"|"minutes"|
 * "seconds") that evenly divides seconds, for populating a duration
 * editor's {value, unit} inputs from a stored duration_sec. 0 renders
 * as {value: 0, unit: "minutes"} (minutes is the most common case).
 * @param {number} seconds
 * @returns {{value: number, unit: "seconds"|"minutes"|"hours"}}
 */
export function bestDurationParts(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0));
  if (s === 0) return { value: 0, unit: "minutes" };
  if (s % 3600 === 0) return { value: s / 3600, unit: "hours" };
  if (s % 60 === 0) return { value: s / 60, unit: "minutes" };
  return { value: s, unit: "seconds" };
}
