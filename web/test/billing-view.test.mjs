// billing-view.test.mjs — unit tests for assets/js/core/billing-view.js
// (SPEC-v0.6 §1 개선 a freshness/stale logic + §3 display-amount glue).
// Every time-dependent case uses fixed injected unix-second clocks and
// an explicit UTC-offset-in-minutes parameter instead of the runtime's
// ambient timezone, so this suite is deterministic regardless of the
// CI runner's TZ.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  elapsedLabel,
  formatAbsoluteLocal,
  isStale,
  freshnessViewModel,
  lastKnownCostViewModel,
  hostCostDisplayAmounts,
} from "../assets/js/core/billing-view.js";

// 2026-09-29T07:00:00Z (a fixed reference instant used throughout),
// computed via Date.UTC rather than a hand-typed literal to avoid a
// transcription error.
const T_2026_09_29_07_00_UTC = Math.floor(Date.UTC(2026, 8, 29, 7, 0, 0) / 1000);

test("elapsedLabel: less than a minute", () => {
  assert.equal(elapsedLabel(1000, 1030), "less than a minute");
  assert.equal(elapsedLabel(1000, 1000), "less than a minute");
});

test("elapsedLabel: minutes only", () => {
  assert.equal(elapsedLabel(0, 5 * 60), "5m");
});

test("elapsedLabel: hours and minutes", () => {
  assert.equal(elapsedLabel(0, 2 * 3600 + 15 * 60), "2h 15m");
});

test("elapsedLabel: exact hours omits minutes", () => {
  assert.equal(elapsedLabel(0, 3 * 3600), "3h");
});

test("elapsedLabel: days and hours (SPEC example: 1d 3h)", () => {
  const since = T_2026_09_29_07_00_UTC;
  const now = since + 1 * 86400 + 3 * 3600;
  assert.equal(elapsedLabel(since, now), "1d 3h");
});

test("elapsedLabel: exact days omits hours", () => {
  assert.equal(elapsedLabel(0, 2 * 86400), "2d");
});

test("elapsedLabel: future timestamp clamps to zero elapsed", () => {
  assert.equal(elapsedLabel(1000, 500), "less than a minute");
});

test("formatAbsoluteLocal: UTC+9 (KST) offset", () => {
  // 2026-09-29T07:00:00Z + 9h = 2026-09-29 16:00 local.
  assert.equal(formatAbsoluteLocal(T_2026_09_29_07_00_UTC, 540), "2026-09-29 16:00");
});

test("formatAbsoluteLocal: UTC+0 offset", () => {
  assert.equal(formatAbsoluteLocal(T_2026_09_29_07_00_UTC, 0), "2026-09-29 07:00");
});

test("formatAbsoluteLocal: negative offset crossing midnight backward", () => {
  // 2026-09-29T07:00:00Z - 8h = 2026-09-28 23:00 local (UTC-8).
  assert.equal(formatAbsoluteLocal(T_2026_09_29_07_00_UTC, -8 * 60), "2026-09-28 23:00");
});

test("formatAbsoluteLocal: positive offset crossing midnight forward", () => {
  // 2026-09-29T23:00:00Z + 9h = 2026-09-30 08:00 local (UTC+9).
  const t = T_2026_09_29_07_00_UTC + 16 * 3600;
  assert.equal(formatAbsoluteLocal(t, 540), "2026-09-30 08:00");
});

test("formatAbsoluteLocal: pads single-digit month/day/hour/minute", () => {
  // 2026-01-02T03:04:00Z, UTC+0.
  const t = Math.floor(Date.UTC(2026, 0, 2, 3, 4, 0) / 1000);
  assert.equal(formatAbsoluteLocal(t, 0), "2026-01-02 03:04");
});

test("isStale: never-successful is not stale", () => {
  assert.equal(isStale(0, 100000, 3600), false);
});

test("isStale: exactly 2x interval is not yet stale (strict >)", () => {
  const interval = 3600;
  const lastSuccessAt = 1000;
  const now = lastSuccessAt + 2 * interval;
  assert.equal(isStale(lastSuccessAt, now, interval), false);
});

test("isStale: just over 2x interval is stale", () => {
  const interval = 3600;
  const lastSuccessAt = 1000;
  const now = lastSuccessAt + 2 * interval + 1;
  assert.equal(isStale(lastSuccessAt, now, interval), true);
});

test("isStale: well within interval is not stale", () => {
  assert.equal(isStale(1000, 1000 + 60, 3600), false);
});

test("isStale: zero/negative interval never flags stale", () => {
  assert.equal(isStale(1000, 100000, 0), false);
});

test("freshnessViewModel: never successful", () => {
  const vm = freshnessViewModel({
    status: "not_configured",
    lastSuccessAt: 0,
    nowUnixSeconds: 100000,
    intervalSeconds: 3600,
    utcOffsetMinutes: 0,
  });
  assert.equal(vm.hasSuccess, false);
  assert.equal(vm.stale, false);
  assert.equal(vm.text, "No successful fetch yet.");
});

test("freshnessViewModel: ok status shows 'Collected' prefix, not stale", () => {
  const lastSuccessAt = T_2026_09_29_07_00_UTC;
  const now = lastSuccessAt + 3600; // 1h later, well within a 24h interval.
  const vm = freshnessViewModel({
    status: "ok",
    lastSuccessAt,
    nowUnixSeconds: now,
    intervalSeconds: 24 * 3600,
    utcOffsetMinutes: 0,
  });
  assert.equal(vm.hasSuccess, true);
  assert.equal(vm.stale, false);
  assert.equal(vm.absolute, "2026-09-29 07:00");
  assert.equal(vm.elapsed, "1h");
  assert.equal(vm.text, "Collected 2026-09-29 07:00 (1h ago).");
});

test("freshnessViewModel: SPEC-v0.6 exact example text (not-ok, prior success, 1d3h, not stale)", () => {
  const lastSuccessAt = T_2026_09_29_07_00_UTC;
  const now = lastSuccessAt + 1 * 86400 + 3 * 3600; // 1d 3h later.
  const vm = freshnessViewModel({
    status: "auth_failed",
    lastSuccessAt,
    nowUnixSeconds: now,
    intervalSeconds: 24 * 3600, // 2x = 48h; 27h elapsed < 48h, not stale.
    utcOffsetMinutes: 0,
  });
  assert.equal(vm.hasSuccess, true);
  assert.equal(vm.stale, false);
  assert.equal(vm.text, "Not connected · last success 2026-09-29 07:00 (1d 3h ago).");
});

test("freshnessViewModel: stale badge appended once elapsed exceeds 2x interval", () => {
  const lastSuccessAt = 1000;
  const intervalSeconds = 3600;
  const now = lastSuccessAt + 2 * intervalSeconds + 60; // just over 2x.
  const vm = freshnessViewModel({
    status: "error",
    lastSuccessAt,
    nowUnixSeconds: now,
    intervalSeconds,
    utcOffsetMinutes: 0,
  });
  assert.equal(vm.stale, true);
  assert.match(vm.text, /\(stale\)\.$/);
});

test("lastKnownCostViewModel: null when status is ok (no 'last known' needed)", () => {
  assert.equal(lastKnownCostViewModel({ status: "ok", last_success_at: 1000, mtd_cost: 5, forecast_cost: 10 }), null);
});

test("lastKnownCostViewModel: null when never successful", () => {
  assert.equal(lastKnownCostViewModel({ status: "auth_failed", last_success_at: 0 }), null);
});

test("lastKnownCostViewModel: null for a missing/undefined snapshot", () => {
  assert.equal(lastKnownCostViewModel(null), null);
  assert.equal(lastKnownCostViewModel(undefined), null);
});

test("lastKnownCostViewModel: renders muted 'last known' MTD/forecast labels", () => {
  const vm = lastKnownCostViewModel({
    status: "auth_failed",
    last_success_at: 1000,
    mtd_cost: 12.3,
    forecast_cost: 45.6,
    currency: "USD",
  });
  assert.equal(vm.mtdLabel, "Last known MTD: 12.30 USD");
  assert.equal(vm.forecastLabel, "Last known forecast: 45.60 USD");
});

test("lastKnownCostViewModel: non-USD CLI currency shown as-is, unconverted", () => {
  const vm = lastKnownCostViewModel({ status: "error", last_success_at: 1, mtd_cost: 1000, forecast_cost: 2000, currency: "EUR" });
  assert.match(vm.mtdLabel, /EUR$/);
  assert.match(vm.forecastLabel, /EUR$/);
});

test("hostCostDisplayAmounts: USD display, no network plan", () => {
  const amounts = hostCostDisplayAmounts(
    { cloud_mtd: 10, cloud_forecast: 20, network_estimate: {}, total_mtd: 10, total_forecast: 20 },
    { currency: "USD" },
  );
  assert.equal(amounts.networkMTD, null);
  assert.equal(amounts.networkForecast, null);
  assert.match(amounts.cloudMTD, /^\$10\.00/);
  assert.match(amounts.totalForecast, /^\$20\.00/);
});

test("hostCostDisplayAmounts: with a network plan, converts every field to KRW", () => {
  const amounts = hostCostDisplayAmounts(
    {
      cloud_mtd: 100,
      cloud_forecast: 200,
      network_estimate: { plan_id: 3, mtd: 5, projected: 15 },
      total_mtd: 105,
      total_forecast: 215,
    },
    { currency: "KRW", krw_per_usd: 1385.5, rate_updated_at: 1780128000 },
  );
  assert.match(amounts.cloudMTD, /₩/);
  assert.match(amounts.networkMTD, /₩/);
  assert.match(amounts.totalForecast, /₩/);
});

test("hostCostDisplayAmounts: plan_id of 0/falsy still counts as 'no plan'", () => {
  const amounts = hostCostDisplayAmounts(
    { cloud_mtd: 1, cloud_forecast: 1, network_estimate: { plan_id: 0, mtd: 0, projected: 0 }, total_mtd: 1, total_forecast: 1 },
    { currency: "USD" },
  );
  assert.equal(amounts.networkMTD, null);
});
