// ui-audit-lib.test.mjs — unit tests for scripts/ui-audit-lib.mjs's pure
// color/contrast/target-size/report-aggregation logic. No browser/DOM
// dependency (matches this repo's web/test/ convention).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  parseColor,
  compositeOver,
  resolveCompositedBackground,
  relativeLuminance,
  contrastRatio,
  requiredContrastRatio,
  evaluateContrast,
  evaluateTargetSize,
  violationKey,
  buildRouteKey,
  parseViewportSpec,
  isAllowed,
  parseAllowList,
  aggregateReport,
} from "../../scripts/ui-audit-lib.mjs";

test("parseColor: rgb()/rgba() comma syntax", () => {
  assert.deepEqual(parseColor("rgb(255, 0, 0)"), { r: 255, g: 0, b: 0, a: 1 });
  assert.deepEqual(parseColor("rgba(10, 20, 30, 0.5)"), { r: 10, g: 20, b: 30, a: 0.5 });
});

test("parseColor: modern space-separated syntax with slash alpha", () => {
  assert.deepEqual(parseColor("rgb(1 2 3 / 0.25)"), { r: 1, g: 2, b: 3, a: 0.25 });
  assert.deepEqual(parseColor("rgb(1 2 3 / 50%)"), { r: 1, g: 2, b: 3, a: 0.5 });
});

test("parseColor: hex 3/4/6/8 digit forms", () => {
  assert.deepEqual(parseColor("#fff"), { r: 255, g: 255, b: 255, a: 1 });
  assert.deepEqual(parseColor("#000f"), { r: 0, g: 0, b: 0, a: 1 });
  assert.deepEqual(parseColor("#112233"), { r: 17, g: 34, b: 51, a: 1 });
  assert.deepEqual(parseColor("#11223380"), { r: 17, g: 34, b: 51, a: 128 / 255 });
});

test("parseColor: transparent keyword", () => {
  assert.deepEqual(parseColor("transparent"), { r: 0, g: 0, b: 0, a: 0 });
});

test("parseColor: unparseable oklch()/currentColor/garbage returns null", () => {
  assert.equal(parseColor("oklch(0.7 0.15 200)"), null);
  assert.equal(parseColor("color-mix(in oklab, red, blue)"), null);
  assert.equal(parseColor("currentcolor"), null);
  assert.equal(parseColor("not-a-color"), null);
  assert.equal(parseColor(""), null);
  assert.equal(parseColor(undefined), null);
});

test("compositeOver: opaque foreground fully replaces background", () => {
  const result = compositeOver({ r: 255, g: 0, b: 0, a: 1 }, { r: 0, g: 0, b: 255, a: 1 });
  assert.deepEqual(result, { r: 255, g: 0, b: 0, a: 1 });
});

test("compositeOver: 50% alpha foreground blends evenly over opaque background", () => {
  const result = compositeOver({ r: 255, g: 255, b: 255, a: 0.5 }, { r: 0, g: 0, b: 0, a: 1 });
  assert.equal(result.a, 1);
  assert.equal(Math.round(result.r), 128);
});

test("resolveCompositedBackground: walks ancestor chain nearest-first, composites outer-to-inner", () => {
  // nearest element is transparent, its parent is semi-transparent red,
  // grandparent (<html>) is opaque white.
  const bg = resolveCompositedBackground(["transparent", "rgba(255,0,0,0.5)", "#ffffff"]);
  assert.ok(bg);
  assert.equal(bg.a, 1);
  assert.equal(Math.round(bg.r), 255);
  assert.equal(Math.round(bg.g), 128);
  assert.equal(Math.round(bg.b), 128);
});

test("resolveCompositedBackground: returns null when nothing in the chain is ever opaque", () => {
  assert.equal(resolveCompositedBackground(["transparent", "rgba(0,0,0,0.3)"]), null);
});

test("resolveCompositedBackground: skips unparseable layers rather than aborting", () => {
  const bg = resolveCompositedBackground(["currentcolor", "#000000"]);
  assert.deepEqual(bg, { r: 0, g: 0, b: 0, a: 1 });
});

test("resolveCompositedBackground: empty input returns null", () => {
  assert.equal(resolveCompositedBackground([]), null);
});

test("relativeLuminance: black is 0, white is 1", () => {
  assert.equal(relativeLuminance({ r: 0, g: 0, b: 0 }), 0);
  assert.equal(relativeLuminance({ r: 255, g: 255, b: 255 }), 1);
});

test("contrastRatio: black on white is 21:1", () => {
  const ratio = contrastRatio({ r: 0, g: 0, b: 0 }, { r: 255, g: 255, b: 255 });
  assert.equal(Math.round(ratio), 21);
});

test("contrastRatio: identical colors is 1:1", () => {
  const ratio = contrastRatio({ r: 128, g: 128, b: 128 }, { r: 128, g: 128, b: 128 });
  assert.equal(ratio, 1);
});

test("contrastRatio: order of arguments does not matter", () => {
  const a = { r: 10, g: 10, b: 10 };
  const b = { r: 240, g: 240, b: 240 };
  assert.equal(contrastRatio(a, b), contrastRatio(b, a));
});

test("requiredContrastRatio: normal text needs 4.5:1", () => {
  assert.equal(requiredContrastRatio({ fontSizePx: 16, bold: false }), 4.5);
});

test("requiredContrastRatio: large text (>=24px) needs 3:1", () => {
  assert.equal(requiredContrastRatio({ fontSizePx: 24, bold: false }), 3);
});

test("requiredContrastRatio: bold >=18.66px counts as large text", () => {
  assert.equal(requiredContrastRatio({ fontSizePx: 19, bold: true }), 3);
  assert.equal(requiredContrastRatio({ fontSizePx: 18, bold: true }), 4.5);
});

test("requiredContrastRatio: UI component override needs 3:1 regardless of size", () => {
  assert.equal(requiredContrastRatio({ fontSizePx: 12, bold: false, isUIComponent: true }), 3);
});

test("evaluateContrast: passes for black text on white background", () => {
  const result = evaluateContrast({
    foreground: "rgb(0, 0, 0)",
    backgroundsNearestFirst: ["rgb(255, 255, 255)"],
    fontSizePx: 16,
    bold: false,
  });
  assert.equal(result.skipped, false);
  assert.equal(result.pass, true);
  assert.ok(result.ratio >= 4.5);
});

test("evaluateContrast: fails for low-contrast gray-on-gray body text", () => {
  const result = evaluateContrast({
    foreground: "rgb(150, 150, 150)",
    backgroundsNearestFirst: ["rgb(180, 180, 180)"],
    fontSizePx: 14,
    bold: false,
  });
  assert.equal(result.skipped, false);
  assert.equal(result.pass, false);
});

test("evaluateContrast: skips when foreground is fully transparent", () => {
  const result = evaluateContrast({
    foreground: "rgba(0,0,0,0)",
    backgroundsNearestFirst: ["#fff"],
    fontSizePx: 16,
    bold: false,
  });
  assert.equal(result.skipped, true);
});

test("evaluateContrast: skips when no opaque background is found (transparent-over-image case)", () => {
  const result = evaluateContrast({
    foreground: "rgb(0,0,0)",
    backgroundsNearestFirst: ["transparent", "rgba(0,0,0,0.2)"],
    fontSizePx: 16,
    bold: false,
  });
  assert.equal(result.skipped, true);
});

test("evaluateContrast: composites a semi-transparent foreground before computing the ratio", () => {
  const result = evaluateContrast({
    foreground: "rgba(0,0,0,0.5)",
    backgroundsNearestFirst: ["#ffffff"],
    fontSizePx: 16,
    bold: false,
  });
  assert.equal(result.skipped, false);
  // 50% black over white composites to mid-gray (~128,128,128), a lower
  // ratio than solid black-on-white but still meaningfully non-1.
  assert.ok(result.ratio > 1 && result.ratio < 21);
});

test("evaluateTargetSize: passes at exactly 24x24", () => {
  assert.deepEqual(evaluateTargetSize({ widthPx: 24, heightPx: 24, isInline: false }), { pass: true });
});

test("evaluateTargetSize: fails below 24x24", () => {
  const result = evaluateTargetSize({ widthPx: 20, heightPx: 20, isInline: false });
  assert.equal(result.pass, false);
  assert.equal(result.widthPx, 20);
  assert.equal(result.heightPx, 20);
});

test("evaluateTargetSize: inline exception always passes regardless of size", () => {
  assert.deepEqual(evaluateTargetSize({ widthPx: 8, heightPx: 8, isInline: true }), { pass: true });
});

test("violationKey/buildRouteKey: stable composition", () => {
  const routeKey = buildRouteKey({ route: "#/settings/billing", viewport: "360x800", theme: "dark" });
  assert.equal(routeKey, "#/settings/billing@360x800@dark");
  assert.equal(violationKey("contrast", routeKey), "contrast:#/settings/billing@360x800@dark");
});

test("parseViewportSpec: parses WxH", () => {
  assert.deepEqual(parseViewportSpec("390x844"), { width: 390, height: 844, label: "390x844" });
});

test("parseViewportSpec: rejects malformed specs", () => {
  assert.throws(() => parseViewportSpec("390"), /invalid viewport spec/);
  assert.throws(() => parseViewportSpec("0x800"), /must be positive/);
  assert.throws(() => parseViewportSpec("390xabc"), /invalid viewport spec/);
});

test("isAllowed: true only when a matching key has a non-empty reason", () => {
  const list = [{ key: "contrast:#/@1x1@dark", reason: "known chart-axis issue, tracked in #123" }];
  assert.equal(isAllowed(list, "contrast:#/@1x1@dark"), true);
  assert.equal(isAllowed(list, "contrast:#/@1x1@light"), false);
  assert.equal(isAllowed([{ key: "x", reason: "" }], "x"), false);
});

test("parseAllowList: accepts a bare array", () => {
  const parsed = parseAllowList([{ key: "a", reason: "r" }]);
  assert.deepEqual(parsed, [{ key: "a", reason: "r" }]);
});

test("parseAllowList: accepts an {entries: [...]} wrapper (for a leading _comment field)", () => {
  const parsed = parseAllowList({ _comment: "see header", entries: [{ key: "a", reason: "r" }] });
  assert.deepEqual(parsed, [{ key: "a", reason: "r" }]);
});

test("parseAllowList: null/undefined means an empty allow-list", () => {
  assert.deepEqual(parseAllowList(null), []);
  assert.deepEqual(parseAllowList(undefined), []);
});

test("parseAllowList: throws on an entry missing a reason", () => {
  assert.throws(() => parseAllowList([{ key: "a" }]), /missing a non-empty "reason"/);
  assert.throws(() => parseAllowList([{ key: "a", reason: "  " }]), /missing a non-empty "reason"/);
});

test("parseAllowList: throws on an entry missing a key", () => {
  assert.throws(() => parseAllowList([{ reason: "r" }]), /missing a non-empty "key"/);
});

test("parseAllowList: throws when the top-level shape is wrong", () => {
  assert.throws(() => parseAllowList({ foo: "bar" }), /must be a JSON array/);
});

test("aggregateReport: splits findings into violations vs. allowed, with a summary", () => {
  const findings = [
    { key: "contrast:#/@360x800@dark", checkId: "contrast", routeKey: "#/@360x800@dark", detail: { ratio: 2 } },
    { key: "overflow:#/@360x800@dark", checkId: "overflow", routeKey: "#/@360x800@dark", detail: {} },
    { key: "contrast:#/@360x800@light", checkId: "contrast", routeKey: "#/@360x800@light", detail: { ratio: 3 } },
  ];
  const allowList = [{ key: "overflow:#/@360x800@dark", reason: "tracked in #99, fix landing in web stage" }];
  const report = aggregateReport(findings, allowList);
  assert.equal(report.summary.total, 3);
  assert.equal(report.summary.violations, 2);
  assert.equal(report.summary.allowed, 1);
  assert.equal(report.summary.byCheck.contrast, 2);
  assert.equal(report.summary.byCheck.overflow, 1);
  assert.equal(report.violations.length, 2);
  assert.equal(report.allowed.length, 1);
  assert.equal(report.allowed[0].reason, "tracked in #99, fix landing in web stage");
});

test("aggregateReport: empty findings produces a zeroed summary", () => {
  const report = aggregateReport([], []);
  assert.equal(report.summary.total, 0);
  assert.equal(report.summary.violations, 0);
  assert.equal(report.summary.allowed, 0);
  assert.deepEqual(report.summary.byCheck, {});
});
