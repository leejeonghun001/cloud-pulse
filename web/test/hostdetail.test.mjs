import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import * as detail from "../assets/js/pages/hostdetail.js";
import * as components from "../assets/js/ui/components.js";
import * as ports from "../assets/js/core/ports.js";
import * as format from "../assets/js/core/format.js";
import { hostGridCard } from "../assets/js/pages/hostgrid.js";
import { thresholdBarClass } from "../assets/js/pages/hosttable.js";
import { selectField } from "../assets/js/ui/select.js";
import { icon } from "../assets/js/ui/icons.js";
import { runHostDetailRefresh } from "../assets/js/core/refresh.js";
import { setCatalogs, setLang, t } from "../assets/js/core/i18n.js";
import { buildAlertTimeline } from "../assets/js/pages/alerttimeline.js";

const store = new Map();
globalThis.localStorage = { getItem: (key) => store.get(key) ?? null, setItem: (key, value) => store.set(key, String(value)), removeItem: (key) => store.delete(key) };
setCatalogs(Object.fromEntries(["en", "ko"].map((lang) => [lang, JSON.parse(readFileSync(new URL(`../assets/i18n/${lang}.json`, import.meta.url)))])));
function element(tagName) {
  return { tagName, children: [], attributes: {}, className: "", textContent: "", style: {}, listeners: {},
    get firstChild() { return this.children[0]; },
    get classList() { const node = this; return { add(...names) { node.className = [node.className, ...names].filter(Boolean).join(" "); } }; },
    append(...children) { this.children.push(...children); },
    removeChild(child) { this.children.splice(this.children.indexOf(child), 1); },
    setAttribute(key, value) { this.attributes[key] = String(value); },
    addEventListener(key, value) { this.listeners[key] = value; },
    contains(node) { return all(this).includes(node); },
    querySelectorAll(selector) { return all(this).filter((node) => selector === "[id]" && node.attributes?.id); },
  };
}
globalThis.document = { createElement: element, createElementNS: (_, tag) => element(tag), createTextNode: (text) => ({ textContent: text }) };
const all = (node) => [node, ...(node.children || []).flatMap(all)];
const text = (node) => all(node).map((part) => part.textContent || "").join(" ");
const byClass = (node, cls) => all(node).filter((part) => part.className?.split(" ").includes(cls));
const now = 1_700_000_060_000;
function fixture(extra = {}) {
  return { host: { id: "host / #한글", hostname: "server-long-name", provider: "oci", os: "linux", platform: "Ubuntu", platform_version: "24.04", arch: "arm64", cpu_model: "Test CPU", cpu_cores: 4, kernel_version: "6.8", agent_version: "v1.0.0" }, status: "up", last_seen: 1_700_000_000,
    latest: { ts: 1_700_000_000, cpu_percent: 32.5, mem_used_percent: 50, mem_used: 2 ** 30, mem_total: 2 ** 31, disk_used_percent: 80, disk_used: 8 * 2 ** 30, disk_total: 10 * 2 ** 30, net_rx_bps: 1024, net_tx_bps: 2 ** 20, uptime_seconds: 3600, load1: 1.25 }, ...extra };
}
const hostSource = readFileSync(new URL("../assets/js/pages/host.js", import.meta.url), "utf8");
function renderer(overrides = {}) {
  const calls = [];
  const destroyed = [];
  const code = hostSource.replace(/^import[\s\S]*?;\s*$/gm, "").replace(/^export\s+/gm, "");
  const api = runInNewContext(`${code}\n({ mountHostDetailPage, renderHostDetailHeader, renderHostEgressCard, renderHostActivity, renderServicesCard, renderInventoryState, renderDisksTable, renderHostCharts, buildLayoutToggle, renderAgentUpdatePanel });`, {
    ...detail, ...components, ...ports, ...format, t, icon, selectField,
    document: globalThis.document, AbortController, runHostDetailRefresh,
    SERIES_COLORS: Object.fromEntries(["cpu", "mem", "disk", "netRx", "netTx", "diskRead", "diskWrite", "load1"].map((name) => [name, "#0369a1"])),
    createTimeSeriesChart: (container, options) => { const call = { container, options }; calls.push(call); return { destroy() { destroyed.push(call); } }; },
    buildAlertTimeline, getAlertEvents: async () => ({ events: [] }),
    ...overrides,
  });
  return { api, calls, destroyed };
}
function verifyNames(root) {
  for (const node of all(root)) {
    const label = node.attributes?.["aria-labelledby"];
    if (!label) continue;
    const headings = all(root).filter((item) => item.attributes?.id === label);
    assert.equal(headings.length, 1, `${label} names exactly one real heading`);
    assert.ok(headings[0].textContent);
  }
}

test("detail header executes real builders with identity, metadata, real resources and all range controls", () => {
  setLang("en");
  const root = element("div");
  const row = fixture();
  const original = JSON.stringify(row);
  const rangeChanges = [], layouts = [];
  renderer().api.renderHostDetailHeader(root, row, now, { range: "6h", layout: "2", onRangeChange: (value) => rangeChanges.push(value), onLayoutChange: (value) => layouts.push(value) });
  assert.equal(all(root).find((node) => node.tagName === "h1").attributes["aria-label"], row.host.hostname);
  for (const value of ["OCI", "Online", "Ubuntu 24.04", "arm64", "Test CPU", "4 cores", "6.8", "v1.0.0", "1h", "32.5%", "1.0 GiB / 2.0 GiB", "8.0 GiB / 10.0 GiB", "1.0 KiB/s", "1.0 MiB/s", "Load (1m) 1.25", "1m ago"]) assert.ok(text(root).includes(value), value);
  const select = all(root).find((node) => node.tagName === "select");
  assert.equal(select.attributes.id, "cp-host-range");
  assert.deepEqual(select.children.map((option) => option.attributes.value), ["1h", "6h", "24h", "7d", "30d"]);
  select.value = "24h"; select.listeners.change();
  assert.deepEqual(rangeChanges, ["24h"]);
  all(root).find((node) => node.attributes?.id === "cp-detail-layout-one").listeners.click();
  assert.deepEqual(layouts, ["1"]);
  verifyNames(root);
  assert.equal(JSON.stringify(row), original);
});

test("offline and no-sample detail remain honest, with fixed shared unknown tracks", () => {
  setLang("en");
  const offline = detail.buildDetailResources(fixture({ status: "down" }), now);
  assert.match(text(offline), /Last reported metrics/);
  assert.doesNotMatch(text(offline), /Latest metrics/);
  const noSample = detail.buildDetailResources(fixture({ latest: null }), now);
  assert.match(text(noSample), /No samples yet/);
  assert.equal(byClass(noSample, "cp-bar-fill").length, 0);
  assert.equal(byClass(noSample, "cp-detail-empty-track").length, 3);
  assert.doesNotMatch(text(noSample), /0\.0%|0 B\/s/);
  const css = readFileSync(new URL("../src/pages/host.css", import.meta.url), "utf8");
  assert.match(css, /\.cp-detail-empty-track\s*\{\s*flex: none;\s*height: var\(--size-cp-track\)/);
  const partial = detail.buildDetailResources(fixture({ latest: { cpu_percent: 0, net_rx_bps: 0, mem_used_percent: null } }), now);
  assert.match(text(partial), /0\.0%/);
  assert.match(text(partial), /0 B\/s/);
  assert.equal(byClass(partial, "cp-bar-fill").length, 1);
});

test("real grid and detail resource renderers share the existing 65/90 thresholds, never resource exceeded", () => {
  setLang("en");
  for (const value of [0, 64.9, 65, 79.9, 80, 89.9, 90, 100, 120]) {
    const row = fixture();
    for (const key of ["cpu_percent", "mem_used_percent", "disk_used_percent"]) row.latest[key] = value;
    const grid = hostGridCard(row, now);
    const resources = detail.buildDetailResources(row, now);
    for (const key of ["cpu", "memory", "disk"]) {
      const gridTile = all(grid).find((node) => node.attributes?.["data-resource"] === key);
      const detailTile = all(resources).find((node) => node.attributes?.["data-detail-resource"] === key);
      for (const tile of [gridTile, detailTile]) {
        const fill = byClass(tile, "cp-bar-fill")[0];
        assert.ok(fill.className.split(" ").includes(thresholdBarClass(value)), `${key} at ${value}%`);
        assert.ok(!fill.className.includes("cp-bar-exceeded"));
        assert.equal(fill.style.width, `${Math.min(value, 100)}%`);
        const bar = all(tile).find((node) => node.attributes?.role === "progressbar");
        assert.equal(bar.attributes["aria-valuetext"], format.formatPercent(value));
      }
    }
  }
});

test("Korean unknown metadata and hostile names are rendered as text, never fabricated cores", () => {
  setLang("ko");
  const row = fixture({ host: { id: "x", hostname: "긴호스트이름-<img onerror=alert(1)>" }, latest: null, status: "unknown", last_seen: 0 });
  const identity = detail.buildDetailIdentity(row, now);
  assert.match(text(identity.top), /미확인/);
  const heading = all(identity.top).find((node) => node.tagName === "h1");
  assert.equal(heading.children.filter((node) => !node.tagName).map((node) => node.textContent).join(""), row.host.hostname);
  assert.equal(heading.attributes["aria-label"], row.host.hostname);
  assert.doesNotMatch(text(identity.meta), /0코어/);
  assert.equal(all(identity.top).filter((node) => node.tagName === "img").length, 0);
  setLang("en");
});

test("traffic keeps reported exceeded amounts/projections, hub override, unlimited and unknown distinct", () => {
  setLang("en");
  const root = element("div");
  renderer().api.renderHostEgressCard(root, "host / #", { tx_bytes: 2048, limit_bytes: 1024, level: "exceeded", limit_source: "hub", projected_tx_bytes: 4096, rx_bytes: 0, rx_limit_bytes: 0, rx_level: "ok" });
  assert.match(text(root), /2\.0 KiB/);
  assert.match(text(root), /Projected: 4\.0 KiB/);
  assert.match(text(root), /hub override/);
  assert.match(text(root), /0 B.*No limit/);
  const bar = all(root).find((node) => node.attributes?.role === "progressbar");
  assert.equal(bar.attributes["aria-valuenow"], "1024");
  assert.equal(bar.attributes["aria-valuetext"], "2.0 KiB / 1.0 KiB");
  assert.equal(all(root).find((node) => node.tagName === "a").attributes.href, "#/settings/traffic?host=host%20%2F%20%23");
  verifyNames(root);
  const unknown = detail.detailTrafficDirection(null, "out");
  assert.equal(byClass(unknown, "cp-bar-fill").length, 0);
  assert.match(text(unknown), /Limit unknown/);
  assert.doesNotMatch(text(unknown), /0 B/);
});

test("six chart renderers pass original arrays and synced cursor without invented summaries", () => {
  setLang("en");
  const { api, calls } = renderer();
  const series = { ts: [1, 2], cpu: [32.5, null], mem: [50, 51], disk: [80, 81], net_rx: [1024, 2048], net_tx: [2048, 4096], disk_read: [0, 1024], disk_write: [0, 2048], load1: [1, 1.25] };
  const root = element("div");
  assert.equal(api.renderHostCharts(root, series).length, 6);
  assert.equal(calls.length, 6);
  for (const call of calls) { assert.equal(call.options.timestamps, series.ts); assert.equal(call.options.syncKey, "cp-host-charts"); }
  const keys = [["cpu"], ["mem"], ["disk"], ["net_rx", "net_tx"], ["disk_read", "disk_write"], ["load1"]];
  calls.forEach((call, index) => keys[index].forEach((key, valueIndex) => assert.equal(call.options.values[valueIndex], series[key])));
  assert.match(text(root), /latest –%/);
  assert.doesNotMatch(text(root), /latest 0\.0%/);
  verifyNames(root);
  const empty = element("div");
  const emptySeries = Object.fromEntries(Object.keys(series).map((key) => [key, []]));
  const previous = calls.length;
  assert.equal(api.renderHostCharts(empty, emptySeries).length, 0);
  assert.equal(calls.length, previous);
  assert.equal(byClass(empty, "cp-detail-chart-empty").length, 6);
  verifyNames(empty);
});

test("services preserve container/Compose/health/ports/process fields and exact remediation", () => {
  setLang("en");
  const { api } = renderer();
  const root = element("div");
  api.renderServicesCard(root, { collected_at: 1_700_000_000, docker: { status: "ok", containers: [{ name: "app", image: "image:v1", state: "running", health: "healthy", status: "Up 1h", compose_project: "project", compose_service: "api", ports: [{ ip: "127.0.0.1", public_port: 8080, private_port: 80, type: "tcp" }] }] }, ports: [{ proto: "tcp", ip: "127.0.0.1", port: 8080, pid: 42, process: "server", container_id: "123456789012abcdef" }] });
  for (const value of ["app", "image:v1", "running", "Healthy", "Up 1h", "project", "api", "8080", "80", "server", "42", "123456789012"]) assert.ok(text(root).includes(value), value);
  assert.ok(byClass(root, "cp-services-port-chip")[0].className.split(" ").includes("cp-tabular"), "published port figures are scoped without styling container text");
  assert.equal(all(root).filter((node) => node.attributes?.role === "region" && node.attributes.tabindex === "0").length, 2);
  verifyNames(root);
  const denied = element("div");
  api.renderServicesCard(denied, { docker: { status: "permission_denied" }, ports: [] });
  assert.match(text(denied), /Docker access denied/);
  assert.ok(text(denied).includes(ports.dockerStatusInfo("permission_denied").fixCommand));
  const missing = element("div");
  api.renderInventoryState(missing, t("host.detail_inventory_pending"));
  assert.match(text(missing), /No inventory has been reported/);
  assert.equal(byClass(missing, "cp-detail-compact-empty").length, 1, "the absent-inventory fallback uses the compact icon treatment");
  assert.equal(all(missing).filter((node) => node.tagName === "table").length, 0);
  verifyNames(missing);
});

test("disks preserve real mounts and unknown cells; request range races and cleanup stay bounded", () => {
  setLang("en");
  const root = element("div");
  renderer().api.renderDisksTable(root, [{ mountpoint: "/data", device: "/dev/test", fstype: "ext4", used_bytes: 1024, total_bytes: 2048, used_percent: 50 }, { mountpoint: "/unknown" }]);
  assert.match(text(root), /\/data.*\/dev\/test.*ext4.*1\.0 KiB.*2\.0 KiB.*50\.0%/);
  const numericCells = byClass(root, "cp-tabular");
  assert.equal(numericCells.length, 6, "only disk measurements opt into tabular figures");
  assert.ok(!numericCells.some((node) => text(node).includes("/data") || text(node).includes("/dev/test") || text(node).includes("ext4")));
  verifyNames(root);
  assert.match(hostSource, /requestedRange !== state\.range/);
  assert.match(hostSource, /controller\.abort\(\)/);
  assert.match(hostSource, /window\.removeEventListener\("cp-theme-change", onThemeChange\)/);
  assert.match(hostSource, /state\.charts\.forEach\(\(c\) => c\.destroy\(\)\)/);
  assert.match(hostSource, /getHostInventory\(hostID, controller\.signal\)/);
  assert.match(hostSource, /copyToClipboard\(built\.command, undefined\)/);
  assert.doesNotMatch(hostSource, /lastOrZero/);
});

test("detail activity and empty inventory sections stay compact and explicit", () => {
  setLang("en");
  const { api } = renderer();
  const activity = element("div");
  api.renderHostActivity(activity, { loading: false, events: [], error: "" });
  assert.match(text(activity), /Activity/);
  assert.ok(text(activity).includes(t("host.activity_empty_title")));
  assert.equal(all(activity).find((node) => node.tagName === "a").attributes.href, "#/alerts");
  const services = element("div");
  api.renderServicesCard(services, { docker: { status: "ok", containers: [] }, ports: [] });
  assert.ok(text(services).includes(t("host.no_services_or_ports_reported")));
  const disks = element("div");
  api.renderDisksTable(disks, []);
  assert.ok(text(disks).includes(t("host.no_disks_reported")));
  verifyNames(activity); verifyNames(services); verifyNames(disks);
});

test("detail breadcrumb has its own accessible label and marks the current host", () => {
  assert.match(hostSource, /aria-label": t\("host\.breadcrumb_label"\)/);
  assert.match(hostSource, /cp-detail-breadcrumb-current", attrs: \{ "aria-current": "page" \}/);
});

test("host activity retains its last result during refreshes and shows refresh errors above it", async () => {
  setLang("en");
  const activityRequests = [];
  const { api } = renderer({
    getHost: async () => fixture(),
    getHostMetrics: async () => ({ ts: [], cpu: [], mem: [], disk: [], net_rx: [], net_tx: [], disk_read: [], disk_write: [], load1: [] }),
    getHostInventory: async () => ({ docker: { status: "ok" }, ports: [] }),
    getAlertEvents: (_params, signal) => new Promise((resolve, reject) => activityRequests.push({ resolve, reject, signal })),
    ApiError: class extends Error {}, getItem: (_key, fallback) => fallback, setItem() {},
    window: { addEventListener() {}, removeEventListener() {} },
  });
  const root = element("div"), handle = api.mountHostDetailPage(root, "test-host", {});
  await handle.refresh();
  assert.equal(activityRequests.length, 1);
  assert.match(text(root), /Loading alert history/);
  const firstEvents = [{ id: 41, state: "firing", rule_name: "retained occurrence", metric: "cpu", value: 95, threshold: 90, started_at: 1_700_000_000, host_id: "test-host", deliveries: [] }];
  activityRequests[0].resolve({ events: firstEvents });
  for (let index = 0; index < 4; index++) await Promise.resolve();
  const priorCard = byClass(root, "cp-detail-activity-card")[0];
  assert.match(text(priorCard), /retained occurrence/);

  await handle.refresh();
  assert.equal(activityRequests.length, 2);
  assert.equal(byClass(root, "cp-detail-activity-card")[0], priorCard, "background refresh leaves activity DOM in place");
  assert.doesNotMatch(text(priorCard), /Loading alert history/);
  activityRequests[1].reject(new Error("activity unavailable"));
  for (let index = 0; index < 4; index++) await Promise.resolve();
  const failedCard = byClass(root, "cp-detail-activity-card")[0];
  assert.match(text(failedCard), /retained occurrence/);
  assert.equal(byClass(failedCard, "cp-error-banner").length, 1);
  assert.ok(all(failedCard).indexOf(byClass(failedCard, "cp-error-banner")[0]) < all(failedCard).indexOf(all(failedCard).find((node) => node.attributes?.["data-event-id"] === "41")));

  const refreshThree = handle.refresh();
  for (let index = 0; index < 3; index++) await Promise.resolve();
  const refreshFour = handle.refresh();
  for (let index = 0; index < 3; index++) await Promise.resolve();
  assert.equal(activityRequests.length, 4);
  activityRequests[3].resolve({ events: [{ ...firstEvents[0], id: 43, rule_name: "newest occurrence" }] });
  activityRequests[2].resolve({ events: [{ ...firstEvents[0], id: 42, rule_name: "stale occurrence" }] });
  await Promise.all([refreshThree, refreshFour]);
  for (let index = 0; index < 4; index++) await Promise.resolve();
  assert.match(text(root), /newest occurrence/);
  assert.doesNotMatch(text(root), /stale occurrence/);
  handle.teardown();
});

test("mounted detail rejects stale ranges, recreates theme charts and aborts/cleans up on teardown", async () => {
  setLang("en");
  const requests = [], events = new Map();
  class ApiError extends Error { constructor(status) { super("test API error"); this.status = status; } }
  const { api, calls, destroyed } = renderer({
    getHost: async () => fixture(),
    getHostMetrics: (_host, range, signal) => new Promise((resolve) => requests.push({ range, signal, resolve })),
    getHostInventory: async () => { throw new ApiError(404); },
    ApiError, getItem: (_key, fallback) => fallback, setItem() {},
    window: { addEventListener: (name, callback) => events.set(name, callback), removeEventListener: (name) => events.delete(name) },
  });
  const series = { ts: [1], cpu: [32.5], mem: [50], disk: [80], net_rx: [1024], net_tx: [2048], disk_read: [0], disk_write: [1024], load1: [1] };
  const root = element("div");
  const handle = api.mountHostDetailPage(root, "test-host", {});
  const refresh = handle.refresh();
  for (let index = 0; index < 6; index++) await Promise.resolve();
  assert.equal(requests[0].range, "1h");
  requests[0].resolve(series);
  await refresh;
  assert.equal(calls.length, 6);
  assert.match(text(root), /No inventory has been reported/);
  const select = all(root).find((node) => node.attributes?.id === "cp-host-range");
  select.value = "6h"; select.listeners.change();
  select.value = "24h"; select.listeners.change();
  assert.deepEqual(requests.map((request) => request.range), ["1h", "6h", "24h"]);
  requests[2].resolve(series);
  for (let index = 0; index < 6; index++) await Promise.resolve();
  assert.equal(calls.length, 12);
  requests[1].resolve(series);
  for (let index = 0; index < 6; index++) await Promise.resolve();
  assert.equal(calls.length, 12, "old range never overwrites the selected range");
  events.get("cp-theme-change")();
  assert.equal(calls.length, 18);
  assert.equal(destroyed.length, 12);
  handle.teardown();
  assert.equal(destroyed.length, 18);
  assert.equal(events.size, 0);
  assert.ok(requests.every((request) => request.signal.aborted));
});

test("mounted metric requests reject A -> B -> A responses/errors and keep the newest generation busy", async () => {
  setLang("en");
  const requests = [];
  const { api, calls } = renderer({
    getHost: async () => fixture(),
    getHostMetrics: (_host, range, signal) => new Promise((resolve, reject) => requests.push({ range, signal, resolve, reject })),
    getHostInventory: async () => ({ docker: { status: "ok" }, ports: [] }),
    ApiError: class extends Error {}, getItem: (_key, fallback) => fallback, setItem() {},
    window: { addEventListener() {}, removeEventListener() {} },
  });
  const sample = (value) => ({ ts: [value], cpu: [value], mem: [value], disk: [value], net_rx: [value], net_tx: [value], disk_read: [value], disk_write: [value], load1: [value] });
  const root = element("div"), handle = api.mountHostDetailPage(root, "test-host", {});
  const first = handle.refresh();
  for (let i = 0; i < 6; i++) await Promise.resolve();
  const select = all(root).find((node) => node.attributes?.id === "cp-host-range");
  select.value = "6h"; select.listeners.change();
  select.value = "1h"; select.listeners.change();
  assert.deepEqual(requests.map((request) => request.range), ["1h", "6h", "1h"]);
  assert.equal(requests[0].signal.aborted, true);
  assert.equal(requests[1].signal.aborted, true);
  assert.equal(requests[2].signal.aborted, false);
  const charts = byClass(root, "cp-charts-grid")[0];
  requests[0].resolve(sample(1));
  requests[1].reject(new Error("stale range failure"));
  await first;
  for (let i = 0; i < 6; i++) await Promise.resolve();
  assert.equal(calls.length, 0, "old A cannot paint just because A is selected again");
  assert.equal(charts.attributes["aria-busy"], "true", "old finally blocks cannot clear the newest loading state");
  assert.equal(byClass(root, "cp-error-banner").length, 0);
  const newest = sample(3);
  requests[2].resolve(newest);
  for (let i = 0; i < 6; i++) await Promise.resolve();
  assert.equal(calls.length, 6);
  assert.equal(calls[0].options.values[0], newest.cpu);
  assert.equal(charts.attributes["aria-busy"], "false");
  assert.match(text(root), /History shown: 1 hour/);

  const pending = handle.refresh();
  for (let i = 0; i < 6; i++) await Promise.resolve();
  handle.teardown();
  assert.equal(requests[3].signal.aborted, true);
  requests[3].resolve(sample(4));
  await pending;
  assert.equal(calls.length, 6, "a late response cannot recreate charts after teardown");
});

test("retained metric history names its actual loaded range after failure and through theme changes in EN/KO", async () => {
  const oldSeries = { ts: [1], cpu: [32.5], mem: [50], disk: [80], net_rx: [1024], net_tx: [2048], disk_read: [0], disk_write: [1024], load1: [1] };
  for (const lang of ["en", "ko"]) {
    setLang(lang);
    const requests = [], events = new Map();
    const { api, calls, destroyed } = renderer({
      getHost: async () => fixture(),
      getHostMetrics: (_host, range, signal) => new Promise((resolve, reject) => requests.push({ range, signal, resolve, reject })),
      getHostInventory: async () => ({ docker: { status: "ok" }, ports: [] }),
      ApiError: class extends Error {}, getItem: (_key, fallback) => fallback, setItem() {},
      window: { addEventListener: (name, callback) => events.set(name, callback), removeEventListener: (name) => events.delete(name) },
    });
    const root = element("div"), handle = api.mountHostDetailPage(root, "test-host", {});
    const initial = handle.refresh();
    for (let i = 0; i < 6; i++) await Promise.resolve();
    requests[0].resolve(oldSeries); await initial;
    const rangeText = () => byClass(root, "cp-detail-history-range")[0].textContent;
    const notice = () => byClass(root, "cp-detail-history-notice")[0].textContent;
    assert.equal(rangeText(), t("host.detail_history_loaded_range", { range: t("host.range_1h") }));
    const select = all(root).find((node) => node.attributes?.id === "cp-host-range");
    select.value = "6h"; select.listeners.change();
    const ranges = { requested: t("host.range_6h"), loaded: t("host.range_1h") };
    assert.equal(notice(), t("host.detail_history_loading_retained", ranges));
    requests[1].reject(new Error("unavailable"));
    for (let i = 0; i < 6; i++) await Promise.resolve();
    assert.equal(notice(), t("host.detail_history_error_retained", ranges));
    assert.equal(calls.length, 6);
    assert.equal(destroyed.length, 0, "failed request retains actual old charts");
    events.get("cp-theme-change")();
    assert.equal(calls[6].options.values[0], oldSeries.cpu);
    assert.equal(rangeText(), t("host.detail_history_loaded_range", { range: t("host.range_1h") }));
    assert.equal(notice(), t("host.detail_history_error_retained", ranges));

    select.value = "24h"; select.listeners.change();
    const newSeries = { ...oldSeries, ts: [2], cpu: [42] };
    requests[2].resolve(newSeries);
    for (let i = 0; i < 6; i++) await Promise.resolve();
    assert.equal(rangeText(), t("host.detail_history_loaded_range", { range: t("host.range_24h") }));
    assert.equal(notice(), "");
    assert.equal(calls[12].options.values[0], newSeries.cpu);
    handle.teardown();
  }
  setLang("en");
});

test("update jump preserves route and the existing panel copies the actual command", async () => {
  setLang("en");
  const update = { available: true, latest: "v2.0.0", command: "safe agent update command", self_update: true };
  const identity = detail.buildDetailIdentity(fixture({ update }), now);
  const button = all(identity.top).find((node) => node.attributes?.id === "cp-detail-update-jump");
  assert.equal(button.tagName, "button");
  assert.equal(button.attributes.type, "button");
  assert.equal(button.attributes.href, undefined, "in-page navigation must not replace the SPA hash");
  const actions = [];
  globalThis.document.getElementById = (id) => {
    assert.equal(id, "cp-detail-agent-update");
    return { scrollIntoView: () => actions.push("scroll"), querySelector: () => ({ focus: () => actions.push("focus") }) };
  };
  button.listeners.click();
  assert.deepEqual(actions, ["scroll", "focus"]);
  delete globalThis.document.getElementById;
  const root = element("div"), copied = [], announced = [];
  renderer().api.renderAgentUpdatePanel(root, update, (message) => announced.push(message), async (command) => copied.push(command));
  await all(root).find((node) => node.tagName === "button").listeners.click();
  assert.deepEqual(copied, [update.command]);
  assert.deepEqual(announced, [t("host.agent_update_command_copied")]);
});

test("initial metrics and inventory errors are disclosed, not fabricated empty results or perpetual loading", async () => {
  setLang("en");
  const { api } = renderer({
    getHost: async () => fixture(), getHostMetrics: async () => { throw new Error("metrics unavailable"); },
    getHostInventory: async () => { throw new Error("inventory unavailable"); },
    ApiError: class extends Error {},
    getItem: (_key, fallback) => fallback, setItem() {},
    window: { addEventListener() {}, removeEventListener() {} },
  });
  const root = element("div"), handle = api.mountHostDetailPage(root, "test-host", {});
  await handle.refresh();
  assert.match(text(root), /Inventory is unavailable/);
  assert.match(text(root), /Unable to refresh metric history/);
  assert.doesNotMatch(text(root), /No inventory has been reported|Loading metric history/);
  assert.equal(all(root).filter((node) => node.tagName === "table").length, 0);
  handle.teardown();
});

test("hostname optional separator breaks preserve exact text and accessible name", () => {
  for (const name of ["demo-aws-web-01", "verify-grid-long-hostname.eu_west-1", "긴한국어-호스트.example", "unbrokenidentifier"]) {
    const heading = detail.detailHostname(name);
    assert.equal(heading.attributes["aria-label"], name);
    assert.equal(heading.children.filter((node) => !node.tagName).map((node) => node.textContent).join(""), name);
    const breaks = heading.children.filter((node) => node.tagName === "wbr");
    assert.equal(breaks.length, (name.match(/[.\-_]/gu) || []).length);
    assert.ok(breaks.every((node) => node.attributes["aria-hidden"] === "true"));
  }
  const css = readFileSync(new URL("../src/pages/host.css", import.meta.url), "utf8");
  assert.match(css, /@media \(max-width: 639px\)\s*\{[\s\S]*?\.cp-host-detail-title-group\s*\{[^}]*flex-col[^}]*flex-basis: 100%/);
  assert.match(css, /\.cp-detail-title-copy \.cp-page-title\s*\{[^}]*overflow-wrap: anywhere/);
});

test("inventory short state chips stay intact without disabling table text wrapping", () => {
  const css = readFileSync(new URL("../src/pages/host.css", import.meta.url), "utf8");
  assert.match(css, /\.cp-detail-meta-processor\s*\{\s*@apply xl:col-span-2;/);
  assert.match(css, /\.cp-detail-services \.cp-table td \.cp-chip\s*\{\s*white-space: nowrap;\s*overflow-wrap: normal;/);
  assert.match(css, /\.cp-detail-services \.cp-table td\s*\{[^}]*overflow-wrap: anywhere/);
  const root = element("div");
  renderer().api.renderServicesCard(root, { docker: { status: "ok", containers: [{ name: "app", state: "running", ports: [] }, { name: "finished", state: "exited", ports: [] }] }, ports: [] });
  const chips = byClass(root, "cp-chip");
  assert.ok(chips.some((node) => node.textContent === "running"));
  assert.ok(chips.some((node) => node.textContent === "exited"));
  assert.ok(all(root).some((node) => node.attributes?.role === "region" && node.attributes.tabindex === "0"));
});

test("inventory short health/state/protocol columns remain intact while long cells can wrap", () => {
  setLang("en");
  const root = element("div");
  renderer().api.renderServicesCard(root, { docker: { status: "ok", containers: [{ name: "long-container-name", image: "long-image-path:v1", state: "running", health: "healthy", status: "Up for a long duration", ports: [] }, { name: "starting", state: "exited", health: "starting", ports: [] }] }, ports: [{ proto: "tcp", ip: "127.0.0.1", port: 8080 }] });
  const shortCells = byClass(root, "cp-detail-short-cell");
  for (const label of ["Healthy", "Starting", "TCP"]) assert.ok(shortCells.some((node) => node.textContent === label), label);
  for (const value of ["long-image-path:v1", "Up for a long duration"]) assert.ok(all(root).some((node) => node.textContent === value && !node.className?.includes("cp-detail-short-cell")));
  const css = readFileSync(new URL("../src/pages/host.css", import.meta.url), "utf8");
  assert.match(css, /\.cp-detail-services \.cp-table \.cp-detail-short-cell,\s*\.cp-detail-services \.cp-table th\s*\{\s*white-space: nowrap;\s*overflow-wrap: normal;/);
  assert.match(css, /\.cp-detail-services \.cp-detail-container-table\s*\{\s*min-width: 56rem;/);
  assert.match(css, /\.cp-detail-services \.cp-table td\s*\{[^}]*overflow-wrap: anywhere/);
});
