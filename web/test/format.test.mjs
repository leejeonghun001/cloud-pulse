// format.test.mjs — unit tests for assets/js/format.js using Node's
// built-in test runner (node --test), no external dependencies.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  clamp,
  formatBytes,
  formatBitrate,
  formatPercent,
  formatDuration,
  formatUptime,
  formatRelativeTime,
  formatRelativeTimeFromUnixSeconds,
  formatNumber,
  formatLoad,
} from "../assets/js/core/format.js";

test("clamp restricts to range", () => {
  assert.equal(clamp(5, 0, 10), 5);
  assert.equal(clamp(-5, 0, 10), 0);
  assert.equal(clamp(15, 0, 10), 10);
  assert.equal(clamp(NaN, 2, 10), 2);
});

test("formatBytes renders IEC units", () => {
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatBytes(512), "512 B");
  assert.equal(formatBytes(1024), "1.0 KiB");
  assert.equal(formatBytes(1536), "1.5 KiB");
  assert.equal(formatBytes(1048576), "1.0 MiB");
  assert.equal(formatBytes(1610612736), "1.5 GiB");
  assert.equal(formatBytes(1099511627776), "1.0 TiB");
  assert.equal(formatBytes(-5), "0 B");
  assert.equal(formatBytes(NaN), "0 B");
});

test("formatBytes respects decimals", () => {
  assert.equal(formatBytes(1610612736, 2), "1.50 GiB");
  assert.equal(formatBytes(1610612736, 0), "2 GiB");
});

test("formatBitrate appends /s", () => {
  assert.equal(formatBitrate(1048576), "1.0 MiB/s");
  assert.equal(formatBitrate(0), "0 B/s");
});

test("formatPercent clamps to [0,100] by default", () => {
  assert.equal(formatPercent(50), "50.0%");
  assert.equal(formatPercent(150), "100.0%");
  assert.equal(formatPercent(-10), "0.0%");
  assert.equal(formatPercent(NaN), "0.0%");
});

test("formatPercent allowOver100 skips upper clamp", () => {
  assert.equal(formatPercent(150, 1, true), "150.0%");
  assert.equal(formatPercent(-10, 1, true), "0.0%");
});

test("formatPercent respects decimals", () => {
  assert.equal(formatPercent(33.333, 2), "33.33%");
});

test("formatDuration renders compact units", () => {
  assert.equal(formatDuration(0), "0s");
  assert.equal(formatDuration(-5), "0s");
  assert.equal(formatDuration(45), "45s");
  assert.equal(formatDuration(90), "1m 30s");
  assert.equal(formatDuration(3661), "1h 1m");
  assert.equal(formatDuration(90061), "1d 1h");
  assert.equal(formatDuration(86400), "1d");
});

test("formatUptime is an alias of formatDuration", () => {
  assert.equal(formatUptime(3661), formatDuration(3661));
});

test("formatRelativeTime renders past differences", () => {
  const now = 1_700_000_000_000;
  assert.equal(formatRelativeTime(now - 500, now), "just now");
  assert.equal(formatRelativeTime(now - 5000, now), "5s ago");
  assert.equal(formatRelativeTime(now - 3 * 60 * 1000, now), "3m ago");
  assert.equal(formatRelativeTime(now - 2 * 3600 * 1000, now), "2h ago");
  assert.equal(formatRelativeTime(now - 4 * 86400 * 1000, now), "4d ago");
});

test("formatRelativeTime renders future differences", () => {
  const now = 1_700_000_000_000;
  assert.equal(formatRelativeTime(now + 5000, now), "in 5s");
});

test("formatRelativeTimeFromUnixSeconds converts seconds to ms", () => {
  const nowMs = 1_700_000_000_000;
  const nowSec = nowMs / 1000;
  assert.equal(
    formatRelativeTimeFromUnixSeconds(nowSec - 60, nowMs),
    "1m ago",
  );
});

test("formatNumber adds thousands separators", () => {
  assert.equal(formatNumber(1234567), (1234567).toLocaleString());
  assert.equal(formatNumber(0), "0");
  assert.equal(formatNumber(NaN), "NaN");
});

test("formatLoad renders 2 decimals", () => {
  assert.equal(formatLoad(0.5), "0.50");
  assert.equal(formatLoad(1), "1.00");
  assert.equal(formatLoad(NaN), "0.00");
});
