// alerts.test.mjs — unit tests for assets/js/core/alerts.js (alert rule
// display metadata, human summary sentence, presets, form validation).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  METRICS,
  OPERATORS,
  PRESETS,
  DEFAULT_COOLDOWN_SEC,
  metricByValue,
  metricLabel,
  operatorLabel,
  formatThreshold,
  formatDurationShort,
  hostScopeLabel,
  channelsLabel,
  ruleSummarySentence,
  presetByID,
  validateRuleForm,
  durationSecFromParts,
  bestDurationParts,
} from "../assets/js/core/alerts.js";

test("METRICS/OPERATORS cover every models enum value", () => {
  const values = METRICS.map((m) => m.value);
  assert.deepEqual(values, ["cpu", "memory", "disk", "load1", "egress_out_pct", "egress_in_pct", "host_down"]);
  assert.deepEqual(OPERATORS.map((o) => o.value), [">", ">="]);
});

test("metricByValue/metricLabel", () => {
  assert.equal(metricByValue("cpu").label, "CPU usage");
  assert.equal(metricByValue("nope"), undefined);
  assert.equal(metricLabel("cpu"), "CPU usage");
  assert.equal(metricLabel("unknown_metric"), "unknown_metric");
});

test("operatorLabel", () => {
  assert.equal(operatorLabel(">"), "above");
  assert.equal(operatorLabel(">="), "at or above");
  assert.equal(operatorLabel("<"), "<");
});

test("formatThreshold: percent metrics", () => {
  assert.equal(formatThreshold("cpu", 90), "90%");
  assert.equal(formatThreshold("memory", 90.5), "90.5%");
  assert.equal(formatThreshold("disk", 100), "100%");
});

test("formatThreshold: egress percent-of-limit metrics", () => {
  assert.equal(formatThreshold("egress_out_pct", 80), "80% of limit");
  assert.equal(formatThreshold("egress_in_pct", 95), "95% of limit");
});

test("formatThreshold: load1 has no unit suffix and trims trailing zeros", () => {
  assert.equal(formatThreshold("load1", 4), "4");
  assert.equal(formatThreshold("load1", 4.5), "4.5");
});

test("formatThreshold: host_down ignores threshold entirely", () => {
  assert.equal(formatThreshold("host_down", 999), "");
});

test("formatDurationShort spells out a single unit", () => {
  assert.equal(formatDurationShort(0), "0 seconds");
  assert.equal(formatDurationShort(1), "1 second");
  assert.equal(formatDurationShort(30), "30 seconds");
  assert.equal(formatDurationShort(60), "1 minute");
  assert.equal(formatDurationShort(300), "5 minutes");
  assert.equal(formatDurationShort(3600), "1 hour");
  assert.equal(formatDurationShort(7200), "2 hours");
});

test("formatDurationShort falls back to seconds when not an even minute/hour", () => {
  assert.equal(formatDurationShort(90), "90 seconds");
  assert.equal(formatDurationShort(3661), "3661 seconds");
});

test("hostScopeLabel", () => {
  assert.equal(hostScopeLabel(""), "any host");
  assert.equal(hostScopeLabel("host-1", () => "web-1"), "web-1");
  assert.equal(hostScopeLabel("host-1", () => undefined), "host-1");
  assert.equal(hostScopeLabel("host-1"), "host-1");
});

test("channelsLabel", () => {
  assert.equal(channelsLabel([], () => "x"), "no channels");
  assert.equal(channelsLabel([1, 2], (id) => (id === 1 ? "Discord #ops" : "Telegram")), "Discord #ops, Telegram");
  assert.equal(channelsLabel([1, 2], (id) => (id === 1 ? "Discord #ops" : undefined)), "Discord #ops");
  assert.equal(channelsLabel([99], () => undefined), "no channels");
});

test("ruleSummarySentence: sustained threshold metric with channels", () => {
  const rule = { metric: "cpu", host_id: "", operator: ">", threshold: 90, duration_sec: 300, channel_ids: [1, 2] };
  const sentence = ruleSummarySentence(rule, { channelNameByID: (id) => (id === 1 ? "Discord #ops" : "Telegram") });
  assert.equal(sentence, "Alert when CPU usage on any host stays above 90% for 5 minutes → Discord #ops, Telegram");
});

test("ruleSummarySentence: immediate (duration_sec 0) metric uses 'is' not 'stays'", () => {
  const rule = { metric: "disk", host_id: "", operator: ">", threshold: 90, duration_sec: 0, channel_ids: [] };
  const sentence = ruleSummarySentence(rule);
  assert.equal(sentence, "Alert when disk usage on any host is above 90% → no channels");
});

test("ruleSummarySentence: scoped to one host via hostnameByID", () => {
  const rule = { metric: "memory", host_id: "host-1", operator: ">=", threshold: 80, duration_sec: 600, channel_ids: [1] };
  const sentence = ruleSummarySentence(rule, {
    hostnameByID: (id) => (id === "host-1" ? "web-1" : undefined),
    channelNameByID: () => "Discord #ops",
  });
  assert.equal(sentence, "Alert when memory usage on web-1 stays at or above 80% for 10 minutes → Discord #ops");
});

test("ruleSummarySentence: host_down, any host", () => {
  const rule = { metric: "host_down", host_id: "", operator: ">", threshold: 0, duration_sec: 120, channel_ids: [1] };
  const sentence = ruleSummarySentence(rule, { channelNameByID: () => "Discord #ops" });
  assert.equal(sentence, "Alert when a host goes offline for 2 minutes → Discord #ops");
});

test("ruleSummarySentence: host_down, scoped host", () => {
  const rule = { metric: "host_down", host_id: "host-1", operator: ">", threshold: 0, duration_sec: 120, channel_ids: [] };
  const sentence = ruleSummarySentence(rule, { hostnameByID: () => "web-1" });
  assert.equal(sentence, "Alert when web-1 goes offline for 2 minutes → no channels");
});

test("PRESETS: every preset id is unique and rule is well-formed", () => {
  const ids = PRESETS.map((p) => p.id);
  assert.equal(new Set(ids).size, ids.length);
  for (const preset of PRESETS) {
    assert.ok(preset.label.length > 0);
    assert.ok(METRICS.some((m) => m.value === preset.rule.metric));
    assert.ok(OPERATORS.some((o) => o.value === preset.rule.operator));
    assert.equal(typeof preset.rule.duration_sec, "number");
  }
});

test("PRESETS covers every SPEC-v0.5 preset sentence", () => {
  const labels = PRESETS.map((p) => p.label);
  assert.deepEqual(labels, [
    "CPU above 90% for 5 minutes",
    "Memory above 90% for 10 minutes",
    "Disk above 90%",
    "Outbound traffic above 80% of limit",
    "Inbound traffic above 80% of limit",
    "Host offline for 2 minutes",
  ]);
});

test("presetByID", () => {
  assert.equal(presetByID("cpu-90-5m").rule.metric, "cpu");
  assert.equal(presetByID("does-not-exist"), undefined);
});

test("DEFAULT_COOLDOWN_SEC matches the hub's default", () => {
  assert.equal(DEFAULT_COOLDOWN_SEC, 3600);
});

test("validateRuleForm: valid form has no errors", () => {
  const errors = validateRuleForm({
    name: "CPU high",
    metric: "cpu",
    operator: ">",
    threshold: 90,
    durationSec: 300,
    cooldownSec: 3600,
  });
  assert.deepEqual(errors, {});
});

test("validateRuleForm: rejects empty name", () => {
  const errors = validateRuleForm({ name: "  ", metric: "cpu", operator: ">", threshold: 90, durationSec: 0, cooldownSec: 0 });
  assert.match(errors.name, /required/);
});

test("validateRuleForm: rejects unknown metric/operator", () => {
  const errors = validateRuleForm({ name: "x", metric: "bogus", operator: "!=", threshold: 1, durationSec: 0, cooldownSec: 0 });
  assert.ok(errors.metric);
  assert.ok(errors.operator);
});

test("validateRuleForm: host_down skips threshold validation", () => {
  const errors = validateRuleForm({ name: "x", metric: "host_down", operator: ">", threshold: NaN, durationSec: 120, cooldownSec: 0 });
  assert.equal(errors.threshold, undefined);
});

test("validateRuleForm: rejects negative threshold/duration/cooldown", () => {
  const errors = validateRuleForm({ name: "x", metric: "cpu", operator: ">", threshold: -1, durationSec: -5, cooldownSec: -1 });
  assert.ok(errors.threshold);
  assert.ok(errors.durationSec);
  assert.ok(errors.cooldownSec);
});

test("durationSecFromParts converts units to seconds", () => {
  assert.equal(durationSecFromParts(5, "minutes"), 300);
  assert.equal(durationSecFromParts(2, "hours"), 7200);
  assert.equal(durationSecFromParts(30, "seconds"), 30);
  assert.equal(durationSecFromParts(-5, "minutes"), 0);
});

test("bestDurationParts picks the largest evenly-dividing unit", () => {
  assert.deepEqual(bestDurationParts(0), { value: 0, unit: "minutes" });
  assert.deepEqual(bestDurationParts(300), { value: 5, unit: "minutes" });
  assert.deepEqual(bestDurationParts(3600), { value: 1, unit: "hours" });
  assert.deepEqual(bestDurationParts(90), { value: 90, unit: "seconds" });
});

test("durationSecFromParts and bestDurationParts round-trip for even values", () => {
  for (const seconds of [0, 30, 60, 300, 600, 3600, 7200]) {
    const parts = bestDurationParts(seconds);
    assert.equal(durationSecFromParts(parts.value, parts.unit), seconds);
  }
});
