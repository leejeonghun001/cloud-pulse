// core/rollout.js — pure helpers for the Fleet → Rollout page
// (SPEC-v0.8 §3-1): summary-card figures, version-distribution bar
// data, outdated-host list rows, and batch-history display fields. No
// DOM access here so this module is fully covered by node --test
// without a browser (mirrors core/alerts.js's own "pure helpers,
// separate module" convention).
import { t } from "./i18n.js";

/** CAPABILITY_LABEL_KEYS maps a models.RemoteUpdateCapabilityClass
 * value to its i18n key. */
const CAPABILITY_LABEL_KEYS = {
  capable: "rollout.capability_capable",
  not_opted_in: "rollout.capability_not_opted_in",
  unsupported: "rollout.capability_unsupported",
};

/**
 * capabilityLabel renders a models.RemoteUpdateCapabilityClass value
 * for display, falling back to the raw value for an unrecognized one.
 * @param {string} capability
 * @returns {string}
 */
export function capabilityLabel(capability) {
  const key = CAPABILITY_LABEL_KEYS[capability];
  return key ? t(key) : capability;
}

/** PLATFORM_LABEL_KEYS maps an OS family value to its i18n key. */
const PLATFORM_LABEL_KEYS = {
  linux: "rollout.os_linux",
  darwin: "rollout.os_darwin",
  windows: "rollout.os_windows",
  freebsd: "rollout.os_freebsd",
};

/**
 * osLabel renders an OS family value ("linux"|"darwin"|"windows"|
 * "freebsd"|"unknown") for display.
 * @param {string} osName
 * @returns {string}
 */
export function osLabel(osName) {
  const key = PLATFORM_LABEL_KEYS[osName];
  return key ? t(key) : t("rollout.os_unknown");
}

/**
 * summaryCardFigures builds the four summary card values shown at the
 * top of the Rollout page from a GET /api/v1/fleet/versions response.
 * @param {Object} summary models.FleetVersionSummary
 * @returns {{
 *   upToDatePercent: number,
 *   upToDateCount: number,
 *   outdatedCount: number,
 *   unknownCount: number,
 *   capableCount: number,
 *   notOptedInCount: number,
 *   unsupportedCount: number,
 *   totalHosts: number,
 * }}
 */
export function summaryCardFigures(summary) {
  const s = summary || {};
  const total = s.total_hosts || 0;
  const upToDate = s.up_to_date_count || 0;
  return {
    upToDatePercent: total > 0 ? Math.round((upToDate / total) * 100) : 0,
    upToDateCount: upToDate,
    outdatedCount: s.outdated_count || 0,
    unknownCount: s.unknown_count || 0,
    capableCount: s.capable_count || 0,
    notOptedInCount: s.not_opted_in_count || 0,
    unsupportedCount: s.unsupported_count || 0,
    totalHosts: total,
  };
}

/**
 * versionDistributionByOS groups a FleetVersionSummary's flat
 * distribution buckets (each {os, version, count}) into one entry per
 * OS, each holding its own version segments in the order they appear
 * (already sorted server-side by descending semver) — the shape the
 * stacked-bar renderer needs: one bar (an OS) with N segments
 * (versions).
 * @param {Array<{os: string, version: string, count: number}>} distribution
 * @returns {Array<{os: string, total: number, segments: Array<{version: string, count: number}>}>}
 */
export function versionDistributionByOS(distribution) {
  const byOS = new Map();
  for (const bucket of distribution || []) {
    if (!byOS.has(bucket.os)) byOS.set(bucket.os, []);
    byOS.get(bucket.os).push({ version: bucket.version, count: bucket.count });
  }
  const out = [];
  for (const [os, segments] of byOS) {
    out.push({ os, total: segments.reduce((n, s) => n + s.count, 0), segments });
  }
  return out;
}

/**
 * segmentPercentages converts a bar's segments into {version, count,
 * percent} triples (percent of the bar's own total, rounded, 0 when
 * total is 0) — the exact numbers the accessible CSS stacked-bar
 * widths and its text-alternative table need.
 * @param {{total: number, segments: Array<{version: string, count: number}>}} bar
 * @returns {Array<{version: string, count: number, percent: number}>}
 */
export function segmentPercentages(bar) {
  const total = bar?.total || 0;
  return (bar?.segments || []).map((s) => ({
    version: s.version,
    count: s.count,
    percent: total > 0 ? Math.round((s.count / total) * 100) : 0,
  }));
}

/**
 * outdatedHostRows filters and sorts a FleetVersionSummary's Hosts
 * list down to just the outdated ones, most-behind first (largest
 * behind-for duration), for the outdated-host table.
 * @param {Array<Object>} hosts models.FleetVersionHost[]
 * @param {number} nowUnixSeconds
 * @returns {Array<Object & {behindForSeconds: number}>}
 */
export function outdatedHostRows(hosts, nowUnixSeconds) {
  const rows = (hosts || [])
    .filter((h) => h.outdated)
    .map((h) => ({
      ...h,
      behindForSeconds: h.outdated_since ? Math.max(0, nowUnixSeconds - h.outdated_since) : 0,
    }));
  rows.sort((a, b) => b.behindForSeconds - a.behindForSeconds);
  return rows;
}

/**
 * behindForText renders a behind-for duration (seconds) as a short
 * human string, e.g. 90000 -> "1 day", 3600 -> "1 hour", 0 -> "less
 * than an hour" (0/unknown "since" never claims a specific duration it
 * can't back up).
 * @param {number} seconds
 * @returns {string}
 */
export function behindForText(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0));
  const days = Math.floor(s / 86400);
  if (days >= 1) return t("rollout.behind_for_days", { count: days });
  const hours = Math.floor(s / 3600);
  if (hours >= 1) return t("rollout.behind_for_hours", { count: hours });
  return t("rollout.behind_for_less_than_hour");
}

/**
 * remotelyUpdatableOutdatedHostIDs returns the host IDs from an
 * outdated-host list that are capable of a remote update batch right
 * now (capability === "capable") — the exact target set for the "Update
 * all remotely-updatable outdated hosts" button.
 * @param {Array<{host_id: string, capability: string}>} hosts
 * @returns {string[]}
 */
export function remotelyUpdatableOutdatedHostIDs(hosts) {
  return (hosts || []).filter((h) => h.capability === "capable").map((h) => h.host_id);
}

/**
 * batchStatusLabel classifies a models.UpdateBatchSummary for display:
 * "failed" if it finished with >=1 failure, "succeeded" if finished
 * with 0 failures, "in_progress" while pending > 0.
 * @param {{pending?: number, failed?: number}} batch
 * @returns {"in_progress"|"succeeded"|"failed"}
 */
export function batchStatusLabel(batch) {
  if ((batch?.pending || 0) > 0) return "in_progress";
  if ((batch?.failed || 0) > 0) return "failed";
  return "succeeded";
}

/** BATCH_STATUS_TEXT_KEYS maps batchStatusLabel's return value to its
 * i18n key. */
const BATCH_STATUS_TEXT_KEYS = {
  in_progress: "rollout.batch_status_in_progress",
  succeeded: "rollout.batch_status_succeeded",
  failed: "rollout.batch_status_failed",
};

/**
 * batchStatusText renders batchStatusLabel's classification for
 * display.
 * @param {{pending?: number, failed?: number}} batch
 * @returns {string}
 */
export function batchStatusText(batch) {
  return t(BATCH_STATUS_TEXT_KEYS[batchStatusLabel(batch)]);
}

/**
 * batchDurationText renders a batch's DurationSec as a short human
 * string, or a placeholder while still in progress (duration_sec is 0
 * and pending > 0).
 * @param {{duration_sec?: number, pending?: number}} batch
 * @returns {string}
 */
export function batchDurationText(batch) {
  if ((batch?.pending || 0) > 0) return t("rollout.batch_duration_in_progress");
  const seconds = batch?.duration_sec || 0;
  if (seconds <= 0) return t("rollout.batch_duration_instant");
  if (seconds < 60) return t("rollout.batch_duration_seconds", { count: seconds });
  const minutes = Math.round(seconds / 60);
  return t("rollout.batch_duration_minutes", { count: minutes });
}

/**
 * failureReasonSummary renders a batch's failure_reasons distribution
 * as a short comma-joined string, e.g. "timeout x2, download_failed",
 * or "" when there are none.
 * @param {Array<{reason: string, count: number}>} failureReasons
 * @returns {string}
 */
export function failureReasonSummary(failureReasons) {
  if (!Array.isArray(failureReasons) || failureReasons.length === 0) return "";
  return failureReasons.map((r) => (r.count > 1 ? `${r.reason} x${r.count}` : r.reason)).join(", ");
}

/**
 * parallelismOptions lists the "Update all" confirm dialog's
 * parallelism choices, mirroring pages/updates.js's existing
 * agentTable parallel-select values (1/3/5/10, default 3) so both
 * update-triggering surfaces offer the same choices.
 * @returns {Array<{value: number, isDefault: boolean}>}
 */
export function parallelismOptions() {
  return [
    { value: 1, isDefault: false },
    { value: 3, isDefault: true },
    { value: 5, isDefault: false },
    { value: 10, isDefault: false },
  ];
}
