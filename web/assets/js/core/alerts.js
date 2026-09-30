// alerts.js — pure helpers for SPEC-v0.5 §D alerting UI: metric/operator
// display metadata, rule presets, a human-readable rule summary
// sentence, and rule-form validation. No DOM access here so this module
// is fully covered by node --test without a browser.

/**
 * METRICS lists every models.AlertMetric value with display metadata:
 * label, unit suffix appended after the threshold ("%" or "" for
 * load1), and whether the metric supports a per-host scope selector
 * (host_down and every metric here do — HostID == "" always means "all
 * hosts", this flag is purely about whether showing a per-host badge in
 * lists makes sense; kept simple: currently all metrics support scope).
 * @type {{value: string, label: string, unit: string}[]}
 */
export const METRICS = [
  { value: "cpu", label: "CPU usage", unit: "%" },
  { value: "memory", label: "Memory usage", unit: "%" },
  { value: "disk", label: "Disk usage", unit: "%" },
  { value: "load1", label: "Load average (1m)", unit: "" },
  { value: "egress_out_pct", label: "Outbound traffic", unit: "% of limit" },
  { value: "egress_in_pct", label: "Inbound traffic", unit: "% of limit" },
  { value: "host_down", label: "Host offline", unit: "" },
  { value: "storage_usage_pct", label: "Storage account usage", unit: "% of quota" },
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

/** OPERATORS lists every models.AlertOperator value with a display label. */
export const OPERATORS = [
  { value: ">", label: "above" },
  { value: ">=", label: "at or above" },
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
  if (metric === "host_down") return "";
  const unit = metricByValue(metric)?.unit ?? "";
  const num = Number.isFinite(threshold) ? threshold : 0;
  const numText = metric === "load1" ? num.toFixed(2).replace(/\.?0+$/, "") || "0" : String(num);
  if (unit === "% of limit") return `${numText}% of limit`;
  if (unit === "% of quota") return `${numText}% of quota`;
  return `${numText}${unit}`;
}

/**
 * formatDurationShort renders a duration in seconds as a compact human
 * string tuned for rule summaries/labels, e.g. 300 -> "5 minutes", 60
 * -> "1 minute", 30 -> "30 seconds", 0 -> "0 seconds". Unlike
 * core/format.js's formatDuration this always spells out a single unit
 * with its full word (never abbreviated to "5m"), matching the
 * human-sentence style SPEC-v0.5 §D asks for.
 * @param {number} seconds
 * @returns {string}
 */
export function formatDurationShort(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0));
  if (s % 3600 === 0 && s >= 3600) {
    const h = s / 3600;
    return `${h} hour${h === 1 ? "" : "s"}`;
  }
  if (s % 60 === 0 && s >= 60) {
    const m = s / 60;
    return `${m} minute${m === 1 ? "" : "s"}`;
  }
  return `${s} second${s === 1 ? "" : "s"}`;
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
  if (!hostID) return "any host";
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
  if (isStorageScopedMetric(metric)) {
    if (!scopeID) return "any connected storage account";
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
  return names.length > 0 ? names.join(", ") : "no channels";
}

/**
 * ruleSummarySentence builds the SPEC-v0.5 §D human sentence describing
 * a rule, e.g. "Alert when CPU usage on any host stays above 90% for 5
 * minutes → Discord #ops, Telegram", or for host_down: "Alert when a
 * host goes offline for 2 minutes → Discord #ops". Pure function of the
 * rule's fields plus two name-lookup callbacks so it doesn't need to
 * fetch anything itself.
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
    const subject = rule.host_id ? scope : "a host";
    return `Alert when ${subject} goes offline for ${duration} → ${channels}`;
  }

  const label = lowerFirstWord(metricLabel(rule.metric));
  const op = operatorLabel(rule.operator);
  const threshold = formatThreshold(rule.metric, rule.threshold);
  const durationClause = rule.duration_sec > 0 ? ` for ${duration}` : "";
  const verb = rule.duration_sec > 0 ? "stays" : "is";
  return `Alert when ${label} on ${scope} ${verb} ${op} ${threshold}${durationClause} → ${channels}`;
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

/**
 * PRESETS lists the SPEC-v0.5 §D rule presets shown in the rule editor
 * as one-click starting points. Each preset's `rule` is a partial
 * models.AlertRule (missing name/id/timestamps get filled by the
 * caller); `name` doubles as the preset's own label and the rule's
 * default Name.
 * @type {{id: string, label: string, rule: Object}[]}
 */
export const PRESETS = [
  {
    id: "cpu-90-5m",
    label: "CPU above 90% for 5 minutes",
    rule: { name: "CPU above 90% for 5 minutes", metric: "cpu", host_id: "", operator: ">", threshold: 90, duration_sec: 300 },
  },
  {
    id: "memory-90-10m",
    label: "Memory above 90% for 10 minutes",
    rule: { name: "Memory above 90% for 10 minutes", metric: "memory", host_id: "", operator: ">", threshold: 90, duration_sec: 600 },
  },
  {
    id: "disk-90",
    label: "Disk above 90%",
    rule: { name: "Disk above 90%", metric: "disk", host_id: "", operator: ">", threshold: 90, duration_sec: 0 },
  },
  {
    id: "egress-out-80",
    label: "Outbound traffic above 80% of limit",
    rule: { name: "Outbound traffic above 80% of limit", metric: "egress_out_pct", host_id: "", operator: ">", threshold: 80, duration_sec: 0 },
  },
  {
    id: "egress-in-80",
    label: "Inbound traffic above 80% of limit",
    rule: { name: "Inbound traffic above 80% of limit", metric: "egress_in_pct", host_id: "", operator: ">", threshold: 80, duration_sec: 0 },
  },
  {
    id: "host-down-2m",
    label: "Host offline for 2 minutes",
    rule: { name: "Host offline for 2 minutes", metric: "host_down", host_id: "", operator: ">", threshold: 0, duration_sec: 120 },
  },
  {
    id: "storage-usage-90",
    label: "Storage account above 90% of quota",
    rule: { name: "Storage account above 90% of quota", metric: "storage_usage_pct", host_id: "", operator: ">", threshold: 90, duration_sec: 0 },
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
    errors.name = "Name is required.";
  }
  if (!METRICS.some((m) => m.value === form.metric)) {
    errors.metric = "Choose a metric.";
  }
  if (!OPERATORS.some((o) => o.value === form.operator)) {
    errors.operator = "Choose an operator.";
  }
  if (form.metric !== "host_down" && (!Number.isFinite(form.threshold) || form.threshold < 0)) {
    errors.threshold = "Threshold must be a non-negative number.";
  }
  if (!Number.isFinite(form.durationSec) || form.durationSec < 0) {
    errors.durationSec = "Duration must be a non-negative number of seconds.";
  }
  if (!Number.isFinite(form.cooldownSec) || form.cooldownSec < 0) {
    errors.cooldownSec = "Cooldown must be a non-negative number of seconds.";
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
