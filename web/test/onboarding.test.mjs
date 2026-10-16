import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import * as components from "../assets/js/ui/components.js";
import * as format from "../assets/js/core/format.js";
import * as hosttable from "../assets/js/pages/hosttable.js";
import * as table from "../assets/js/ui/table.js";
import { hostGridCard } from "../assets/js/pages/hostgrid.js";
import { selectField } from "../assets/js/ui/select.js";
import { buildEgressSection } from "../assets/js/pages/egress.js";
import { buildBucketsSection } from "../assets/js/pages/buckets.js";
import { buildStorageAccountsSection } from "../assets/js/pages/storage-overview.js";
import { icon } from "../assets/js/ui/icons.js";
import { createDialog, buildDialogActions } from "../assets/js/ui/dialog.js";
import { setCatalogs, setLang, t } from "../assets/js/core/i18n.js";
import * as hostfilter from "../assets/js/core/hostfilter.js";

// Exercise integration copy without requiring unmerged locale keys in the
// product catalogs. After merge, the checked-in catalogs are sufficient.
let delta = {};
try { delta = JSON.parse(readFileSync(new URL("../../.verification/round28/onboarding/locale-delta.json", import.meta.url))); } catch {}
const merge = (base, extra) => Object.fromEntries(Object.keys({ ...base, ...extra }).map((key) => [key,
  extra[key] && typeof extra[key] === "object" ? merge(base[key] || {}, extra[key]) : extra[key] ?? base[key],
]));
const catalogs = Object.fromEntries(["en", "ko"].map((lang) => [lang, merge(JSON.parse(readFileSync(new URL(`../assets/i18n/${lang}.json`, import.meta.url))), delta[lang] || {})]));
setCatalogs(catalogs);
const store = new Map();
globalThis.localStorage = { getItem: (key) => store.get(key) ?? null, setItem: (key, value) => store.set(key, String(value)), removeItem: (key) => store.delete(key) };

class Element {
  constructor(tagName) { this.tagName = tagName.toUpperCase(); this.children = []; this.attributes = {}; this.className = ""; this.listeners = {}; this.style = {}; this.rawText = ""; this.value = ""; }
  get textContent() { return this.rawText + this.children.map((child) => child.textContent || "").join(""); }
  set textContent(value) { this.rawText = String(value); this.children = []; }
  get firstChild() { return this.children[0]; }
  get id() { return this.attributes.id || ""; }
  set id(value) { this.attributes.id = value; }
  get hidden() { return "hidden" in this.attributes; }
  set hidden(value) { if (value) this.attributes.hidden = ""; else delete this.attributes.hidden; }
  get disabled() { return "disabled" in this.attributes; }
  set disabled(value) { if (value) this.attributes.disabled = ""; else delete this.attributes.disabled; }
  get classList() { return { add: (...names) => { this.className = [...new Set([...this.className.split(" ").filter(Boolean), ...names])].join(" "); }, toggle: (name, enabled) => { const classes = new Set(this.className.split(" ").filter(Boolean)); if (enabled) classes.add(name); else classes.delete(name); this.className = [...classes].join(" "); } }; }
  get isConnected() { return globalThis.document.body.contains(this); }
  append(...children) { for (const child of children) { child.parent = this; this.children.push(child); } }
  removeChild(child) { this.children.splice(this.children.indexOf(child), 1); child.parent = null; }
  remove() { this.parent?.removeChild(this); }
  setAttribute(key, value) { this.attributes[key] = String(value); }
  getAttribute(key) { return this.attributes[key] ?? null; }
  hasAttribute(key) { return key in this.attributes; }
  removeAttribute(key) { delete this.attributes[key]; }
  addEventListener(key, callback) { (this.listeners[key] ||= []).push(callback); }
  async fire(key, event = {}) { for (const callback of this.listeners[key] || []) await callback(event); }
  contains(node) { return all(this).includes(node); }
  matches(selector) { return selector.split(",").some((part) => {
    const sections = part.trim().split(/\s+/);
    const simple = sections.pop();
    const matches = simple === ":disabled" ? this.disabled : simple.startsWith("#") ? this.id === simple.slice(1) : simple.startsWith("[") ? this.hasAttribute(simple.slice(1, -1)) : (() => {
      const [tag, ...classes] = simple.split(".");
      return (!tag || this.tagName === tag.toUpperCase()) && classes.every((cls) => this.className.split(" ").includes(cls));
    })();
    if (!matches) return false;
    if (!sections.length) return true;
    let parent = this.parent;
    while (parent) { if (parent.matches(sections.join(" "))) return true; parent = parent.parent; }
    return false;
  }); }
  closest(selector) { return this.matches(selector) ? this : this.parent?.closest(selector) || null; }
  querySelectorAll(selector) { return all(this).slice(1).filter((node) => node.matches?.(selector)); }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  focus(options) { this.focusOptions = options; globalThis.document.activeElement = this; }
  select() { this.selected = true; }
  showModal() { this.open = true; }
  close() { this.open = false; for (const callback of this.listeners.close || []) callback(); }
}
const all = (root) => [root, ...(root.children || []).flatMap(all)];
const byClass = (root, cls) => all(root).filter((node) => node.className?.split(" ").includes(cls));
globalThis.HTMLElement = Element;
const documentEvents = new Map();
globalThis.document = {
  createElement: (tag) => new Element(tag), createElementNS: (_, tag) => new Element(tag), createTextNode: (text) => ({ textContent: text }), body: new Element("body"), activeElement: null,
  addEventListener: (event, fn) => { if (!documentEvents.has(event)) documentEvents.set(event, new Set()); documentEvents.get(event).add(fn); },
  removeEventListener: (event, fn) => documentEvents.get(event)?.delete(fn),
};
const overviewSource = readFileSync(new URL("../assets/js/pages/overview.js", import.meta.url), "utf8");
const shellSource = readFileSync(new URL("../assets/js/ui/shell.js", import.meta.url), "utf8");
const settle = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };
function overviewRenderer(overrides = {}) {
  const code = overviewSource.replace(/^import[\s\S]*?;\s*$/gm, "").replace(/^export\s+/gm, "");
  return runInNewContext(`${code}\n({ mountOverviewPage, renderSystemsCard, buildFirstAgentGuide });`, {
    ...components, ...format, ...hosttable, ...table, ...hostfilter, t, icon, hostGridCard, selectField, document: globalThis.document, AbortController,
    currentMonth: () => "2026-10", getStoredDirection: () => "out", setStoredDirection() {},
    getItem: (_key, fallback) => fallback, getJSON: (_key, fallback) => fallback, setJSON() {}, setItem() {},
    createDropdown() {}, dropdownSeparator: () => new Element("div"),
    getEgress: async () => ({ hosts: [] }), getBuckets: async () => ({ buckets: [], collectors: [] }), getStorageAccounts: async () => ({ accounts: [] }), getBilling: async () => null, loadDisplayCurrency: async () => null,
    buildEgressSection: () => new Element("div"), buildBucketsSection: () => ({ node: new Element("div"), charts: [] }), buildStorageAccountsSection: () => new Element("div"),
    outdatedAgentCount: () => 0, ApiError: class extends Error {}, ...overrides,
  });
}
function installRenderer(navigator = {}) {
  const code = shellSource.slice(shellSource.indexOf("export function openAddAgentDialog(")).replace(/^export\s+/gm, "");
  return runInNewContext(`${code}\nopenAddAgentDialog`, { ...components, icon, createDialog, buildDialogActions, t, navigator });
}
function enrollmentRequester(overrides = {}) {
  const source = readFileSync(new URL("../assets/js/main.js", import.meta.url), "utf8");
  const code = source.slice(source.indexOf("let addAgentRequest ="), source.indexOf("// Global update banner"));
  const location = { hash: "#/" };
  const page = {};
  return runInNewContext(`let activePage = page; ${code}\n({ openAddAgent, location, replacePage(next) { activePage = next; } });`, {
    document: globalThis.document, location, page, isAuthenticated: () => true,
    getAgentToken: async () => ({ install_command: commands[0].command, install_commands: commands }),
    openAddAgentDialog: installRenderer(), showToast() {}, t, ...overrides,
  });
}
// Deliberately fake command fixtures; no installation or external delivery.
const commands = [
  { os: "linux", label: "Linux (systemd)", command: "unit-linux-command --hub https://hub.example.com --token TEST-ONLY" },
  { os: "darwin", label: "macOS (launchd)", command: "unit-macos-command --hub https://hub.example.com --token TEST-ONLY" },
  { os: "windows", label: "Windows (service)", command: "unit-windows-command -Hub https://hub.example.com -Token TEST-ONLY" },
];
function latestDialog() { return globalThis.document.body.querySelectorAll("dialog").at(-1); }

test("shared empty, error and skeleton states retain actions, retry focus and accessible loading status", async () => {
  let actions = 0;
  const empty = components.emptyState({ title: "Empty", message: "Nothing here", compact: true, action: { label: "Add", onClick: () => { actions++; }, iconName: "plus" } });
  const action = empty.querySelector("button");
  await action.fire("click");
  assert.equal(actions, 1);
  assert.ok(empty.className.includes("cp-empty-state-compact"));

  let complete;
  const failed = components.errorState({ title: "Failed", message: "Try again", onRetry: () => new Promise((resolve) => { complete = resolve; }) });
  globalThis.document.body.append(failed);
  const retry = failed.querySelector("button");
  const pending = retry.fire("click");
  assert.equal(retry.disabled, true);
  assert.equal(retry.textContent, t("state.retrying"));
  complete(); await pending;
  assert.equal(retry.disabled, false);
  assert.equal(globalThis.document.activeElement, retry);
  failed.remove();

  // refresh() commonly clears the container, detaching the pressed Retry:
  // focus moves to the replacement error state's Retry, not <body>.
  const host = new Element("div");
  globalThis.document.body.append(host);
  const first = components.errorState({ title: "Failed", onRetry: async () => {
    first.remove();
    host.append(components.errorState({ title: "Failed again", onRetry: () => {} }));
  } });
  host.append(first);
  await first.querySelector("button").fire("click");
  const replacement = host.querySelector("button");
  assert.notEqual(replacement, null);
  assert.equal(globalThis.document.activeElement, replacement);
  host.remove();

  for (const state of [components.skeletonLines(2), components.skeletonCards(2), components.skeletonTable(2, 3)]) {
    assert.equal(state.querySelector("[role]").getAttribute("role"), "status");
    assert.equal(state.children[0].getAttribute("aria-hidden"), "true");
  }
});

test("overview mount never confuses loading/error with a confirmed empty fleet or fetches credentials", async () => {
  setLang("en");
  const requests = [], signals = [];
  let opens = 0;
  const api = overviewRenderer({ getHosts: (signal) => { signals.push(signal); return new Promise((resolve, reject) => requests.push({ resolve, reject })); } });
  const root = new Element("div"), handle = api.mountOverviewPage(root, { onAddAgent: () => { opens++; } });
  assert.match(root.textContent, /Loading your hosts/);
  assert.equal(byClass(root, "cp-onboarding").length, 0);
  assert.equal(root.querySelector("#cp-onboarding-add-agent"), null);
  assert.equal(opens, 0);
  const initial = handle.refresh();
  requests[0].reject(new Error("unavailable")); await initial;
  assert.match(root.textContent, /couldn't check your hosts/);
  assert.equal(byClass(root, "cp-error-banner").length, 1);
  assert.equal(byClass(root, "cp-onboarding").length, 0);
  const retry = root.querySelector("#cp-overview-retry").fire("click");
  assert.match(root.textContent, /Loading your hosts/);
  requests[1].resolve({ hosts: [] }); await retry;
  assert.equal(byClass(root, "cp-onboarding").length, 1);
  assert.equal(byClass(root, "cp-error-banner").length, 0);
  assert.match(root.textContent, /0 hosts/);
  assert.equal(byClass(root, "cp-stat-cards")[0].hidden, true);
  assert.equal(byClass(root, "cp-stat-card").length, 0, "an empty fleet does not render zero-valued measured stat cards");
  assert.equal(root.querySelectorAll("pre").length, 0);
  assert.equal(opens, 0, "confirmed empty does not open enrollment automatically");
  await root.querySelector("#cp-onboarding-add-agent").fire("click");
  assert.equal(opens, 1);

  const failedRefresh = handle.refresh(); requests[2].reject(new Error("later unavailable")); await failedRefresh;
  assert.equal(byClass(root, "cp-onboarding").length, 0, "a stale zero-host result must not mask the current API error");
  assert.equal(byClass(root, "cp-error-banner").length, 1);
  handle.teardown(); assert.ok(signals.every((signal) => signal.aborted));
});

test("overview loading and error notices preserve localized text and status semantics in EN/KO", () => {
  const api = overviewRenderer();
  for (const lang of ["en", "ko"]) {
    setLang(lang);
    for (const hostsError of [null, new Error("unavailable")]) {
      const card = new Element("div");
      api.renderSystemsCard(card, { hostsLoaded: false, hostsError, hosts: [] }, {});
      const notice = byClass(card, "cp-overview-load-state")[0];
      const message = notice.querySelector("p");
      assert.equal(message.textContent, hostsError ? t("overview.onboarding_load_error") : t("overview.onboarding_loading"));
      assert.equal(message.getAttribute("role"), hostsError ? null : "status");
      assert.equal(Boolean(card.querySelector("#cp-overview-retry")), Boolean(hostsError));
      assert.equal(byClass(card, "cp-onboarding").length, 0);
    }
  }
  setLang("en");
});

test("confirmed-empty guide has three real steps, a native CTA and safe docs links in EN/KO", async () => {
  const api = overviewRenderer();
  for (const lang of ["en", "ko"]) {
    setLang(lang);
    let resolveOpen, opens = 0;
    const guide = api.buildFirstAgentGuide(() => { opens++; return new Promise((resolve) => { resolveOpen = resolve; }); });
    assert.equal(byClass(guide, "cp-onboarding-step").length, 3);
    const add = guide.querySelector("#cp-onboarding-add-agent");
    assert.equal(add.tagName, "BUTTON");
    assert.equal(add.attributes.type, "button");
    assert.equal(guide.querySelectorAll("pre,code,input,textarea").length, 0);
    const link = guide.querySelector("a");
    assert.ok(link.attributes.href.endsWith("/docs/install.md"));
    assert.equal(link.attributes.rel, "noopener noreferrer");
    const pending = add.fire("click");
    assert.equal(add.disabled, false, "pending native button stays focusable");
    assert.equal(add.getAttribute("aria-disabled"), "true");
    await add.fire("click"); assert.equal(opens, 1);
    resolveOpen(); await pending; assert.equal(add.disabled, false); assert.equal(add.getAttribute("aria-disabled"), null);
    assert.ok(!guide.textContent.includes("overview.onboarding_"));
    assert.ok(guide.textContent.includes(t("overview.onboarding_title")));
  }
  setLang("en");
  assert.equal(api.buildFirstAgentGuide().querySelector("a").attributes.href, "#/settings/agents", "unwired embed still provides a native enrollment link");
  const failure = api.buildFirstAgentGuide(async () => { throw new Error("do not show sensitive error details"); });
  globalThis.document.body.append(failure);
  await failure.querySelector("button").fire("click");
  assert.match(failure.textContent, /couldn't be opened/);
  assert.ok(!failure.textContent.includes("sensitive error"));
  failure.remove();
});

test("rendered overview section builders resolve every heading reference to one actual H2 in EN/KO", () => {
  for (const lang of ["en", "ko"]) {
    setLang(lang);
    const root = new Element("div"); globalThis.document.body.append(root);
    root.append(
      buildEgressSection({ month: "2026-10", direction: "out", hosts: [], onMonthChange() {}, onDirectionChange() {} }),
      buildBucketsSection({ buckets: [], collectors: [], nowMs: Date.now() }).node,
      buildStorageAccountsSection({ accounts: [], nowMs: Date.now() }),
    );
    assert.equal(root.querySelectorAll("section").length, 3);
    for (const section of root.querySelectorAll("section")) {
      const id = section.getAttribute("aria-labelledby");
      const targets = root.querySelectorAll(`#${id}`);
      assert.equal(targets.length, 1, `${lang}: ${id} has exactly one rendered target`);
      assert.equal(targets[0].tagName, "H2");
      assert.ok(targets[0].textContent.length > 0);
      assert.equal(section.contains(targets[0]), true);
    }
    root.remove();
  }
  setLang("en");
});

test("async enrollment retains its native button, suppresses duplicates and restores the explicit opener on dismissal", async () => {
  let resolveToken, calls = 0;
  const requester = enrollmentRequester({ getAgentToken: () => { calls++; return new Promise((resolve) => { resolveToken = resolve; }); } });
  const root = new Element("div"); globalThis.document.body.append(root);
  const api = overviewRenderer({ getHosts: async () => ({ hosts: [] }) });
  const handle = api.mountOverviewPage(root, { onAddAgent: requester.openAddAgent });
  await handle.refresh();
  const button = root.querySelector("#cp-onboarding-add-agent"); button.focus();
  const pending = button.fire("click");
  assert.equal(button.tagName, "BUTTON"); assert.equal(button.disabled, false);
  assert.equal(globalThis.document.activeElement, button);
  await button.fire("click"); assert.equal(calls, 1);
  const other = new Element("button"); globalThis.document.body.append(other); other.focus();
  resolveToken({ install_command: commands[0].command, install_commands: commands }); await pending;
  const dialog = latestDialog();
  assert.equal(dialog.open, true); assert.equal(dialog.querySelector("code").textContent, "");
  assert.ok(!dialog.textContent.includes("TEST-ONLY"));
  assert.equal(button.getAttribute("aria-disabled"), null); assert.equal(button.disabled, false);
  // Unit dismissal uses close(); real native Enter/Escape is the assigned QA matrix.
  dialog.querySelector("#cp-install-reveal").focus(); dialog.close();
  assert.equal(globalThis.document.activeElement, button);
  assert.equal(button.focusOptions.preventScroll, true);
  handle.teardown(); root.remove(); other.remove(); globalThis.document.activeElement = null;
});

test("teardown aborts async enrollment and detached buttons receive no late error or disabled state", async () => {
  for (const fail of [false, true]) {
    let finish, signal;
    const requester = enrollmentRequester({ getAgentToken: (s) => { signal = s; return new Promise((resolve, reject) => { finish = fail ? reject : resolve; }); } });
    const root = new Element("div"); globalThis.document.body.append(root);
    const handle = overviewRenderer({ getHosts: async () => ({ hosts: [] }) }).mountOverviewPage(root, { onAddAgent: requester.openAddAgent });
    await handle.refresh();
    const button = root.querySelector("#cp-onboarding-add-agent"); button.focus();
    const dialogsBefore = globalThis.document.body.querySelectorAll("dialog").length;
    const pending = button.fire("click"); handle.teardown(); root.remove();
    assert.equal(signal.aborted, true);
    finish(fail ? new Error("private failure") : { install_command: commands[0].command }); await pending;
    assert.equal(globalThis.document.body.querySelectorAll("dialog").length, dialogsBefore);
    assert.equal(button.disabled, false); assert.equal(button.getAttribute("aria-disabled"), null);
    assert.ok(!root.textContent.includes("couldn't be opened"));
    globalThis.document.activeElement = null;
  }
});

test("newer enrollment wins without a losing completion changing the newer button's pending state", async () => {
  const requests = [];
  const requester = enrollmentRequester({ getAgentToken: () => new Promise((resolve, reject) => requests.push({ resolve, reject })) });
  const api = overviewRenderer();
  const first = api.buildFirstAgentGuide(requester.openAddAgent), second = api.buildFirstAgentGuide(requester.openAddAgent);
  globalThis.document.body.append(first, second);
  const older = first.querySelector("button"), newer = second.querySelector("button");
  const oldPending = older.fire("click"), newPending = newer.fire("click");
  requests[0].resolve({ install_command: commands[0].command }); await oldPending;
  assert.equal(older.getAttribute("aria-disabled"), null);
  assert.equal(newer.getAttribute("aria-disabled"), "true"); assert.equal(newer.disabled, false);
  requests[1].resolve({ install_command: commands[0].command }); await newPending;
  const dialog = latestDialog(); assert.equal(dialog.open, true);
  assert.equal(newer.getAttribute("aria-disabled"), null);
  dialog.close(); assert.equal(globalThis.document.activeElement, newer);
  first.remove(); second.remove(); globalThis.document.activeElement = null;
});

test("route replacement, A-B-A generation and disconnected openers discard pending enrollment", async () => {
  for (const change of ["route", "generation", "detached"]) {
    let resolve;
    const requester = enrollmentRequester({ getAgentToken: () => new Promise((done) => { resolve = done; }) });
    const trigger = new Element("button"); globalThis.document.body.append(trigger);
    const count = globalThis.document.body.querySelectorAll("dialog").length;
    const pending = requester.openAddAgent(trigger);
    if (change === "route") requester.location.hash = "#/settings";
    if (change === "generation") { requester.replacePage({}); requester.location.hash = "#/settings"; requester.replacePage({}); requester.location.hash = "#/"; }
    if (change === "detached") trigger.remove();
    resolve({ install_command: commands[0].command }); await pending;
    assert.equal(globalThis.document.body.querySelectorAll("dialog").length, count, change);
    trigger.remove();
  }
});

test("Escape while enrollment is still loading cancels late opening without moving focus or leaving listeners", async () => {
  let resolve;
  const requester = enrollmentRequester({ getAgentToken: () => new Promise((done) => { resolve = done; }) });
  const trigger = new Element("button"); globalThis.document.body.append(trigger); trigger.focus();
  const count = globalThis.document.body.querySelectorAll("dialog").length;
  const pending = requester.openAddAgent(trigger);
  for (const listener of documentEvents.get("keydown")) listener({ key: "Escape" });
  resolve({ install_command: commands[0].command }); await pending;
  assert.equal(globalThis.document.body.querySelectorAll("dialog").length, count);
  assert.equal(globalThis.document.activeElement, trigger);
  assert.equal(documentEvents.get("keydown").size, 0);
  trigger.remove(); globalThis.document.activeElement = null;
});

test("compatible dialog focus options never force focus onto detached, disabled, hidden, inert or canceled triggers", () => {
  for (const invalid of ["detached", "disabled", "hidden", "inert", "canceled"]) {
    const trigger = new Element("button"), elsewhere = new Element("button");
    const wrapper = new Element("div"); wrapper.append(trigger); globalThis.document.body.append(wrapper, elsewhere);
    trigger.focus();
    let current = true;
    const { open, dialog } = createDialog({ titleId: "unit-dialog-title", title: "Unit" });
    open({ restoreFocusTo: trigger, shouldRestoreFocus: () => current });
    if (invalid === "detached") trigger.remove();
    if (invalid === "disabled") trigger.disabled = true;
    if (invalid === "hidden") wrapper.hidden = true;
    if (invalid === "inert") wrapper.setAttribute("inert", "");
    if (invalid === "canceled") current = false;
    elsewhere.focus(); dialog.close();
    assert.equal(globalThis.document.activeElement, elsewhere, invalid);
    dialog.remove(); wrapper.remove(); elsewhere.remove();
  }
  globalThis.document.activeElement = null;
});

test("real host arrival replaces onboarding using only API hosts and preserves the selected grid", async () => {
  setLang("en");
  const row = { host: { id: "unit-host", hostname: "reported-host", provider: "other" }, status: "up", latest: null, egress: { tx_bytes: 0, rx_bytes: 0 }, last_seen: 1_700_000_000 };
  let hosts = [];
  const api = overviewRenderer({ getHosts: async () => ({ hosts }), getItem: (key, fallback) => key === hosttable.VIEW_STORAGE_KEY ? "grid" : fallback });
  const root = new Element("div"), handle = api.mountOverviewPage(root, {});
  await handle.refresh();
  assert.equal(byClass(root, "cp-onboarding").length, 1);
  hosts = [row]; await handle.refresh();
  assert.equal(byClass(root, "cp-onboarding").length, 0);
  assert.equal(byClass(root, "cp-host-grid-card").length, 1);
  assert.match(root.textContent, /reported-host/);
  assert.equal(byClass(root, "cp-stat-cards")[0].hidden, false);
  assert.match(root.textContent, /1 host/);
  handle.teardown();
  const main = readFileSync(new URL("../assets/js/main.js", import.meta.url), "utf8");
  assert.match(main, /mountOverviewPage\(container, \{ announce, onAddAgent: openAddAgent \}\)/);
});

test("late overview response cannot show onboarding after teardown", async () => {
  let resolveHosts;
  const api = overviewRenderer({ getHosts: () => new Promise((resolve) => { resolveHosts = resolve; }) });
  const root = new Element("div"), handle = api.mountOverviewPage(root, {});
  const pending = handle.refresh(); handle.teardown(); resolveHosts({ hosts: [] }); await pending;
  assert.equal(byClass(root, "cp-onboarding").length, 0);
});

test("a scheduled zero-host refresh preserves keyboard focus on the onboarding action", async () => {
  setLang("en");
  const api = overviewRenderer({ getHosts: async () => ({ hosts: [] }) });
  const root = new Element("div"), handle = api.mountOverviewPage(root, { onAddAgent() {} });
  await handle.refresh();
  const button = root.querySelector("#cp-onboarding-add-agent");
  button.focus();
  await handle.refresh({ reason: "scheduled" });
  assert.equal(globalThis.document.activeElement.id, button.id);
  assert.equal(globalThis.document.activeElement.focusOptions.preventScroll, true);
  handle.teardown(); globalThis.document.activeElement = null;
});

test("install dialog uses actual commands only after explicit reveal, with platform guidance and keyboard tabs", async () => {
  setLang("en");
  const copied = [];
  const openDialog = installRenderer({ clipboard: { writeText: async (command) => copied.push(command) } });
  const trigger = new Element("button"); globalThis.document.body.append(trigger); trigger.focus();
  openDialog(commands[0].command, commands);
  const dialog = latestDialog();
  const pre = dialog.querySelector("pre"), code = dialog.querySelector("code"), reveal = dialog.querySelector("#cp-install-reveal");
  const buttons = dialog.querySelectorAll('[role]');
  assert.equal(dialog.open, true);
  assert.equal(code.textContent, ""); assert.equal(pre.hidden, true);
  assert.ok(!dialog.textContent.includes("TEST-ONLY"));
  assert.match(dialog.textContent, /sudo access.*systemd service/);
  const copy = byClass(dialog, "cp-install-command-actions")[0].children[1];
  assert.equal(copy.disabled, true); await copy.fire("click"); assert.equal(copied.length, 0);
  await reveal.fire("click");
  assert.equal(code.textContent, commands[0].command);
  assert.equal(reveal.attributes["aria-expanded"], "true");
  assert.equal(globalThis.document.activeElement, copy);
  await copy.fire("click"); assert.equal(copied[0], commands[0].command);
  const tabs = buttons.filter((button) => button.attributes.role === "tab");
  let prevented = false;
  await tabs[0].fire("keydown", { key: "ArrowRight", preventDefault() { prevented = true; } });
  assert.equal(prevented, true);
  assert.equal(globalThis.document.activeElement, tabs[1]);
  assert.equal(tabs[1].attributes.tabindex, "0");
  assert.equal(tabs[0].attributes.tabindex, "-1");
  assert.equal(code.textContent, ""); assert.equal(copy.disabled, true);
  assert.match(dialog.textContent, /administrator access.*launchd service/);
  assert.equal(dialog.querySelector("#cp-install-panel").attributes["aria-labelledby"], tabs[1].id);
  await reveal.fire("click"); await copy.fire("click"); assert.equal(copied[1], commands[1].command);
  await tabs[1].fire("keydown", { key: "End", preventDefault() {} });
  assert.match(dialog.textContent, /PowerShell as Administrator/);
  await reveal.fire("click"); await copy.fire("click"); assert.equal(copied[2], commands[2].command);
  assert.match(dialog.textContent, /Copying the command doesn't connect the host/);
  assert.match(dialog.textContent, /no host is marked connected until it reports/);
  dialog.close();
  assert.equal(code.textContent, "");
  assert.equal(globalThis.document.activeElement, trigger);
  assert.equal(dialog.isConnected, false);
  assert.equal(commands[2].command.includes("TEST-ONLY"), true, "cleanup never mutates the caller's API data");
  trigger.remove();
});

test("single-command fallback keeps its Unix tab label, Linux guidance and actual command in EN/KO", async () => {
  for (const lang of ["en", "ko"]) {
    setLang(lang);
    assert.equal(t("nav.install_platform_unix_label"), "Linux / macOS");
    for (const installCommands of [undefined, []]) {
      installRenderer()(commands[0].command, installCommands);
      const dialog = latestDialog();
      const tabs = dialog.querySelectorAll("[role]").filter((node) => node.attributes.role === "tab");
      assert.equal(tabs.length, 1);
      assert.equal(tabs[0].textContent, "Linux / macOS");
      assert.equal(tabs[0].getAttribute("aria-selected"), "true");
      assert.equal(dialog.querySelector("#cp-install-panel").getAttribute("aria-labelledby"), tabs[0].id);
      assert.equal(byClass(dialog, "cp-install-terminal")[0].textContent, t("nav.install_terminal_linux"));
      assert.equal(dialog.querySelector("code").textContent, "");
      await dialog.querySelector("#cp-install-reveal").fire("click");
      assert.equal(dialog.querySelector("code").textContent, commands[0].command);
      dialog.close();
    }
  }
  setLang("en");
});

test("clipboard denial selects a real manual-copy field; hiding and closing clear credentials", async () => {
  setLang("ko");
  installRenderer({ clipboard: { writeText: async () => { throw new Error("denied"); } } })(commands[0].command);
  const dialog = latestDialog();
  await dialog.querySelector("#cp-install-reveal").fire("click");
  const actions = byClass(dialog, "cp-install-command-actions")[0];
  await actions.children[1].fire("click");
  const fallback = dialog.querySelector("textarea");
  assert.equal(fallback.value, commands[0].command);
  assert.equal(fallback.hidden, false); assert.equal(fallback.selected, true);
  assert.equal(globalThis.document.activeElement, fallback);
  await actions.children[2].fire("click");
  assert.equal(fallback.value, ""); assert.equal(fallback.hidden, true);
  assert.equal(dialog.querySelector("code").textContent, "");
  assert.match(dialog.textContent, /명령을 숨겼습니다/);
  dialog.close(); setLang("en");
});

test("late clipboard errors cannot expose the old platform command or change the current status", async () => {
  let rejectCopy;
  installRenderer({ clipboard: { writeText: () => new Promise((_, reject) => { rejectCopy = reject; }) } })(commands[0].command, commands);
  const dialog = latestDialog();
  await dialog.querySelector("#cp-install-reveal").fire("click");
  const copy = byClass(dialog, "cp-install-command-actions")[0].children[1];
  const pending = copy.fire("click");
  await dialog.querySelector("#cp-install-platform-2").fire("click");
  rejectCopy(new Error("late failure")); await pending;
  assert.equal(dialog.querySelector("code").textContent, "");
  assert.equal(dialog.querySelector("textarea").value, "");
  assert.equal(dialog.querySelector("textarea").hidden, true);
  assert.equal(copy.disabled, true);
  assert.match(dialog.textContent, /PowerShell as Administrator/);
  dialog.close();
});

test("closing during a clipboard request clears the dialog and ignores a late error", async () => {
  let rejectCopy;
  installRenderer({ clipboard: { writeText: () => new Promise((_, reject) => { rejectCopy = reject; }) } })(commands[0].command, commands);
  const dialog = latestDialog();
  await dialog.querySelector("#cp-install-reveal").fire("click");
  const pending = byClass(dialog, "cp-install-command-actions")[0].children[1].fire("click");
  const code = dialog.querySelector("code"), fallback = dialog.querySelector("textarea");
  dialog.close();
  rejectCopy(new Error("late denied")); await pending;
  assert.equal(code.textContent, ""); assert.equal(fallback.value, ""); assert.equal(fallback.hidden, true);
  assert.equal(dialog.isConnected, false);
});

test("missing install command stays disabled and no invented connection state or command is shown", () => {
  installRenderer()("");
  const dialog = latestDialog();
  assert.equal(dialog.querySelector("#cp-install-reveal").disabled, true);
  assert.equal(dialog.querySelector("code").textContent, "");
  assert.match(dialog.textContent, /didn't provide an install command/);
  dialog.close();
});

test("onboarding styles wrap EN/KO labels without changing tokens or adding decorative motion", () => {
  const css = readFileSync(new URL("../src/pages/overview.css", import.meta.url), "utf8");
  assert.match(css, /\.cp-onboarding\s*\{[^}]*grid-cols-1[^}]*lg:grid-cols-2/);
  assert.match(css, /\.cp-onboarding-step p\s*\{[^}]*break-keep[^}]*overflow-wrap: anywhere/);
  assert.match(css, /\.cp-add-agent-dialog \.cp-btn-group\s*\{[^}]*flex-wrap/);
  assert.match(css, /\.cp-install-command-actions \.cp-btn\s*\{[^}]*whitespace-normal/);
  assert.doesNotMatch(css, /@keyframes|animation:/);
});

test("overview spend panel shows per-currency totals, failing providers and a setup pointer", async () => {
  const { spendSummary } = await import("../assets/js/core/billing-view.js");
  const { formatUSD } = await import("../assets/js/core/currency.js");
  const code = overviewSource.replace(/^import[\s\S]*?;\s*$/gm, "").replace(/^export\s+/gm, "");
  const renderSpend = runInNewContext(`${code}\nrenderSpend`, { ...components, t, icon, document: globalThis.document, spendSummary, formatUSD });
  const host = new Element("div");

  renderSpend(host, null);
  assert.equal(host.hidden, true, "billing off on the hub hides the panel");

  renderSpend(host, { snapshots: [{ provider: "aws", status: "not_installed", last_success_at: 0 }] });
  assert.equal(host.hidden, false);
  assert.ok(host.textContent.includes(t("overview.spend_not_connected")));

  renderSpend(host, { snapshots: [
    { provider: "oci", status: "ok", currency: "SGD", mtd_cost: 1.5, forecast_cost: 3, last_success_at: 9, source_host: "box" },
    { provider: "aws", status: "permission_denied", currency: "USD", mtd_cost: 0, forecast_cost: 0, last_success_at: 0 },
  ] });
  assert.ok(host.textContent.includes(formatUSD(1.5, "SGD", { taxNote: false })));
  assert.ok(host.textContent.includes(t("overview.spend_attention", { providers: "AWS" })));
  assert.ok(!host.textContent.includes(t("overview.spend_not_connected")));
});
