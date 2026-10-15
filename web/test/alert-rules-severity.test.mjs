import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { setCatalogs, setLang } from "../assets/js/core/i18n.js";
import { ruleEditorDraft, rulePayload, ruleRow, ruleSeverity } from "../assets/js/pages/settings/alert-rules.js";
import { setDraftMetric } from "../assets/js/core/alerts.js";

function element(tagName) {
  return {
    tagName,
    children: [],
    attributes: {},
    className: "",
    textContent: "",
    listeners: {},
    append(...children) { this.children.push(...children); },
    setAttribute(key, value) { this.attributes[key] = String(value); },
    addEventListener(key, callback) { this.listeners[key] = callback; },
  };
}

globalThis.document = {
  createElement: element,
  createElementNS: (_, tag) => element(tag),
  createTextNode: (text) => ({ textContent: text }),
};
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };

const assetsDir = new URL("../assets/i18n/", import.meta.url);
const sidecar = JSON.parse(readFileSync(new URL("../../.verification/round39/i18n-worker-b.json", import.meta.url)));
setCatalogs(Object.fromEntries(["en", "ko"].map((lang) => [lang, {
  ...JSON.parse(readFileSync(new URL(`${lang}.json`, assetsDir))),
  ...sidecar[lang],
}])));
setLang("en");

const all = (node) => [node, ...(node.children || []).flatMap(all)];

test("rule request payload persists the selected severity", () => {
  const payload = rulePayload({
    name: "CPU high", enabled: true, metric: "cpu", hostID: "host-1", operator: ">", threshold: 90,
    durationValue: 5, durationUnit: "minutes", cooldownValue: 1, cooldownUnit: "hours",
    severity: "critical", notifyResolved: true, channelIDs: [7],
  });
  assert.equal(payload.severity, "critical");
  assert.equal(payload.duration_sec, 300);
  assert.equal(payload.cooldown_sec, 3600);
});

test("editing a rule preserves its chosen severity when its metric changes", () => {
  const draft = ruleEditorDraft({ metric: "cpu", severity: "critical", duration_sec: 300, cooldown_sec: 3600 });
  setDraftMetric(draft, "memory");
  assert.equal(draft.severityTouched, true);
  assert.equal(draft.severity, "critical");
});

test("rule list displays a small severity badge with old-rule fallback", () => {
  const base = {
    id: 1, name: "Host offline", enabled: true, metric: "host_down", host_id: "", operator: ">", threshold: 0,
    duration_sec: 120, channel_ids: [],
  };
  assert.equal(ruleSeverity(base), "critical");
  const row = ruleRow({
    rule: { ...base, severity: "warning" }, channels: [], hosts: [], storageAccounts: [], signal: new AbortController().signal,
    hostnameByID: () => undefined, channelNameByID: () => undefined, storageAccountNameByID: () => undefined, onChanged() {},
  });
  const badge = all(row).find((node) => node.className?.split(" ").includes("cp-rule-severity"));
  assert.ok(badge);
  assert.match(badge.className, /cp-chip-warning/);
  assert.equal(badge.textContent, "Warning");
});
