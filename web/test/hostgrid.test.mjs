import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { el } from "../assets/js/ui/components.js";
import { hostGridCard, gridAccentLevel, gridEgressRow, gridStatus, measured } from "../assets/js/pages/hostgrid.js";
import { setCatalogs, setLang } from "../assets/js/core/i18n.js";

const storage = new Map();
globalThis.localStorage = { getItem: (key) => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, String(value)), removeItem: (key) => storage.delete(key) };
const catalogs = Object.fromEntries(["en", "ko"].map((lang) => [lang, JSON.parse(readFileSync(new URL(`../assets/i18n/${lang}.json`, import.meta.url)))]));
setCatalogs(catalogs);
function element(tagName) {
  return { tagName, children: [], attributes: {}, className: "", textContent: "", style: {},
    append(...children) { this.children.push(...children); },
    setAttribute(key, value) { this.attributes[key] = String(value); },
    addEventListener() {},
  };
}
globalThis.document = { createElement: element, createElementNS: (_, tag) => element(tag), createTextNode: (text) => ({ textContent: text }) };
const all = (node) => [node, ...(node.children || []).flatMap(all)];
const text = (node) => all(node).map((part) => part.textContent || "").join(" ");
const byClass = (node, name) => all(node).filter((part) => part.className?.split(" ").includes(name));
const byData = (node, key, value) => all(node).find((part) => part.attributes?.[`data-${key}`] === value);
const now = 1_700_000_060_000;
function fixture(overrides = {}) {
  return {
    host: { id: "server / #한글", hostname: "host-long-name", provider: "aws", os: "linux", arch: "arm64", agent_version: "v1.0.0" },
    status: "up", last_seen: 1_700_000_000,
    latest: { ts: 1_700_000_000, cpu_percent: 32.5, mem_used_percent: 50, mem_used: 2 ** 30, mem_total: 2 ** 31, disk_used_percent: 80, disk_used: 8 * 2 ** 30, disk_total: 10 * 2 ** 30, net_rx_bps: 1024, net_tx_bps: 2 ** 20, load1: 1.25, uptime_seconds: 3600 },
    egress: { tx_bytes: 3 * 2 ** 30, limit_bytes: 10 * 2 ** 30, level: "ok", limit_source: "hub", rx_bytes: 2 ** 30, rx_limit_bytes: 0, rx_level: "ok" },
    ...overrides,
  };
}

test("grid card preserves actual metrics, IEC units, limits, metadata and one native detail link", () => {
  setLang("en");
  const row = fixture();
  const before = JSON.stringify(row);
  const card = hostGridCard(row, now);
  assert.equal(card.tagName, "a");
  assert.equal(card.attributes.href, `#/host/${encodeURIComponent(row.host.id)}`);
  assert.match(card.attributes["aria-label"], /host-long-name.*Online/);
  for (const value of ["32.5%", "50.0%", "80.0%", "1.0 KiB/s", "1.0 MiB/s", "load 1.25", "Uptime 1h", "3.0 GiB", "10.0 GiB", "No limit", "AWS", "arm64", "agent v1.0.0", "1m ago", "hub override"]) assert.ok(text(card).includes(value), value);
  assert.equal(all(card).filter((part) => ["a", "button", "input", "select"].includes(part.tagName)).length, 1);
  assert.equal(all(card).filter((part) => part.attributes?.tabindex).length, 0);
  assert.equal(JSON.stringify(row), before, "never mutate the API response");
});

test("missing samples and partial fields stay unknown, never fabricated zero or healthy fills", () => {
  setLang("en");
  const card = hostGridCard(fixture({ latest: null, egress: null, last_seen: 0, status: "unknown" }), now);
  assert.match(text(card), /No samples yet/);
  assert.match(text(card), /Last seen unknown/);
  assert.equal(byClass(card, "cp-bar-fill").length, 0);
  assert.equal(all(card).filter((part) => part.attributes?.["aria-valuenow"]).length, 0);
  assert.doesNotMatch(text(card), /0\.0%|0 B|0s/);
  const partial = hostGridCard(fixture({ latest: { cpu_percent: 0, net_tx_bps: 0, mem_used_percent: null } }), now);
  assert.match(text(byData(partial, "resource", "cpu")), /0\.0%/);
  assert.equal(byClass(byData(partial, "resource", "memory"), "cp-bar-fill").length, 0);
  assert.match(text(byData(partial, "network", "tx")), /0 B\/s/);
  assert.doesNotMatch(text(byData(partial, "network", "rx")), /0 B/);
  for (const invalid of [null, undefined, "0", NaN, Infinity, -1]) assert.equal(measured(invalid), false);
});

test("offline and unknown status do not claim live measurements or invent offline duration", () => {
  setLang("en");
  const card = hostGridCard(fixture({ status: "down" }), now);
  assert.match(card.className, /cp-card-offline/);
  assert.match(text(card), /Offline.*Last reported metrics/);
  assert.match(text(card), /32\.5%/);
  assert.doesNotMatch(text(card), /Latest metrics|Offline since/);
  assert.equal(gridStatus("unknown").tone, "neutral");
  assert.equal(gridStatus("up").tone, "ok");
});

test("grid accent uses the worst reported condition and keeps unknown values neutral", () => {
  assert.equal(gridAccentLevel(fixture()), "warning", "the reported disk threshold is a warning");
  assert.equal(gridAccentLevel(fixture({ status: "down" })), "exceeded");
  assert.equal(gridAccentLevel(fixture({ egress: { ...fixture().egress, level: "exceeded" } })), "exceeded");
  assert.equal(gridAccentLevel(fixture({ latest: null, egress: { level: "exceeded" } })), "neutral", "partial traffic is not a real exceeded reading");
  assert.equal(gridAccentLevel(fixture({ latest: { cpu_percent: 95 } })), "critical");
  assert.equal(gridAccentLevel(fixture({ latest: null, egress: null, status: "unknown" })), "neutral");
});

test("monthly traffic distinguishes absent, unlimited, exceeded and hub-overridden limits", () => {
  setLang("en");
  const exceeded = gridEgressRow("Outbound", { tx_bytes: 2048, limit_bytes: 1024, level: "exceeded", limit_source: "hub" }, "out");
  assert.match(text(exceeded), /hub override.*Exceeded.*2\.0 KiB.*1\.0 KiB/);
  const bar = all(exceeded).find((part) => part.attributes?.role === "progressbar");
  assert.equal(bar.attributes["aria-valuenow"], "1024");
  assert.equal(bar.attributes["aria-valuetext"], "2.0 KiB / 1.0 KiB");
  assert.equal(byClass(exceeded, "cp-bar-fill")[0].style.width, "100%");
  const unknown = gridEgressRow("Outbound", { tx_bytes: 1024 }, "out");
  assert.match(text(unknown), /Limit unknown/);
  assert.equal(byClass(unknown, "cp-chip").length, 0);
  assert.equal(byClass(unknown, "cp-bar-fill").length, 0);
  const unlimited = gridEgressRow("Inbound", { rx_bytes: 0, rx_limit_bytes: 0, rx_level: "ok" }, "in");
  assert.match(text(unlimited), /0 B.*No limit/);
  assert.equal(byClass(unlimited, "cp-bar-fill").length, 0);
});

test("provider and update distinctions survive; Korean and hostile long names remain plain text", () => {
  setLang("ko");
  const row = fixture({ host: { ...fixture().host, hostname: "긴한국어호스트이름-<img onerror=alert(1)>" , provider: "oci" }, update: { available: true, latest: "v2.0.0", command: "safe update command" } });
  const card = hostGridCard(row, now);
  assert.equal(byClass(card, "cp-host-grid-name")[0].textContent, row.host.hostname);
  assert.match(text(card), /OCI/);
  assert.match(text(card), /온라인/);
  assert.match(text(card), /수신/);
  assert.match(text(card), /월간 트래픽/);
  assert.match(text(card), /상세 보기/);
  assert.equal(all(card).filter((part) => part.tagName === "img").length, 0);
  assert.equal(byClass(card, "cp-host-card-update").length, 1);
  assert.match(byClass(card, "cp-host-card-update")[0].attributes.title, /safe update command/);
  setLang("en");
});

test("update cards announce the update state through their only interactive label", () => {
  setCatalogs({
    en: { ...catalogs.en, overview: { ...catalogs.en.overview, card_open_host_update: "Open {name}, {status}, agent update available" } },
    ko: { ...catalogs.ko, overview: { ...catalogs.ko.overview, card_open_host_update: "{name} 열기, {status}, 에이전트 업데이트 가능" } },
  });
  const row = fixture({ update: { available: true, latest: "v2.0.0" } });
  setLang("en");
  const english = hostGridCard(row, now);
  assert.equal(english.attributes["aria-label"], "Open host-long-name, Online, agent update available");
  const englishChip = byClass(english, "cp-host-card-update")[0];
  assert.equal(englishChip.attributes["aria-hidden"], "true");
  assert.equal(englishChip.attributes["aria-label"], undefined);

  setLang("ko");
  const korean = hostGridCard(row, now);
  assert.equal(korean.attributes["aria-label"], "host-long-name 열기, 온라인, 에이전트 업데이트 가능");
  setLang("en");
});

test("overview retains sorting/filter persistence, keyboard typing and chart lifecycle", () => {
  const source = readFileSync(new URL("../assets/js/pages/overview.js", import.meta.url), "utf8");
  assert.match(source, /filterRows\(state\.hosts, state\.filter\)/);
  assert.match(source, /sortRows\(filtered, state\.sort, columnAccessors\)/);
  assert.match(source, /if \(state\.view === "grid"\) controls\.append\(buildGridSort/);
  assert.match(source, /setJSON\(SORT_STORAGE_KEY, state\.sort\)/);
  assert.match(source, /hasStoredView: storedView === "grid" \|\| storedView === "table"/);
  assert.match(source, /state\.view = state\.hosts\.length <= 12 \? "grid" : "table"/);
  assert.match(source, /summary_hosts_down[\s\S]*down\.length > 0 \? "critical" : "neutral"/);
  assert.match(source, /bannerHost\.hidden = true/, "an empty banner must not create a second page gap before KPIs");
  assert.match(source, /cp-stat-cards-\$\{cards\.length\}[\s\S]*cp-stat-cards-odd/, "KPI count exposes the mobile orphan treatment");
  assert.match(source, /input\?\.focus\(\{ preventScroll: true \}\)/);
  assert.match(source, /input\?\.setSelectionRange\(\.\.\.selection\)/);
  assert.match(source, /link\.getAttribute\("href"\) === focusedCardHref/);
  assert.match(source, /active\.tagName === "SELECT"/);
  assert.match(source, /controller\.abort\(\)/);
  assert.match(source, /globalThis\.addEventListener\?\.\("resize", syncTableOverflowOnResize\)/);
  assert.match(source, /globalThis\.removeEventListener\?\.\("resize", syncTableOverflowOnResize\)/);
  assert.match(source, /resizeFrame = globalThis\.requestAnimationFrame/);
  assert.match(source, /state\.charts\.forEach\(\(c\) => c\.destroy\(\)\)/);
  const css = readFileSync(new URL("../src/pages/overview.css", import.meta.url), "utf8");
  assert.match(css, /min-width: 56\.25rem[\s\S]*repeat\(2, minmax\(0, 1fr\)\)/);
  assert.match(css, /min-width: 75rem[\s\S]*repeat\(3, minmax\(0, 1fr\)\)/);
  assert.doesNotMatch(css, /cp-host-grid-card-(?:exceeded|critical|warning)::before/);
  assert.doesNotMatch(css, /cp-host-grid-card-(?:exceeded|critical|warning) \.cp-host-card-topline/);
  assert.match(css, /\.cp-host-grid-card\s*\{[\s\S]*background-color: rgb\(var\(--surface-raised\)\)/);
  assert.match(css, /\.cp-systems-mobile-card\s*\{[\s\S]*bg-\[rgb\(var\(--surface-raised\)\)\]/);
  assert.match(css, /cp-host-meta-row/);
  assert.match(css, /cp-stat-cards-odd > :last-child[\s\S]*grid-column: 1 \/ -1/);
  assert.match(css, /min-width: 64rem[\s\S]*cp-stat-cards-odd > :last-child[\s\S]*grid-column: auto/);
  assert.match(css, /min-width: 80rem[\s\S]*\.cp-host-meta-row \{ flex-wrap: wrap; gap: 0\.25rem 0; \}[\s\S]*border-inline-start/);
  assert.match(css, /\.cp-host-resource\s*\{[\s\S]*grid-template-columns: 3\.25rem 3\.5rem minmax\(0, 1fr\)/);
  assert.match(css, /\.cp-host-grid-name\s*\{[^}]*overflow-wrap: anywhere/s);
  assert.match(css, /@media \(hover: hover\)/);
  assert.match(css, /\.cp-systems-row:hover td:first-child \{ background-color: rgb\(var\(--muted-surface\)\); \}/);
  assert.match(css, /\.cp-systems-row-offline\s*\{\s*background-color: rgb\(var\(--surface-sunken\)\);\s*\}/);
  assert.match(css, /\.cp-systems-row-offline td:first-child \{ background-color: rgb\(var\(--surface-sunken\)\); \}/);
});

test("All Systems section resolves its accessible name to a real rendered heading", () => {
  setLang("en");
  const source = readFileSync(new URL("../assets/js/pages/overview.js", import.meta.url), "utf8");
  // Execute the real renderer with isolated header-control stubs. el() is
  // the production builder: unsupported top-level id options cannot pass.
  const renderer = source.slice(source.indexOf("function renderSystemsCard("), source.indexOf("function buildViewToggle("));
  const section = el("section", { attrs: { "aria-labelledby": "cp-systems-heading" } });
  runInNewContext(`${renderer}\nrenderSystemsCard(section, state, {});`, {
    el, section, state: { hosts: [], hostsLoaded: true, filter: "", view: "table", visibleColumns: [] },
    t: (key) => key === "overview.all_systems_title" ? "All Systems" : key,
    clearChildren() {}, filterRows: () => [],
    icon: () => element("svg"),
    buildViewToggle: () => element("div"), buildColumnsDropdown: () => element("div"),
  });
  const heading = all(section).find((node) => node.attributes?.id === section.attributes["aria-labelledby"]);
  assert.ok(heading, "aria-labelledby must reference an actual DOM id");
  assert.equal(heading.tagName, "h2");
  assert.ok(heading.className.split(" ").includes("cp-card-title"));
  assert.equal(heading.textContent, "All Systems");
  assert.equal(all(section).filter((node) => node.attributes?.id === "cp-systems-heading").length, 1);
});

test("direct unknown grid tracks keep shared height without flex growth or shrink", () => {
  const css = readFileSync(new URL("../src/pages/overview.css", import.meta.url), "utf8");
  const rule = css.match(/\.cp-host-resource > \.cp-bar-track,\s*\.cp-host-traffic-row > \.cp-bar-track\s*\{([^}]+)\}/);
  assert.ok(rule, "both vertical-stack placeholder callers need the bounded override");
  assert.match(rule[1], /flex:\s*none\s*;/);
  assert.match(rule[1], /height:\s*var\(--size-cp-track\)\s*;/);
  const tokens = readFileSync(new URL("../src/tokens.css", import.meta.url), "utf8");
  assert.match(tokens, /--size-cp-track:\s*0\.375rem\s*;/);
  const unknown = hostGridCard(fixture({ latest: null, egress: null }), now);
  for (const resource of ["cpu", "memory", "disk"]) {
    const tile = byData(unknown, "resource", resource);
    const tracks = tile.children.filter((node) => node.className?.split(" ").includes("cp-bar-track"));
    assert.equal(tracks.length, 1, `${resource} retains one direct unknown track`);
    assert.equal(tracks[0].attributes["aria-hidden"], "true");
    assert.equal(tracks[0].children.length, 0, "unknown tracks must not fabricate fills");
  }
});
