// hosttable.test.mjs — pure-helper tests for pages/hosttable.js (sort
// accessors, filter matching, threshold classification, columns
// persistence normalization).
import test from "node:test";
import assert from "node:assert/strict";
import {
  COLUMNS,
  defaultVisibleColumns,
  columnAccessors,
  thresholdLevel,
  thresholdBarClass,
  matchesFilter,
  filterRows,
  agentDotLevel,
  normalizeVisibleColumns,
} from "../assets/js/pages/hosttable.js";

test("thresholdLevel classifies per spec boundaries", () => {
  assert.equal(thresholdLevel(0), "ok");
  assert.equal(thresholdLevel(64.9), "ok");
  assert.equal(thresholdLevel(65), "warning");
  assert.equal(thresholdLevel(89.9), "warning");
  assert.equal(thresholdLevel(90), "critical");
  assert.equal(thresholdLevel(100), "critical");
  assert.equal(thresholdLevel(NaN), "ok");
});

test("thresholdBarClass maps to cp-bar-* classes", () => {
  assert.equal(thresholdBarClass(10), "cp-bar-ok");
  assert.equal(thresholdBarClass(70), "cp-bar-warning");
  assert.equal(thresholdBarClass(95), "cp-bar-critical");
});

test("columnAccessors: system sorts case-insensitively by hostname", () => {
  const a = { host: { hostname: "Zeta" } };
  const b = { host: { hostname: "alpha" } };
  assert.equal(columnAccessors.system(a), "zeta");
  assert.equal(columnAccessors.system(b), "alpha");
});

test("columnAccessors: numeric metrics fall back sanely when latest is missing", () => {
  const row = {};
  assert.equal(columnAccessors.cpu(row), -1);
  assert.equal(columnAccessors.memory(row), -1);
  assert.equal(columnAccessors.disk(row), -1);
  assert.equal(columnAccessors.load(row), -1);
  assert.equal(columnAccessors.outbound(row), 0);
  assert.equal(columnAccessors.inbound(row), 0);
  assert.equal(columnAccessors.lastSeen(row), 0);
});

test("columnAccessors: egress bytes read from egress sub-object", () => {
  const row = { egress: { tx_bytes: 500, rx_bytes: 200 } };
  assert.equal(columnAccessors.outbound(row), 500);
  assert.equal(columnAccessors.inbound(row), 200);
});

test("matchesFilter: empty query matches everything", () => {
  assert.equal(matchesFilter({ host: { hostname: "web-1" } }, ""), true);
  assert.equal(matchesFilter({ host: { hostname: "web-1" } }, "   "), true);
});

test("matchesFilter: matches hostname/id/provider/status/os/arch case-insensitively", () => {
  const row = { host: { hostname: "Web-1", id: "web-1-id", provider: "aws", os: "linux", arch: "amd64" }, status: "up" };
  assert.equal(matchesFilter(row, "web"), true);
  assert.equal(matchesFilter(row, "AWS"), true);
  assert.equal(matchesFilter(row, "up"), true);
  assert.equal(matchesFilter(row, "linux"), true);
  assert.equal(matchesFilter(row, "arm"), false);
});

test("filterRows filters a list down using matchesFilter", () => {
  const rows = [
    { host: { hostname: "web-1" }, status: "up" },
    { host: { hostname: "db-1" }, status: "down" },
  ];
  assert.deepEqual(filterRows(rows, "web").map((r) => r.host.hostname), ["web-1"]);
  assert.equal(filterRows(rows, "").length, 2);
});

test("agentDotLevel: warning when update available, ok otherwise", () => {
  assert.equal(agentDotLevel({ available: true }), "warning");
  assert.equal(agentDotLevel({ available: false }), "ok");
  assert.equal(agentDotLevel(null), "ok");
  assert.equal(agentDotLevel(undefined), "ok");
});

test("defaultVisibleColumns includes every known column key", () => {
  const defaults = defaultVisibleColumns();
  assert.equal(defaults.length, COLUMNS.length);
  for (const c of COLUMNS) assert.ok(defaults.includes(c.key));
});

test("normalizeVisibleColumns falls back to defaults for invalid/empty input", () => {
  assert.deepEqual(normalizeVisibleColumns(null), defaultVisibleColumns());
  assert.deepEqual(normalizeVisibleColumns(undefined), defaultVisibleColumns());
  assert.deepEqual(normalizeVisibleColumns("nonsense"), defaultVisibleColumns());
  assert.deepEqual(normalizeVisibleColumns([]), defaultVisibleColumns());
});

test("normalizeVisibleColumns drops unknown keys and always includes always-visible columns", () => {
  const result = normalizeVisibleColumns(["cpu", "bogus-key"]);
  assert.ok(result.includes("cpu"));
  assert.ok(result.includes("system")); // always-visible
  assert.ok(!result.includes("bogus-key"));
});

test("normalizeVisibleColumns preserves a valid persisted subset", () => {
  const result = normalizeVisibleColumns(["cpu", "memory"]);
  assert.ok(result.includes("cpu"));
  assert.ok(result.includes("memory"));
  assert.ok(result.includes("system"));
  assert.ok(!result.includes("disk"));
});
