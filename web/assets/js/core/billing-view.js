// billing-view.js — pure helpers for the Costs page / Settings →
// Billing's provider-status freshness display (SPEC-v0.6 §1 개선 a):
// "Not connected · last success 2026-09-29 07:00 (1d 3h ago)", the
// stale-badge decision, and money-formatting glue for the display-
// currency setting (SPEC-v0.6 §3). No DOM access here so this is fully
// covered by `node --test` without a browser; pages/costs.js and
// pages/settings/billing.js are the DOM consumers.
//
// Every time-dependent function takes `now` (unix seconds) as an
// explicit parameter rather than reading Date.now() internally, and
// absolute-time formatting takes an explicit UTC-offset-in-minutes
// parameter rather than relying on the runtime's local timezone — this
// keeps web/test/billing-view.test.mjs deterministic and
// timezone-independent regardless of which TZ the CI runner uses.

import { formatDisplayAmount } from "./currency.js";
import { t } from "./i18n.js";

/** SECONDS_PER_MINUTE / HOUR / DAY are used by elapsedLabel's compact
 * breakdown (mirrors core/format.js's formatDuration but capped to the
 * "Nd Nh"-style two-unit-max shown in SPEC's own example). */
const SECONDS_PER_MINUTE = 60;
const SECONDS_PER_HOUR = 3600;
const SECONDS_PER_DAY = 86400;

/**
 * elapsedLabel renders the elapsed time between sinceUnixSeconds and
 * nowUnixSeconds as a compact "Nd Nh"/"Nh Nm"/"Nm"/"less than a minute"
 * string, matching SPEC-v0.6 §1's example "(1d 3h ago)". Always
 * non-negative (a sinceUnixSeconds in the future clamps to "less than a
 * minute").
 * @param {number} sinceUnixSeconds
 * @param {number} nowUnixSeconds
 * @returns {string}
 */
export function elapsedLabel(sinceUnixSeconds, nowUnixSeconds) {
  const totalSeconds = Math.max(0, Math.floor(nowUnixSeconds - sinceUnixSeconds));
  if (totalSeconds < SECONDS_PER_MINUTE) return "less than a minute";

  const days = Math.floor(totalSeconds / SECONDS_PER_DAY);
  const hours = Math.floor((totalSeconds % SECONDS_PER_DAY) / SECONDS_PER_HOUR);
  const minutes = Math.floor((totalSeconds % SECONDS_PER_HOUR) / SECONDS_PER_MINUTE);

  if (days > 0) return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
  if (hours > 0) return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`;
  return `${minutes}m`;
}

/**
 * formatAbsoluteLocal renders a unix-seconds timestamp as
 * "YYYY-MM-DD HH:mm" using an explicitly injected UTC offset in
 * minutes (positive east of UTC, matching JS's own
 * `-Date.prototype.getTimezoneOffset()` sign convention), rather than
 * the runtime's ambient local timezone — this is what makes
 * web/test/billing-view.test.mjs deterministic regardless of the CI
 * runner's TZ.
 * @param {number} unixSeconds
 * @param {number} utcOffsetMinutes e.g. 540 for UTC+9 (KST), -300 for UTC-5 (EST)
 * @returns {string}
 */
export function formatAbsoluteLocal(unixSeconds, utcOffsetMinutes) {
  const shiftedMs = (unixSeconds + utcOffsetMinutes * 60) * 1000;
  const d = new Date(shiftedMs);
  const pad = (n) => String(n).padStart(2, "0");
  const year = d.getUTCFullYear();
  const month = pad(d.getUTCMonth() + 1);
  const day = pad(d.getUTCDate());
  const hours = pad(d.getUTCHours());
  const minutes = pad(d.getUTCMinutes());
  return `${year}-${month}-${day} ${hours}:${minutes}`;
}

/**
 * isStale reports whether a provider snapshot last successful at
 * lastSuccessAt (unix seconds, 0/absent = never) should be flagged
 * stale at time now given a poll interval in seconds, mirroring
 * models.CloudCostStale exactly: stale once the elapsed time exceeds
 * twice the interval. A never-successful snapshot is not "stale" (see
 * freshnessViewModel's "no successful fetch yet" case instead).
 * @param {number} lastSuccessAt unix seconds, 0 = never
 * @param {number} nowUnixSeconds
 * @param {number} intervalSeconds
 * @returns {boolean}
 */
export function isStale(lastSuccessAt, nowUnixSeconds, intervalSeconds) {
  if (!lastSuccessAt || lastSuccessAt <= 0) return false;
  if (!intervalSeconds || intervalSeconds <= 0) return false;
  return nowUnixSeconds - lastSuccessAt > 2 * intervalSeconds;
}

/**
 * freshnessViewModel builds the full view model for a provider's
 * freshness line (SPEC-v0.6 §1 개선 a), covering all three cases:
 *   - never successful: {hasSuccess: false, text: "No successful fetch yet."}
 *   - ok now: {hasSuccess: true, stale: false, text: "Collected 2026-09-29 07:00 (1d 3h ago)."}
 *   - not ok now, prior success: {hasSuccess: true, stale: bool,
 *     text: "Not connected · last success 2026-09-29 07:00 (1d 3h ago)."}
 * @param {Object} params
 * @param {string} params.status a models.CloudBillingStatus value
 * @param {number} params.lastSuccessAt unix seconds, 0 = never
 * @param {number} params.nowUnixSeconds
 * @param {number} params.intervalSeconds currently effective billing poll interval
 * @param {number} params.utcOffsetMinutes see formatAbsoluteLocal
 * @returns {{hasSuccess: boolean, stale: boolean, absolute: string, elapsed: string, text: string}}
 */
export function freshnessViewModel({ status, lastSuccessAt, nowUnixSeconds, intervalSeconds, utcOffsetMinutes }) {
  if (!lastSuccessAt || lastSuccessAt <= 0) {
    return { hasSuccess: false, stale: false, absolute: "", elapsed: "", text: t("costs.no_successful_fetch_yet") };
  }

  const absolute = formatAbsoluteLocal(lastSuccessAt, utcOffsetMinutes);
  const elapsed = elapsedLabel(lastSuccessAt, nowUnixSeconds);
  const stale = isStale(lastSuccessAt, nowUnixSeconds, intervalSeconds);
  const prefix = status === "ok" ? t("costs.freshness_collected") : t("costs.freshness_not_connected");
  const staleSuffix = stale ? ` ${t("costs.freshness_stale_suffix")}` : "";
  const text = t("costs.freshness_line", { prefix, absolute, elapsed, staleSuffix });

  return { hasSuccess: true, stale, absolute, elapsed, text };
}

/**
 * lastKnownCostViewModel builds the "last known" MTD/forecast display
 * for a provider card that isn't currently `ok` but has previously
 * collected values — SPEC-v0.6 §1 개선 a requires these keep showing
 * (muted, labelled "last known") rather than disappearing the moment a
 * poll fails. Returns null when there is nothing to show (no prior
 * success at all).
 * @param {{status: string, last_success_at?: number, mtd_cost?: number, forecast_cost?: number, currency?: string}} snapshot
 * @returns {{mtdLabel: string, forecastLabel: string}|null}
 */
export function lastKnownCostViewModel(snapshot) {
  if (!snapshot || snapshot.status === "ok") return null;
  if (!snapshot.last_success_at || snapshot.last_success_at <= 0) return null;
  const currency = snapshot.currency || "USD";
  return {
    mtdLabel: t("costs.last_known_mtd", { amount: formatMoney(snapshot.mtd_cost, currency) }),
    forecastLabel: t("costs.last_known_forecast", { amount: formatMoney(snapshot.forecast_cost, currency) }),
  };
}

/** formatMoney renders a raw CLI-currency amount (never converted —
 * SPEC-v0.6 §3: a non-USD CLI currency is shown as-is) with two
 * decimals and a currency code suffix, no "(excl. tax)" repetition
 * (callers put that note once per section per SPEC-v0.6 §2's ask to
 * avoid repeating it on every number). */
function formatMoney(amount, currency) {
  const n = Number(amount);
  const value = Number.isFinite(n) ? n : 0;
  return `${value.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })} ${currency}`;
}

/**
 * hostCostDisplayAmounts converts a HostCost's four money fields
 * (cloud_mtd, cloud_forecast via network_estimate, total_mtd,
 * total_forecast) into display-currency-formatted strings in one call,
 * so page code doesn't repeat the same four formatDisplayAmount calls
 * inline. Values without a "$"/"₩" prefix note are the network
 * estimate's own plan-less "No plan" fallback, left to the caller.
 * @param {{cloud_mtd: number, cloud_forecast: number, network_estimate?: {plan_id?: number|string, mtd?: number, projected?: number}, total_mtd: number, total_forecast: number}} hostCost
 * @param {{currency?: string, krw_per_usd?: number, rate_updated_at?: number}|null|undefined} displayCurrency
 * @returns {{cloudMTD: string, cloudForecast: string, networkMTD: string|null, networkForecast: string|null, totalMTD: string, totalForecast: string}}
 */
export function hostCostDisplayAmounts(hostCost, displayCurrency) {
  const net = hostCost.network_estimate || {};
  const hasPlan = Boolean(net.plan_id);
  const opts = { taxNote: false };
  return {
    cloudMTD: formatDisplayAmount(hostCost.cloud_mtd, displayCurrency, opts),
    cloudForecast: formatDisplayAmount(hostCost.cloud_forecast, displayCurrency, opts),
    networkMTD: hasPlan ? formatDisplayAmount(net.mtd, displayCurrency, opts) : null,
    networkForecast: hasPlan ? formatDisplayAmount(net.projected, displayCurrency, opts) : null,
    totalMTD: formatDisplayAmount(hostCost.total_mtd, displayCurrency, opts),
    totalForecast: formatDisplayAmount(hostCost.total_forecast, displayCurrency, opts),
  };
}

/**
 * providerStatusHint returns the i18n key (and params) of a "what to do
 * next" hint for a provider snapshot that isn't "ok", or null. The hint
 * depends on where collection runs: the hub's own CLI, or an agent's
 * instance-principal query (snapshot.source_host).
 * @param {{provider: string, status: string, source_host?: string}} snap
 * @returns {{key: string, params: Object}|null}
 */
export function providerStatusHint(snap) {
  if (!snap || snap.status === "ok") return null;
  const host = snap.source_host || "";
  if (snap.provider === "oci" && host) {
    const status = ["not_installed", "not_configured", "auth_failed", "permission_denied"].includes(snap.status) ? snap.status : "error";
    return { key: `costs.hint_oci_agent_${status}`, params: { host } };
  }
  if (snap.provider === "aws" && ["not_installed", "not_configured", "auth_failed", "permission_denied"].includes(snap.status)) {
    return { key: `costs.hint_aws_${snap.status}`, params: {} };
  }
  if (snap.provider === "oci" && snap.status !== "error") {
    return { key: "costs.hint_oci_hub", params: {} };
  }
  return { key: "costs.hint_retry", params: {} };
}

/**
 * spendSummary condenses provider snapshots into the Overview's "this
 * month" view. Figures are summed per currency only — providers report
 * in their own billing currency (USD, SGD, ...) and are never converted
 * into one another. A provider counts once it has ever collected
 * successfully; one that is set up but currently failing (an agent
 * reporting, credentials present, or past figures) is listed under
 * `attention`, while one that was simply never set up is not.
 * @param {Array<Object>} snapshots models.CloudCostSnapshot JSON objects
 * @returns {{totals: Array<{currency: string, mtd: number, forecast: number, providers: string[]}>, attention: string[], stale: boolean}}
 */
export function spendSummary(snapshots) {
  /** @type {Map<string, {currency: string, mtd: number, forecast: number, providers: string[]}>} */
  const byCurrency = new Map();
  const attention = [];
  let stale = false;
  for (const snap of snapshots || []) {
    const hasFigures = (snap.last_success_at || 0) > 0;
    if (hasFigures) {
      const currency = snap.currency || "USD";
      const row = byCurrency.get(currency) || { currency, mtd: 0, forecast: 0, providers: [] };
      row.mtd += Number(snap.mtd_cost) || 0;
      row.forecast += Number(snap.forecast_cost) || 0;
      row.providers.push(snap.provider);
      byCurrency.set(currency, row);
      stale ||= Boolean(snap.stale);
    }
    const setUp = hasFigures || Boolean(snap.source_host) || ["auth_failed", "permission_denied", "error"].includes(snap.status);
    if (snap.status !== "ok" && setUp) attention.push(snap.provider);
  }
  const totals = [...byCurrency.values()].sort((a, b) => a.currency.localeCompare(b.currency));
  return { totals, attention, stale };
}
