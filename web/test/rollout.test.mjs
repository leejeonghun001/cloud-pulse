// rollout.test.mjs — unit tests for assets/js/core/rollout.js's pure
// helpers (SPEC-v0.8 §3-1). Installs a minimal sample catalog via
// setCatalogs so t()-backed helpers resolve real strings rather than
// bare keys, mirroring i18n.test.mjs's own setup pattern.
import { test } from "node:test";
import assert from "node:assert/strict";
import { setCatalogs } from "../assets/js/core/i18n.js";
import {
  capabilityLabel,
  osLabel,
  summaryCardFigures,
  versionDistributionByOS,
  segmentPercentages,
  outdatedHostRows,
  behindForText,
  remotelyUpdatableOutdatedHostIDs,
  batchStatusLabel,
  batchStatusText,
  batchDurationText,
  failureReasonSummary,
  parallelismOptions,
} from "../assets/js/core/rollout.js";

const SAMPLE = {
  rollout: {
    capability_capable: "Capable",
    capability_not_opted_in: "Not opted in",
    capability_unsupported: "Unsupported",
    os_linux: "Linux",
    os_darwin: "macOS",
    os_windows: "Windows",
    os_freebsd: "FreeBSD",
    os_unknown: "Unknown",
    behind_for_days: { one: "{count} day", other: "{count} days" },
    behind_for_hours: { one: "{count} hour", other: "{count} hours" },
    behind_for_less_than_hour: "less than an hour",
    batch_status_in_progress: "In progress",
    batch_status_succeeded: "Succeeded",
    batch_status_failed: "Failed",
    batch_duration_in_progress: "In progress",
    batch_duration_instant: "Instant",
    batch_duration_minutes: { one: "{count} minute", other: "{count} minutes" },
    batch_duration_seconds: { one: "{count} second", other: "{count} seconds" },
  },
};
setCatalogs({ en: SAMPLE, ko: SAMPLE });

test("capabilityLabel: known values map to labels", () => {
  assert.equal(capabilityLabel("capable"), "Capable");
  assert.equal(capabilityLabel("not_opted_in"), "Not opted in");
  assert.equal(capabilityLabel("unsupported"), "Unsupported");
});

test("capabilityLabel: unrecognized value falls back to raw", () => {
  assert.equal(capabilityLabel("bogus"), "bogus");
});

test("osLabel: known OS families", () => {
  assert.equal(osLabel("linux"), "Linux");
  assert.equal(osLabel("darwin"), "macOS");
  assert.equal(osLabel("windows"), "Windows");
  assert.equal(osLabel("freebsd"), "FreeBSD");
});

test("osLabel: unrecognized/missing falls back to Unknown", () => {
  assert.equal(osLabel("unknown"), "Unknown");
  assert.equal(osLabel(""), "Unknown");
  assert.equal(osLabel(undefined), "Unknown");
});

test("summaryCardFigures: computes percent and passes through counts", () => {
  const got = summaryCardFigures({
    total_hosts: 10,
    up_to_date_count: 6,
    outdated_count: 3,
    unknown_count: 1,
    capable_count: 5,
    not_opted_in_count: 2,
    unsupported_count: 3,
  });
  assert.deepEqual(got, {
    upToDatePercent: 60,
    upToDateCount: 6,
    outdatedCount: 3,
    unknownCount: 1,
    capableCount: 5,
    notOptedInCount: 2,
    unsupportedCount: 3,
    totalHosts: 10,
  });
});

test("summaryCardFigures: zero hosts never divides by zero", () => {
  const got = summaryCardFigures({ total_hosts: 0 });
  assert.equal(got.upToDatePercent, 0);
});

test("summaryCardFigures: missing/null summary defaults everything to 0", () => {
  const got = summaryCardFigures(null);
  assert.equal(got.totalHosts, 0);
  assert.equal(got.upToDatePercent, 0);
});

test("versionDistributionByOS: groups buckets by OS preserving segment order", () => {
  const distribution = [
    { os: "linux", version: "v0.7.0", count: 3 },
    { os: "linux", version: "v0.6.0", count: 2 },
    { os: "darwin", version: "v0.7.0", count: 1 },
  ];
  const got = versionDistributionByOS(distribution);
  assert.equal(got.length, 2);
  const linux = got.find((b) => b.os === "linux");
  assert.equal(linux.total, 5);
  assert.deepEqual(linux.segments, [
    { version: "v0.7.0", count: 3 },
    { version: "v0.6.0", count: 2 },
  ]);
});

test("versionDistributionByOS: empty/missing input returns []", () => {
  assert.deepEqual(versionDistributionByOS([]), []);
  assert.deepEqual(versionDistributionByOS(undefined), []);
});

test("segmentPercentages: computes rounded percent of the bar's own total", () => {
  const bar = { total: 4, segments: [{ version: "v0.7.0", count: 3 }, { version: "v0.6.0", count: 1 }] };
  const got = segmentPercentages(bar);
  assert.deepEqual(got, [
    { version: "v0.7.0", count: 3, percent: 75 },
    { version: "v0.6.0", count: 1, percent: 25 },
  ]);
});

test("segmentPercentages: zero total yields 0 percents, no division by zero", () => {
  const got = segmentPercentages({ total: 0, segments: [{ version: "v0.7.0", count: 0 }] });
  assert.deepEqual(got, [{ version: "v0.7.0", count: 0, percent: 0 }]);
});

test("outdatedHostRows: filters to outdated only, sorted most-behind first", () => {
  const hosts = [
    { host_id: "h1", outdated: false },
    { host_id: "h2", outdated: true, outdated_since: 1000 },
    { host_id: "h3", outdated: true, outdated_since: 500 },
  ];
  const now = 2000;
  const got = outdatedHostRows(hosts, now);
  assert.equal(got.length, 2);
  assert.equal(got[0].host_id, "h3"); // behind 1500s > h2's 1000s
  assert.equal(got[0].behindForSeconds, 1500);
  assert.equal(got[1].host_id, "h2");
  assert.equal(got[1].behindForSeconds, 1000);
});

test("outdatedHostRows: outdated with no outdated_since gets behindForSeconds 0", () => {
  const got = outdatedHostRows([{ host_id: "h1", outdated: true, outdated_since: 0 }], 5000);
  assert.equal(got[0].behindForSeconds, 0);
});

test("behindForText: days when >= 1 day behind", () => {
  assert.equal(behindForText(2 * 86400 + 100), "2 days");
  assert.equal(behindForText(86400), "1 day");
});

test("behindForText: hours when < 1 day but >= 1 hour", () => {
  assert.equal(behindForText(3 * 3600), "3 hours");
  assert.equal(behindForText(3600), "1 hour");
});

test("behindForText: less than an hour for small values", () => {
  assert.equal(behindForText(59), "less than an hour");
  assert.equal(behindForText(0), "less than an hour");
});

test("remotelyUpdatableOutdatedHostIDs: filters to capable only", () => {
  const hosts = [
    { host_id: "h1", capability: "capable" },
    { host_id: "h2", capability: "not_opted_in" },
    { host_id: "h3", capability: "capable" },
    { host_id: "h4", capability: "unsupported" },
  ];
  assert.deepEqual(remotelyUpdatableOutdatedHostIDs(hosts), ["h1", "h3"]);
});

test("remotelyUpdatableOutdatedHostIDs: empty/missing input returns []", () => {
  assert.deepEqual(remotelyUpdatableOutdatedHostIDs([]), []);
  assert.deepEqual(remotelyUpdatableOutdatedHostIDs(undefined), []);
});

test("batchStatusLabel: in_progress while pending > 0 regardless of failed", () => {
  assert.equal(batchStatusLabel({ pending: 1, failed: 2 }), "in_progress");
});

test("batchStatusLabel: failed when finished with >=1 failure", () => {
  assert.equal(batchStatusLabel({ pending: 0, failed: 1 }), "failed");
});

test("batchStatusLabel: succeeded when finished with 0 failures", () => {
  assert.equal(batchStatusLabel({ pending: 0, failed: 0 }), "succeeded");
});

test("batchStatusText: renders the localized label", () => {
  assert.equal(batchStatusText({ pending: 0, failed: 0 }), "Succeeded");
  assert.equal(batchStatusText({ pending: 0, failed: 2 }), "Failed");
  assert.equal(batchStatusText({ pending: 3, failed: 0 }), "In progress");
});

test("batchDurationText: in-progress placeholder while pending", () => {
  assert.equal(batchDurationText({ pending: 1, duration_sec: 0 }), "In progress");
});

test("batchDurationText: instant for a zero-duration finished batch", () => {
  assert.equal(batchDurationText({ pending: 0, duration_sec: 0 }), "Instant");
});

test("batchDurationText: minutes for >= 60s", () => {
  assert.equal(batchDurationText({ pending: 0, duration_sec: 125 }), "2 minutes");
  assert.equal(batchDurationText({ pending: 0, duration_sec: 60 }), "1 minute");
});

test("batchDurationText: seconds for < 60s", () => {
  assert.equal(batchDurationText({ pending: 0, duration_sec: 30 }), "30 seconds");
});

test("failureReasonSummary: joins reasons with counts > 1 annotated", () => {
  const got = failureReasonSummary([
    { reason: "timeout", count: 2 },
    { reason: "download_failed", count: 1 },
  ]);
  assert.equal(got, "timeout x2, download_failed");
});

test("failureReasonSummary: empty/missing input returns empty string", () => {
  assert.equal(failureReasonSummary([]), "");
  assert.equal(failureReasonSummary(undefined), "");
});

test("parallelismOptions: includes 1/3/5/10 with 3 as default", () => {
  const opts = parallelismOptions();
  assert.deepEqual(opts.map((o) => o.value), [1, 3, 5, 10]);
  assert.equal(opts.find((o) => o.value === 3).isDefault, true);
  assert.equal(opts.filter((o) => o.isDefault).length, 1);
});
