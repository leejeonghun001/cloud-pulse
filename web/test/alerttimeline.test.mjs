import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import * as timeline from "../assets/js/pages/alerttimeline.js";
import * as components from "../assets/js/ui/components.js";
import { selectField } from "../assets/js/ui/select.js";
import { icon } from "../assets/js/ui/icons.js";
import { formatDuration } from "../assets/js/core/format.js";
import { eventSeverity, metricLabel } from "../assets/js/core/alerts.js";
import { setCatalogs, setLang, t } from "../assets/js/core/i18n.js";

const storage = new Map();
globalThis.localStorage = { getItem: (key) => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, String(value)), removeItem: (key) => storage.delete(key) };
setCatalogs(Object.fromEntries(["en", "ko"].map((lang) => [lang, JSON.parse(readFileSync(new URL(`../assets/i18n/${lang}.json`, import.meta.url)))])));
function element(tagName) {
  return { tagName, children: [], attributes: {}, className: "", textContent: "", listeners: {},
    get id() { return this.attributes.id || ""; },
    get firstChild() { return this.children[0]; },
    get classList() { const node = this; return { add(...names) { node.className = [node.className, ...names].filter(Boolean).join(" "); } }; },
    append(...children) { this.children.push(...children); },
    removeChild(child) { this.children.splice(this.children.indexOf(child), 1); },
    setAttribute(key, value) { this.attributes[key] = String(value); },
    getAttribute(key) { return this.attributes[key] ?? null; },
    hasAttribute(key) { return key in this.attributes; },
    focus(options) { this.focusOptions = options; globalThis.document.activeElement = this; },
    getBoundingClientRect() { return globalThis.measureNode?.(this) || { top: 0, bottom: 0 }; },
    addEventListener(key, callback) { this.listeners[key] = callback; },
    contains(node) { return all(this).includes(node); },
    querySelectorAll(selector) { return all(this).filter((node) => selector === "[id]" ? node.attributes?.id : selector === "[data-event-id]" && node.attributes?.["data-event-id"] !== undefined); },
  };
}
globalThis.document = { createElement: element, createElementNS: (_, tag) => element(tag), createTextNode: (text) => ({ textContent: text }) };
const all = (node) => [node, ...(node.children || []).flatMap(all)];
const text = (node) => all(node).map((part) => part.textContent || "").join(" ");
const classes = (node, cls) => all(node).filter((part) => part.className?.split(" ").includes(cls));
const source = readFileSync(new URL("../assets/js/pages/alerts.js", import.meta.url), "utf8");
function event(overrides = {}) {
  return { id: 1, rule_name: "CPU rule", host_id: "host / #한글", hostname: "host-name", metric: "cpu", value: 92.5, threshold: 90, state: "resolved", started_at: 1_700_000_000, resolved_at: 1_700_000_120, deliveries: [], ...overrides };
}
function renderer(overrides = {}) {
  const code = source.replace(/^import[\s\S]*?;\s*$/gm, "").replace(/^export\s+/gm, "");
  return runInNewContext(`${code}\n({ mountAlertsPage, renderHistoryCard, renderActiveCard });`, { ...components, ...timeline, selectField, icon, formatDuration, eventSeverity, metricLabel, t, document: globalThis.document, AbortController, ...overrides });
}
function names(root) {
  for (const node of all(root)) if (node.attributes?.["aria-labelledby"]) {
    const target = all(root).filter((part) => part.attributes?.id === node.attributes["aria-labelledby"]);
    assert.equal(target.length, 1); assert.ok(target[0].textContent);
  }
}

test("timeline preserves API occurrence order, local date groups and compact occurrence start times", () => {
  setLang("en");
  const events = [event({ id: 3, state: "firing", started_at: 1_700_100_000 }), event({ id: 2, started_at: 1_700_099_900, resolved_at: 1_700_100_100 }), event()];
  const before = JSON.stringify(events);
  const groups = timeline.groupAlertEvents(events);
  assert.deepEqual(groups.flatMap((group) => group.events.map((item) => item.id)), [3, 2, 1]);
  const root = timeline.buildAlertTimeline(events, 1_700_100_120);
  assert.deepEqual(all(root).filter((node) => node.tagName === "li").map((node) => node.attributes["data-event-id"]), ["3", "2", "1"]);
  assert.match(text(root), /Firing/); assert.match(text(root), /Resolved/);
  assert.match(text(root), /92\.5% vs 90%/);
  assert.match(text(root), /Duration 2m/);
  assert.ok(all(root).some((node) => node.tagName === "time" && node.attributes.datetime === new Date(events[1].started_at * 1000).toISOString()));
  assert.equal(JSON.stringify(events), before); names(root);
});

test("host-scoped timelines omit their redundant host link while retaining the two row lines", () => {
  setLang("en");
  const root = timeline.buildAlertTimeline([event({ state: "firing" })], 1_700_000_120, { hideHost: true });
  const occurrence = all(root).find((node) => node.attributes?.["data-event-id"] === "1");
  assert.equal(classes(occurrence, "cp-activity-host").length, 0);
  assert.equal(classes(occurrence, "cp-activity-primary").length, 1);
  assert.equal(classes(occurrence, "cp-activity-secondary").length, 1);
  assert.match(text(occurrence), /CPU rule/);
  assert.match(text(occurrence), /Firing for 2m/);
});

test("null times, values and unrecognized states are unknown, not zero/resolved/success", () => {
  setLang("en");
  const root = timeline.buildAlertTimeline([event({ state: null, started_at: null, resolved_at: null, value: null, threshold: null, host_id: null, hostname: null, rule_name: null, metric: null, deliveries: [{ ok: null, channel_name: null }] })]);
  for (const word of ["Unknown", "Date unknown", "Time unknown", "Duration unknown", "Host unknown", "Rule unknown", "delivery status unknown"]) assert.ok(text(root).includes(word), word);
  assert.doesNotMatch(text(root), /0\.0|0s|Resolved|delivered/);
  assert.equal(all(root).filter((node) => node.tagName === "a").length, 0);
  assert.equal(timeline.eventDate(0), null); assert.equal(timeline.eventDate("100"), null);
  names(root);
});

test("host routes are encoded, storage scopes are not false host links, hostile names stay text", () => {
  setLang("ko");
  const hostile = "긴규칙이름-<img onerror=alert(1)>";
  const root = timeline.buildAlertTimeline([event({ rule_name: hostile, hostname: hostile, deliveries: [{ ok: false, channel_name: hostile, error: "<script>failure</script>" }] })]);
  assert.equal(classes(root, "cp-activity-rule")[0].textContent, hostile);
  assert.equal(all(root).find((node) => node.tagName === "a").attributes.href, `#/host/${encodeURIComponent(event().host_id)}`);
  assert.equal(all(root).filter((node) => ["img", "script"].includes(node.tagName)).length, 0);
  assert.match(text(root), /해결/); assert.ok(text(root).includes("<script>failure</script>"));
  const storage = timeline.eventHost(event({ metric: "storage_usage_pct" }));
  assert.equal(storage.tagName, "span");
  setLang("en");
});

test("delivery outcomes retain full visible failure context and absence is not delivery success", () => {
  setLang("en");
  const root = timeline.deliveryChips([{ channel_name: "Channel A", ok: true }, { channel_name: "Channel B", ok: false, error: "permission denied" }]);
  assert.match(text(root), /Channel A: delivered/); assert.match(text(root), /Channel B: permission denied/);
  assert.match(text(timeline.deliveryChips([])), /No delivery attempts recorded/);
  assert.equal(timeline.eventCondition(event({ metric: "host_down", value: 0, threshold: 0 })), metricLabel("host_down"));
});

test("timeline tones follow firing severity, including the old-event fallback", () => {
  setLang("en");
  const root = timeline.buildAlertTimeline([
    event({ id: 1, state: "firing", severity: "warning" }),
    event({ id: 2, state: "firing", metric: "host_down" }),
    event({ id: 3, state: "firing", metric: "cpu" }),
    event({ id: 4, state: "resolved", severity: "critical" }),
    event({ id: 5, state: "unknown", rule_level: "warning" }),
  ]);
  const items = all(root).filter((node) => node.attributes?.["data-event-id"]);
  assert.match(items[0].className, /cp-activity-event-warning/);
  assert.match(items[1].className, /cp-activity-event-critical/);
  assert.match(items[2].className, /cp-activity-event-warning/);
  assert.match(items[3].className, /cp-activity-event-ok/);
  assert.match(items[4].className, /cp-activity-event-neutral/);
  assert.equal(timeline.eventCondition(event({ metric: "egress_out_pct", value: 85, threshold: 80 })), "Outbound traffic: 85.0% vs 80% of limit");
});

test("history controls preserve state filter/pagination and honest loading/error/empty semantics", () => {
  setLang("en");
  const api = renderer(), root = element("div"), changes = [], more = [];
  const state = { stateFilter: "firing", hostFilter: "", events: [event()], hasMore: true, loading: false };
  api.renderHistoryCard(root, state, { onFilterChange: (value) => changes.push(value), onLoadMore: () => more.push(true) });
  names(root);
  const select = all(root).find((node) => node.tagName === "select");
  assert.deepEqual(select.children.map((option) => option.attributes.value), ["", "firing", "resolved"]);
  select.value = "resolved"; select.listeners.change();
  assert.deepEqual(JSON.parse(JSON.stringify(changes)), [{ stateFilter: "resolved", hostFilter: "" }]);
  all(root).find((node) => node.attributes?.id === "cp-activity-load-more").listeners.click();
  assert.equal(more.length, 1);
  const loading = element("div"); api.renderHistoryCard(loading, { ...state, events: [], loading: true }, {});
  assert.match(text(loading), /Loading alert history/); assert.doesNotMatch(text(loading), /No events/);
  const error = element("div"); api.renderHistoryCard(error, { ...state, events: [], historyError: "failed" }, { onFilterChange() {} });
  assert.match(text(error), /failed/); assert.doesNotMatch(text(error), /No events match/);
  const empty = element("div"); api.renderHistoryCard(empty, { ...state, events: [], hasMore: false }, {});
  assert.match(text(empty), /No events match this state/);
  assert.equal(all(root).filter((node) => node.tagName === "button").length, 2, "only Refresh and Load more, no mute/rule mutations");
});

test("mounted history uses API cursors, prevents duplicate load-more and rejects stale filter responses", async () => {
  setLang("en");
  const requests = [], announcements = [];
  const api = renderer({ getActiveAlerts: async () => ({ events: [] }), getAlertEvents: (params, signal) => new Promise((resolve) => requests.push({ params, signal, resolve })) });
  const root = element("div"), handle = api.mountAlertsPage(root, { announce: (value) => announcements.push(value) });
  const first = handle.refresh();
  const page = Array.from({ length: 50 }, (_, index) => event({ id: index + 1, started_at: 1_700_000_000 - index }));
  requests[0].resolve({ events: page }); await first;
  assert.equal(requests[0].params.limit, 50);
  const button = all(root).find((node) => node.attributes?.id === "cp-activity-load-more");
  button.listeners.click(); button.listeners.click();
  assert.equal(requests.length, 2);
  assert.equal(requests[1].params.before, page[49].started_at);
  requests[1].resolve({ events: [event({ id: 51, started_at: 1_699_000_000 })] });
  for (let index = 0; index < 5; index++) await Promise.resolve();
  assert.equal(all(root).filter((node) => node.tagName === "li" && node.attributes?.["data-event-id"]).length, 51);
  const select = all(root).find((node) => node.tagName === "select");
  select.value = "firing"; select.listeners.change();
  const secondSelect = all(root).find((node) => node.tagName === "select");
  secondSelect.value = "resolved"; secondSelect.listeners.change();
  assert.equal(requests[2].signal.aborted, true);
  requests[3].resolve({ events: [event({ id: 99, rule_name: "latest filter" })] });
  requests[2].resolve({ events: [event({ id: 98, rule_name: "stale filter" })] });
  for (let index = 0; index < 6; index++) await Promise.resolve();
  assert.match(text(root), /latest filter/); assert.doesNotMatch(text(root), /stale filter/);
  const reload = handle.refresh();
  assert.equal(requests[4].params.before, undefined, "refresh reloads newest page, not older pagination");
  requests[4].resolve({ events: [event({ id: 100 })] }); await reload;
  handle.teardown(); assert.equal(requests[4].signal.aborted, true);
  assert.equal(announcements.length, 2);
});

test("failed newest-page refresh preserves last-known history and its pagination cursor", async () => {
  setLang("en");
  let request = 0;
  const params = [];
  const page = Array.from({ length: 50 }, (_, index) => event({ id: index + 1, started_at: 1_700_000_000 - index }));
  const api = renderer({ ApiError: class extends Error {}, getActiveAlerts: async () => ({ events: [] }), getAlertEvents: async (query) => {
    params.push(query); request++;
    if (request === 2) throw new Error("unavailable");
    return { events: request === 1 ? page : [] };
  } });
  const root = element("div"), handle = api.mountAlertsPage(root, {});
  await handle.refresh(); await handle.refresh();
  assert.equal(all(root).filter((node) => node.attributes?.["data-event-id"]).length, 50);
  assert.match(text(root), /Unable to reach the hub API/);
  all(root).find((node) => node.attributes?.id === "cp-activity-load-more").listeners.click();
  for (let index = 0; index < 4; index++) await Promise.resolve();
  assert.equal(params[2].before, page[49].started_at);
  handle.teardown();
});

test("active alerts keep actual rule/host/value/duration and never add mutation controls", () => {
  setLang("en");
  const root = element("div");
  renderer().renderActiveCard(root, [event({ state: "firing", started_at: Math.floor(Date.now() / 1000) - 120 }), event({ id: 2, value: null, threshold: null, started_at: 0 })]);
  assert.match(text(root), /CPU rule/);
  assert.match(text(root), /92\.5% vs 90%/);
  assert.match(text(root), /Firing for 2m/);
  assert.match(text(root), /Duration unknown/);
  assert.equal(all(root).filter((node) => node.tagName === "a").length, 1, "a shared host group has one host link");
  assert.equal(all(root).filter((node) => ["button", "input"].includes(node.tagName)).length, 0);
  assert.equal(classes(root, "cp-alerts-active-detail").length, 2, "each active alert keeps condition and duration together on its second line");
  names(root);
});

test("grouped active-host counts expose an accessible localized count", () => {
  setLang("en");
  const root = element("div");
  renderer().renderActiveCard(root, [event({ state: "firing" }), event({ id: 2, state: "firing" })]);
  const count = classes(root, "cp-chip-other")[0];
  assert.equal(count.textContent, "2");
  assert.equal(count.attributes["aria-label"], t("alerts_page.active_alert_count", { count: 2 }));
});

test("scheduled refresh preserves paginated history DOM, focus and reading position; explicit Refresh reloads newest", async () => {
  setLang("en");
  const requests = [];
  let activeCount = 0;
  const viewport = { scrollX: 0, scrollY: 0, innerHeight: 800, scrollCalls: [],
    scrollTo(options) { this.scrollCalls.push(options); this.scrollX = options.left; this.scrollY = options.top; },
  };
  const api = renderer({ window: viewport,
    getActiveAlerts: async () => ({ events: Array.from({ length: activeCount }, (_, i) => event({ id: 200 + i, state: "firing" })) }),
    getAlertEvents: (params, signal) => new Promise((resolve) => requests.push({ params, signal, resolve })),
  });
  const root = element("div"), handle = api.mountAlertsPage(root, {});
  const firstPage = Array.from({ length: 50 }, (_, i) => event({ id: i + 1, started_at: 1_700_000_000 - i }));
  const olderPage = Array.from({ length: 50 }, (_, i) => event({ id: i + 51, started_at: 1_699_000_000 - i }));
  const occurrences = () => all(root).filter((node) => node.attributes?.["data-event-id"] !== undefined);
  const initial = handle.refresh({ reason: "initial" });
  requests[0].resolve({ events: firstPage }); await initial;
  const more = all(root).find((node) => node.id === "cp-activity-load-more");
  more.listeners.click();
  assert.equal(requests[1].params.before, firstPage[49].started_at);
  await handle.refresh({ reason: "scheduled" });
  assert.equal(requests.length, 2, "background refresh must not cancel in-flight pagination");
  assert.equal(requests[1].signal.aborted, false);
  requests[1].resolve({ events: olderPage });
  for (let i = 0; i < 6; i++) await Promise.resolve();
  assert.equal(occurrences().length, 100);
  const olderLink = all(root).find((node) => node.id === "cp-activity-host-51");
  const oldNodes = occurrences();
  olderLink.focus({ preventScroll: true });
  viewport.scrollY = 4300;
  globalThis.measureNode = (node) => {
    const index = occurrences().findIndex((eventNode) => all(eventNode).includes(node));
    const count = classes(root, "cp-alerts-active-item").length;
    const loadingNotice = classes(root, "cp-activity-notice").length;
    const top = 300 + count * 100 + index * 80 + loadingNotice * 24 - viewport.scrollY;
    return { top, bottom: top + 80 };
  };
  try {
    const beforeTop = olderLink.getBoundingClientRect().top;
    activeCount = 3;
    await handle.refresh({ reason: "scheduled" });
    assert.equal(requests.length, 2);
    assert.deepEqual(occurrences(), oldNodes, "loaded history is not rebuilt by scheduled refresh");
    assert.equal(globalThis.document.activeElement, olderLink);
    assert.equal(olderLink.getBoundingClientRect().top, beforeTop);
    assert.equal(viewport.scrollY, 4600, "active-alert growth is compensated above the reading anchor");
    assert.equal(viewport.scrollCalls.length, 1);

    all(root).find((node) => node.id === "cp-activity-load-more").listeners.click();
    assert.equal(requests[2].params.before, olderPage[49].started_at, "scheduled refresh preserves the API cursor");
    const focusedReplacement = globalThis.document.activeElement;
    assert.notEqual(focusedReplacement, olderLink, "Load more executes the real rebuilding renderer");
    assert.equal(focusedReplacement.id, olderLink.id);
    assert.equal(focusedReplacement.focusOptions.preventScroll, true);
    assert.equal(focusedReplacement.getBoundingClientRect().top, beforeTop, "loading notice does not move the reading anchor");
    await handle.refresh({ reason: "scheduled" });
    assert.equal(requests.length, 3);
    assert.equal(requests[2].signal.aborted, false);
    requests[2].resolve({ events: [] });
    for (let i = 0; i < 6; i++) await Promise.resolve();
    assert.equal(globalThis.document.activeElement.id, olderLink.id);
    assert.equal(globalThis.document.activeElement.getBoundingClientRect().top, beforeTop);

    const button = all(root).find((node) => node.id === "cp-activity-refresh");
    button.focus({ preventScroll: true });
    const explicit = button.listeners.click();
    assert.equal(requests[3].params.before, undefined);
    requests[3].resolve({ events: [event({ id: 999, rule_name: "newest occurrence" })] }); await explicit;
    assert.deepEqual(occurrences().map((node) => node.attributes["data-event-id"]), ["999"]);
    assert.equal(globalThis.document.activeElement.id, "cp-activity-refresh");
    assert.match(text(root), /newest occurrence/);
  } finally {
    delete globalThis.measureNode;
    globalThis.document.activeElement = null;
    handle.teardown();
  }
});

test("scheduled refresh retains a confirmed empty history and current filter; explicit refresh uses that filter", async () => {
  setLang("ko");
  const requests = [];
  const api = renderer({ getActiveAlerts: async () => ({ events: [] }),
    getAlertEvents: (params, signal) => new Promise((resolve) => requests.push({ params, signal, resolve })),
  });
  const root = element("div"), handle = api.mountAlertsPage(root, {});
  const initial = handle.refresh({ reason: "initial" });
  requests[0].resolve({ events: [] }); await initial;
  await handle.refresh({ reason: "scheduled" });
  assert.equal(requests.length, 1, "confirmed empty is not mistaken for not-yet-loaded");
  const select = all(root).find((node) => node.id === "cp-alerts-state-filter");
  select.value = "resolved"; select.listeners.change();
  await handle.refresh({ reason: "scheduled" });
  assert.equal(requests.length, 2);
  assert.equal(requests[1].signal.aborted, false, "scheduled update does not restart the user's filter request");
  requests[1].resolve({ events: [event()] });
  for (let i = 0; i < 6; i++) await Promise.resolve();
  const explicit = handle.refresh();
  assert.equal(requests[2].params.state, "resolved");
  assert.equal(requests[2].params.before, undefined);
  requests[2].resolve({ events: [] }); await explicit;
  assert.match(text(root), /새로고침/);
  assert.match(text(root), /활성 알림이 갱신되어도 이력은 그대로 유지됩니다/);
  handle.teardown();
  setLang("en");
});

test("Activity host links have stable occurrence IDs while storage spans use non-link styles", () => {
  const first = timeline.buildAlertTimeline([event({ id: "event / #한글" })]);
  const second = timeline.buildAlertTimeline([event({ id: "event / #한글", hostname: "updated hostname" })]);
  const firstLink = all(first).find((node) => node.tagName === "a");
  assert.equal(firstLink.id, `cp-activity-host-${encodeURIComponent("event / #한글")}`);
  assert.equal(all(second).find((node) => node.tagName === "a").id, firstLink.id);
  const storage = timeline.buildAlertTimeline([event({ metric: "storage_usage_pct" })]);
  const span = classes(storage, "cp-activity-host")[0];
  assert.equal(span.tagName, "span");
  assert.equal(span.attributes.href, undefined);
  assert.equal(span.attributes.id, undefined);
  const css = readFileSync(new URL("../src/pages/alerts.css", import.meta.url), "utf8");
  const dateRule = css.match(/\.cp-activity-date\s*\{([^}]+)\}/)[1];
  assert.doesNotMatch(dateRule, /sticky|top-0|z-10/);
  assert.match(dateRule, /surface-raised/);
  for (const cls of ["cp-activity-host", "cp-alerts-active-host"]) {
    const base = css.match(new RegExp(`\\.${cls}\\s*\\{([^}]+)\\}`))[1];
    assert.match(base, /text-\[rgb\(var\(--muted\)\)\]/);
    assert.doesNotMatch(base, /--link|hover:underline/);
    const link = css.match(new RegExp(`a\\.${cls}\\s*\\{([^}]+)\\}`))[1];
    assert.match(link, /--link/);
    assert.match(link, /hover:underline/);
  }
});
