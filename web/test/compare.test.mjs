import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  parseCompareHosts,
  serializeCompareHosts,
  compareColorForHost,
  assignCompareColors,
  compareMetricsKey,
  shouldReloadCompareMetrics,
  isCompareHistoryLoading,
  reconcileCompareSelection,
  maxHostsByColumn,
  isCurrentCompareRequest,
  alignMetricSeries,
} from "../assets/js/pages/compare.js";

const chartsSource = readFileSync(new URL("../assets/js/pages/charts.js", import.meta.url), "utf8");

test("compare URL parsing deduplicates, caps selection, and identifies unknown IDs", () => {
  const parsed = parseCompareHosts("missing-first,a,b,a,c,d,e,missing-last", ["a", "b", "c", "d", "e"]);
  assert.deepEqual(parsed.ids, ["a", "b", "c", "d"]);
  assert.deepEqual(parsed.unknown, ["missing-first", "missing-last"]);
  assert.equal(serializeCompareHosts(["b", "a", "b"]), "#/compare?hosts=b%2Ca");
  const unknown = parseCompareHosts("missing,a", ["a"]);
  assert.deepEqual(unknown, { ids: ["a"], unknown: ["missing"] });
});

test("steady fleet refresh preserves history charts and an explicit empty selection", () => {
  const historyKey = compareMetricsKey(["a", "b"], "1h");
  assert.equal(shouldReloadCompareMetrics(historyKey, ["a", "b"], "1h"), false);
  assert.equal(shouldReloadCompareMetrics(historyKey, ["a", "b"], "6h"), true);
  assert.equal(shouldReloadCompareMetrics(historyKey, ["a", "c"], "1h"), true);

  const firstLoad = reconcileCompareSelection({ urlSelectionSeeded: false, initialHosts: "a,b", knownIDs: ["a", "b"], selected: [], unknown: [] });
  assert.deepEqual(firstLoad.selected, ["a", "b"]);
  const afterUserClears = reconcileCompareSelection({ ...firstLoad, initialHosts: "a,b", knownIDs: ["a", "b"], selected: [], unknown: [] });
  assert.deepEqual(afterUserClears.selected, []);

  const compareSource = readFileSync(new URL("../assets/js/pages/compare.js", import.meta.url), "utf8");
  assert.match(compareSource, /shouldReloadCompareMetrics\(state\.historyKey, state\.selected, state\.range\)/);
  const fleetRenderer = compareSource.match(/function renderFleet\(\) \{([^}]*)\}/)?.[1] || "";
  assert.match(fleetRenderer, /renderPicker\(\); renderNotices\(\); renderSummary\(\);/);
  assert.doesNotMatch(fleetRenderer, /renderCharts/);
});

test("history loading status ends when every history request fails", () => {
  assert.equal(isCompareHistoryLoading(new Map(), true), true);
  assert.equal(isCompareHistoryLoading(new Map(), false), false);
  assert.equal(isCompareHistoryLoading(new Map([["a", {}]]), true), false);
});

test("host colors are deterministic and independent of chart ordering", () => {
  assert.equal(compareColorForHost("host-a"), compareColorForHost("host-a"));
  assert.equal(compareColorForHost("host-b"), compareColorForHost("host-b"));
  const first = assignCompareColors(["host-c", "host-a", "host-b"]);
  const reordered = assignCompareColors(["host-b", "host-c", "host-a"]);
  assert.deepEqual([...first], [...reordered]);
  assert.equal(new Set(first.values()).size, 3);
});

test("max summary values highlight ties and ignore unknown readings", () => {
  const maxima = maxHostsByColumn([
    { id: "a", cpu: 80, mem: 30, disk: null, net: 100 },
    { id: "b", cpu: 80, mem: 50, disk: null, net: 10 },
    { id: "c", cpu: null, mem: 10, disk: null, net: null },
  ]);
  assert.deepEqual([...maxima.cpu], ["a", "b"]);
  assert.deepEqual([...maxima.mem], ["b"]);
  assert.deepEqual([...maxima.disk], []);
  assert.deepEqual([...maxima.net], ["a"]);
});

test("stale history responses cannot apply after range or selection changes", () => {
  assert.equal(isCurrentCompareRequest(3, 3, ["a", "b"], ["a", "b"]), true);
  assert.equal(isCurrentCompareRequest(2, 3, ["a", "b"], ["a", "b"]), false);
  assert.equal(isCurrentCompareRequest(3, 3, ["a", "b"], ["b", "a"]), false);
});

test("series share only exact timestamps and preserve missing samples as gaps", () => {
  const histories = new Map([
    ["a", { ts: [10, 20], cpu: [1, null] }],
    ["b", { ts: [20, 30], cpu: [2, 3] }],
  ]);
  assert.deepEqual(alignMetricSeries(histories, ["a", "b"], "cpu"), {
    ts: [10, 20, 30], values: [[1, null, null], [null, 2, 3]],
  });
});

test("comparison charts opt into thin lines without changing area-chart defaults", () => {
  assert.match(chartsSource, /lineOnly = false/);
  assert.match(chartsSource, /width: lineOnly \? 1\.5 : 2/);
  assert.match(chartsSource, /lineOnly \? \{\} : \{ fill: `\$\{s\.color\}26` \}/);
  const compareSource = readFileSync(new URL("../assets/js/pages/compare.js", import.meta.url), "utf8");
  assert.match(compareSource, /lineOnly: true/);
  assert.doesNotMatch(compareSource, /cp-compare-legend/);
});
