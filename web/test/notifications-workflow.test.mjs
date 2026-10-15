// Notification workflow only. Real DOM builders and API request decisions,
// a local DOM fake, and mock fetch — no providers, hub, browser, or dependencies.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { setCatalogs, setLang } from "../assets/js/core/i18n.js";
import { mountNotificationsSection } from "../assets/js/pages/settings/notifications.js";

// Before integration, apply the approved delta in memory only. After the
// verifier merges catalogs, these tests also work without scratch artifacts.
const delta = await readFile(new URL("../../.verification/round28/notification-ui/locale-delta.json", import.meta.url), "utf8")
  .then(JSON.parse).catch((err) => { if (err.code === "ENOENT") return null; throw err; });
const catalogs = {};
for (const lang of ["en", "ko"]) {
  const base = JSON.parse(await readFile(new URL(`../assets/i18n/${lang}.json`, import.meta.url), "utf8"));
  catalogs[lang] = { ...base, notify: { ...base.notify, ...delta?.[lang].notify }, settings: { ...base.settings, notifications: { ...base.settings.notifications, ...delta?.[lang].settings.notifications } } };
}
setCatalogs(catalogs);

test("notification platform label pairs compact size with its matching leading token", async () => {
  const css = await readFile(new URL("../src/pages/alerts.css", import.meta.url), "utf8");
  const rule = css.match(/\.cp-notify-platform-locked strong\s*\{([^}]*)\}/)?.[1];
  assert.ok(rule);
  assert.match(rule, /text-\[length:var\(--text-cp-sm\)\]/);
  assert.match(rule, /leading-\[length:var\(--leading-cp-sm\)\]/);
});

test("notification idle copy resolves individual literal translation calls before the DOM options", async () => {
  const source = await readFile(new URL("../assets/js/pages/settings/notifications.js", import.meta.url), "utf8");
  const renderIdle = source.match(/function renderIdle\(changed = false\)\s*\{([\s\S]*?)\n  \}/)?.[1];
  assert.ok(renderIdle);
  assert.match(renderIdle, /t\("settings\.notifications\.test_config_changed"\)/);
  assert.match(renderIdle, /t\("settings\.notifications\.draft_test_idle"\)/);
  assert.doesNotMatch(renderIdle, /t\(changed\s*\?/);
  assert.match(renderIdle, /text\s*\}\)\)/);
});

test("two-column notification dialog has room for fields and guide, while tablet row actions stay below metadata", async () => {
  const css = await readFile(new URL("../src/pages/alerts.css", import.meta.url), "utf8");
  assert.match(css, /\.cp-notify-dialog\.cp-dialog-wide\s*\{\s*@apply lg:max-w-5xl;/);
  assert.match(css, /@media \(min-width: 1280px\)\s*\{[\s\S]*?\.cp-notify-card \.cp-channel-row-actions\s*\{[^}]*grid-column: 3/);
  assert.doesNotMatch(css, /@media \(min-width: 768px\)\s*\{[\s\S]*?\.cp-notify-card \.cp-channel-row-actions\s*\{[^}]*grid-column: 3/);
});

test("notification guide copy uses manual receipt checks rather than claiming Send test confirms delivery", () => {
  for (const lang of ["en", "ko"]) {
    for (const key of ["telegram_step_5", "whatsapp_step_6", "webhook_step_4"]) {
      assert.ok(catalogs[lang].notify[key]);
      assert.doesNotMatch(catalogs[lang].notify[key], /Send test to confirm delivery|테스트 발송으로 전달을 확인/);
    }
  }
  assert.match(catalogs.en.notify.telegram_step_5, /does not confirm receipt/);
  assert.match(catalogs.ko.notify.telegram_step_5, /수신이나 읽음 확인이 아닙니다/);
});

function fakeDOM() {
  let document;
  class Node {
    constructor(tag) { this.tagName = tag; this.children = []; this.attributes = {}; this.listeners = {}; this.className = ""; this.value = ""; this.disabled = false; this._text = ""; }
    get firstChild() { return this.children[0] || null; }
    get classList() { return { add: (...classes) => { this.className = [...new Set([...this.className.split(" "), ...classes])].filter(Boolean).join(" "); } }; }
    get textContent() { return this._text + this.children.map((node) => node.textContent).join(""); }
    set textContent(text) { this._text = String(text); this.children = []; }
    get isConnected() { return this === document.body || Boolean(this.parent?.isConnected); }
    append(...nodes) { for (const node of nodes) { node.remove?.(); node.parent = this; this.children.push(node); } }
    removeChild(node) { this.children.splice(this.children.indexOf(node), 1); node.parent = null; }
    remove() { this.parent?.removeChild(this); }
    setAttribute(key, value) { this.attributes[key] = String(value); if (key === "id") this.id = String(value); if (key === "value") this.value = String(value); }
    getAttribute(key) { return this.attributes[key] ?? null; }
    removeAttribute(key) { delete this.attributes[key]; }
    addEventListener(type, listener) { (this.listeners[type] ||= []).push(listener); }
    async emit(type, options = {}) {
      const event = { target: this, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, ...options };
      await Promise.all((this.listeners[type] || []).map((listener) => listener(event)));
      return event;
    }
    focus() { document.activeElement = this; }
    click() { if (!this.disabled) return this.emit("click"); }
    showModal() { this.open = true; this.querySelector("input, select")?.focus(); }
    close() { this.open = false; return this.emit("close"); }
    querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
    querySelectorAll(selector) { return descendants(this).filter((node) => selector.split(",").some((part) => matches(node, part.trim()))); }
  }
  const matches = (node, selector) => selector[0] === "#" ? node.id === selector.slice(1) : selector[0] === "." ? node.className.split(" ").includes(selector.slice(1)) : node.tagName === selector;
  document = { body: new Node("body"), activeElement: null, createElement: (tag) => new Node(tag), createElementNS: (_ns, tag) => new Node(tag), createTextNode: (text) => { const node = new Node("#text"); node.textContent = text; return node; }, getElementById: (id) => document.body.querySelector(`#${id}`) };
  return { document, Node };
}
function descendants(root) { return root.children.flatMap((node) => [node, ...descendants(node)]); }
function response(body, status = 200) { return { ok: status < 400, status, headers: { get: () => null }, json: async () => body }; }
function deferred() { let resolve; const promise = new Promise((r) => { resolve = r; }); return { promise, resolve }; }
const telegram = { id: 7, name: "Ops Telegram", type: "telegram", enabled: true, config: { bot_token: "***", chat_id: "-100100", message_thread_id: "4" } };
const whatsapp = { id: 9, name: "Ops WhatsApp", type: "whatsapp", enabled: false, config: { access_token: "***", phone_number_id: "123456", to: "15551234567" } };
const discord = { id: 11, name: "Ops Discord", type: "discord", enabled: true, config: { webhook_url: "***" } };

async function harness({ channels = [telegram], lang = "en", handler, health, healthError = false } = {}, run) {
  const keys = ["document", "HTMLElement", "fetch", "localStorage", "location", "history"];
  const previous = new Map(keys.map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  const { document, Node } = fakeDOM();
  const calls = [], announcements = [], writes = [];
  globalThis.document = document;
  globalThis.HTMLElement = Node;
  globalThis.location = { hash: "#/settings/notifications" };
  globalThis.history = { replaceState(_state, _unused, hash) { globalThis.location.hash = hash; } };
  const storage = new Map();
  globalThis.localStorage = { getItem: (key) => storage.get(key) ?? null, setItem: (key, value) => { writes.push([key, value]); storage.set(key, value); }, removeItem: (key) => storage.delete(key) };
  setLang(lang);
  globalThis.fetch = async (path, init = {}) => {
    const call = { path, method: init.method || "GET", body: init.body ? JSON.parse(init.body) : null, signal: init.signal };
    calls.push(call);
    if (call.method === "GET" && path === "/api/v1/alerts/channels") return response(channels);
    if (call.method === "GET" && path === "/api/v1/alerts/channels/health") {
      if (healthError) throw new Error("mock health fetch failed");
      return response(health || { channels: [{ channel_id: channels[0]?.id, state: "failing", stats_30d: { last_failure_at: 1_700_000_000 }, last_synthetic: { outcome: "failed", started_at: 1_700_000_000 } }] });
    }
    if (!handler) throw new Error("Unexpected request in mock-only test");
    return handler(call);
  };
  const root = document.createElement("main"); document.body.append(root);
  const handle = mountNotificationsSection(root, { announce: (text) => announcements.push(text) });
  const byClass = (scope, cls) => scope.querySelector(`.${cls}`);
  const buttons = (scope) => scope.querySelectorAll("button");
  const openEdit = async (name = channels[0]?.name) => {
    await buttons(root).find((button) => button.getAttribute("aria-label") === catalogs[lang].settings.notifications.edit_channel_aria.replace("{name}", name)).click();
    return document.body.querySelector("dialog");
  };
  const openAdd = async () => { await byClass(root, "cp-notify-header").querySelector("button").click(); return document.body.querySelector("dialog"); };
  const getTest = (dialog) => byClass(dialog, "cp-notify-test-head").querySelector("button");
  const getSave = (dialog) => buttons(byClass(dialog, "cp-dialog-actions"))[1];
  const input = async (dialog, id, value) => { const node = dialog.querySelector(`#${id}`); node.value = value; await node.emit("input"); return node; };
  try {
    await handle.refresh();
    await run({ root, document, calls, announcements, writes, handle, byClass, buttons, openEdit, openAdd, getTest, getSave, input });
  } finally {
    handle.teardown();
    for (const [key, descriptor] of previous) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key]; }
    setLang("en");
  }
}

test("edited Telegram tests the current destination with existing ID and omitted credential, never saved-test heuristic", async () => {
  await harness({ handler: () => response({ ok: true, delivery: "unsafe server copy that must not be rendered" }) }, async (ui) => {
    const dialog = await ui.openEdit();
    assert.equal(dialog.querySelector("#cp-channel-type"), null);
    assert.equal(dialog.querySelector("#cp-channel-field-bot_token").value, "");
    await ui.input(dialog, "cp-channel-field-chat_id", "-100999");
    await ui.input(dialog, "cp-channel-field-message_thread_id", "8");
    await ui.getTest(dialog).click();
    assert.deepEqual(ui.calls.at(-1).body, { name: telegram.name, type: "telegram", config: { chat_id: "-100999", message_thread_id: "8" }, existing_channel_id: 7 });
    assert.equal(ui.calls.at(-1).path, "/api/v1/alerts/channels/test");
    assert.match(dialog.textContent, /does not verify/);
    assert.doesNotMatch(dialog.textContent, /unsafe server copy/);
    assert.equal(ui.writes.filter(([key]) => key.includes("notify-checklist")).length, 0);
  });
});

test("unchanged edited Discord preserves stored URL without exposing it or failing local URL validation", async () => {
  const sensitiveChannel = { ...discord, config: { webhook_url: "https://discord.com/api/webhooks/111/NEVER-EXPOSE-STORED" } };
  await harness({ channels: [sensitiveChannel], handler: () => response({ ok: true }) }, async (ui) => {
    const dialog = await ui.openEdit();
    const field = dialog.querySelector("#cp-channel-field-webhook_url");
    assert.equal(field.value, "");
    assert.equal(field.getAttribute("value"), null);
    assert.doesNotMatch(ui.document.body.textContent, /NEVER-EXPOSE-STORED/);
    await ui.getTest(dialog).click();
    assert.deepEqual(ui.calls.at(-1).body.config, {});
    assert.equal(ui.calls.at(-1).body.existing_channel_id, 11);
    assert.match(dialog.textContent, /does not read the message back/);
  });
});

test("WhatsApp replacement credential and new recipient survive the draft request; clearing replacement restores omission", async () => {
  await harness({ channels: [whatsapp], handler: () => response({ ok: true }) }, async (ui) => {
    const dialog = await ui.openEdit();
    const password = await ui.input(dialog, "cp-channel-field-access_token", "explicit-fake-replacement");
    await ui.input(dialog, "cp-channel-field-to", "15557654321");
    await ui.getTest(dialog).click();
    assert.deepEqual(ui.calls.at(-1).body, { name: whatsapp.name, type: "whatsapp", config: { access_token: "explicit-fake-replacement", phone_number_id: "123456", to: "15557654321" }, existing_channel_id: 9 });
    assert.equal(password.getAttribute("value"), null);
    assert.match(dialog.textContent, /not delivery confirmation/);
    await ui.input(dialog, "cp-channel-field-access_token", "");
    await ui.getTest(dialog).click();
    assert.equal("access_token" in ui.calls.at(-1).body.config, false);
  });
});

test("test validates name and required configuration before making any request, with focus and clear-on-input", async () => {
  await harness({ channels: [] }, async (ui) => {
    const dialog = await ui.openAdd();
    const type = dialog.querySelector("#cp-channel-type"); type.value = "telegram"; await type.emit("change");
    await ui.input(dialog, "cp-channel-field-bot_token", "123456:FAKE-ONLY");
    await ui.input(dialog, "cp-channel-field-chat_id", "-100999");
    const name = await ui.input(dialog, "cp-channel-name", "   ");
    const before = ui.calls.length;
    await ui.getTest(dialog).click();
    assert.equal(ui.calls.length, before);
    assert.equal(name.getAttribute("aria-invalid"), "true");
    assert.equal(ui.document.activeElement, name);
    assert.match(dialog.textContent, /Name is required/);
    await ui.input(dialog, "cp-channel-name", "Draft");
    assert.equal(name.getAttribute("aria-invalid"), null);
    await ui.input(dialog, "cp-channel-field-chat_id", "");
    await ui.getTest(dialog).click();
    assert.equal(ui.calls.length, before);
    assert.equal(ui.document.activeElement.id, "cp-channel-field-chat_id");
    await ui.input(dialog, "cp-channel-field-chat_id", "-100999");
    await ui.input(dialog, "cp-channel-name", "가".repeat(68));
    await ui.getTest(dialog).click();
    assert.equal(ui.calls.length, before);
    assert.match(dialog.textContent, /name is too long/);
  });
});

test("a typed *** is rejected as a credential, not sent to any endpoint", async () => {
  await harness({}, async (ui) => {
    const dialog = await ui.openEdit(); await ui.input(dialog, "cp-channel-field-bot_token", "***");
    const before = ui.calls.length; await ui.getTest(dialog).click();
    assert.equal(ui.calls.length, before); assert.match(dialog.textContent, /not a credential/);
  });
});

test("input invalidation aborts old request; stale success cannot turn a new draft green or unlock its newer request", async () => {
  const first = deferred(), second = deferred(); let count = 0;
  await harness({ handler: () => ++count === 1 ? first.promise : second.promise }, async (ui) => {
    const dialog = await ui.openEdit(), testBtn = ui.getTest(dialog), saveBtn = ui.getSave(dialog);
    const pendingFirst = testBtn.click();
    assert.equal(testBtn.disabled, true); assert.equal(saveBtn.disabled, true);
    const oldSignal = ui.calls.at(-1).signal;
    await ui.input(dialog, "cp-channel-field-chat_id", "-100222");
    assert.equal(oldSignal.aborted, true); assert.equal(testBtn.disabled, false);
    assert.match(dialog.textContent, /Configuration changed/);
    const pendingSecond = testBtn.click();
    first.resolve(response({ ok: true })); await pendingFirst;
    assert.equal(testBtn.disabled, true); assert.equal(saveBtn.disabled, true);
    assert.equal(ui.byClass(dialog, "cp-notify-test-accepted"), null);
    second.resolve(response({ ok: false, diagnosis: { code: "telegram_chat_not_found", detail: "chat not found" } })); await pendingSecond;
    assert.equal(testBtn.disabled, false); assert.equal(saveBtn.disabled, false);
    assert.ok(ui.byClass(dialog, "cp-notify-test-failed"));
    assert.equal(ui.byClass(dialog, "cp-notify-test-accepted"), null);
    assert.equal(ui.document.activeElement.id, "cp-channel-field-chat_id");
    assert.deepEqual(ui.announcements, ["Test could not be sent"]);
  });
});

test("a second click while sending does not create a concurrent duplicate request", async () => {
  const wait = deferred();
  await harness({ handler: () => wait.promise }, async (ui) => {
    const dialog = await ui.openEdit(), button = ui.getTest(dialog);
    const pending = button.click();
    await button.emit("click");
    assert.equal(ui.calls.filter((call) => call.method === "POST").length, 1);
    assert.match(button.textContent, /Sending/);
    wait.resolve(response({ ok: true })); await pending;
    assert.equal(button.disabled, false);
    assert.equal(ui.byClass(dialog, "cp-channel-dialog-test-result").getAttribute("aria-busy"), "false");
  });
});

test("name, enabled, destination, optional field, and secret edits clear a previous result", async () => {
  await harness({ handler: () => response({ ok: true }) }, async (ui) => {
    const dialog = await ui.openEdit();
    for (const [id, value] of [["cp-channel-name", "Renamed"], ["cp-channel-field-chat_id", "-100888"], ["cp-channel-field-message_thread_id", "9"], ["cp-channel-field-bot_token", "123456:FAKE-TOKEN"]]) {
      await ui.getTest(dialog).click(); assert.ok(ui.byClass(dialog, "cp-notify-test-accepted"));
      await ui.input(dialog, id, value); assert.equal(ui.byClass(dialog, "cp-notify-test-accepted"), null);
    }
    await ui.getTest(dialog).click();
    const enabled = dialog.querySelectorAll("input").find((node) => node.getAttribute("type") === "checkbox");
    enabled.checked = false; await enabled.emit("change");
    assert.equal(ui.byClass(dialog, "cp-notify-test-accepted"), null);
  });
});

test("changing add platform clears credentials/result and blocks a late result from the old platform", async () => {
  const wait = deferred();
  await harness({ channels: [], handler: () => wait.promise }, async (ui) => {
    const dialog = await ui.openAdd();
    await ui.input(dialog, "cp-channel-field-webhook_url", "https://discord.com/api/webhooks/111/FAKE-ONLY");
    const pending = ui.getTest(dialog).click(); const oldSignal = ui.calls.at(-1).signal;
    assert.equal("existing_channel_id" in ui.calls.at(-1).body, false);
    const picker = dialog.querySelector("#cp-channel-type"); picker.value = "telegram"; await picker.emit("change");
    assert.equal(oldSignal.aborted, true);
    assert.equal(dialog.querySelector("#cp-channel-field-webhook_url"), null);
    assert.equal(dialog.querySelector("#cp-channel-field-bot_token").value, "");
    wait.resolve(response({ ok: true })); await pending;
    assert.equal(ui.byClass(dialog, "cp-notify-test-accepted"), null);
    const before = ui.calls.length; await ui.getTest(dialog).click(); assert.equal(ui.calls.length, before);
  });
});

test("late failure after a name edit cannot replace the cleared result or steal focus", async () => {
  const wait = deferred();
  await harness({ handler: () => wait.promise }, async (ui) => {
    const dialog = await ui.openEdit(); const pending = ui.getTest(dialog).click();
    const name = await ui.input(dialog, "cp-channel-name", "New draft"); name.focus();
    wait.resolve(response({ ok: false, diagnosis: { code: "telegram_unauthorized", detail: "stale failure" } })); await pending;
    assert.equal(ui.byClass(dialog, "cp-notify-test-failed"), null);
    assert.equal(ui.document.activeElement, name);
    assert.deepEqual(ui.announcements, []);
  });
});

test("webhook checkbox changes clear the result and its draft request has no existing ID", async () => {
  await harness({ channels: [], handler: () => response({ ok: true }) }, async (ui) => {
    const dialog = await ui.openAdd(); const picker = dialog.querySelector("#cp-channel-type");
    picker.value = "webhook"; await picker.emit("change");
    await ui.input(dialog, "cp-channel-field-url", "https://example.invalid/mock-only");
    await ui.getTest(dialog).click();
    assert.equal("existing_channel_id" in ui.calls.at(-1).body, false);
    assert.match(dialog.textContent, /beyond the receiving endpoint/);
    const checkbox = dialog.querySelector("#cp-channel-field-include_image"); checkbox.checked = true; await checkbox.emit("change");
    assert.equal(ui.byClass(dialog, "cp-notify-test-accepted"), null);
    await ui.getTest(dialog).click(); assert.equal(ui.calls.at(-1).body.config.include_image, "true");
  });
});

test("plain diagnosis detail is redacted, docs URL is fixed, and editing clears failure highlights", async () => {
  await harness({ handler: () => response({ ok: false, diagnosis: { code: "telegram_unauthorized", title: "FAKE-SECRET", detail: "<img onerror=alert(1)> 123456:FAKE-SECRET https://unsafe.test/credential Bearer sensitive", docs_url: "javascript:alert(1)" } }) }, async (ui) => {
    const dialog = await ui.openEdit(); await ui.input(dialog, "cp-channel-field-bot_token", "123456:FAKE-SECRET");
    await ui.getTest(dialog).click();
    assert.match(dialog.textContent, /<img onerror=alert\(1\)>/);
    assert.doesNotMatch(dialog.textContent, /FAKE-SECRET|unsafe\.test|Bearer sensitive/);
    assert.equal(dialog.querySelector("img"), null);
    assert.equal(dialog.querySelector("a").getAttribute("href"), "https://core.telegram.org/bots/api");
    assert.equal(dialog.querySelector("#cp-channel-field-bot_token").getAttribute("aria-invalid"), "true");
    await ui.input(dialog, "cp-channel-field-chat_id", "-100555");
    assert.equal(ui.byClass(dialog, "cp-notify-test-failed"), null);
    assert.equal(dialog.querySelector("#cp-channel-field-bot_token").getAttribute("aria-invalid"), null);
  });
});

test("400/404/500 and transport failures show safe actionable text and recover the sending state", async () => {
  const scenarios = [
    [400, { error: "NEVER-EXPOSE-SECRET", details: { chat_id: "secret field response" } }, /Check the name/],
    [404, { error: "NEVER-EXPOSE-SECRET" }, /no longer exists/],
    [500, { error: "NEVER-EXPOSE-SECRET" }, /hub’s status/],
    [0, null, /connection and retry/],
  ];
  for (const [status, body, expected] of scenarios) {
    await harness({ handler: () => { if (!status) throw new Error("NEVER-EXPOSE-SECRET https://unsafe.test/token"); return response(body, status); } }, async (ui) => {
      const dialog = await ui.openEdit(); await ui.getTest(dialog).click();
      assert.match(dialog.textContent, expected); assert.doesNotMatch(dialog.textContent, /NEVER-EXPOSE-SECRET|unsafe\.test/);
      assert.equal(ui.getTest(dialog).disabled, false); assert.equal(ui.getSave(dialog).disabled, false);
      if (status === 400) assert.equal(ui.document.activeElement.id, "cp-channel-field-chat_id");
    });
  }
});

test("saved row uses saved-test endpoint and leaves independent health unchanged", async () => {
  await harness({ channels: [whatsapp], handler: () => response({ ok: true }) }, async (ui) => {
    const row = ui.byClass(ui.root, "cp-channel-row");
    const health = ui.byClass(row, "cp-notify-health-summary").textContent;
    const testBtn = ui.byClass(row, "cp-channel-row-actions").querySelector("button");
    await testBtn.click();
    assert.equal(ui.calls.at(-1).path, "/api/v1/alerts/channels/9/test"); assert.equal(ui.calls.at(-1).body, null);
    assert.equal(ui.byClass(row, "cp-notify-health-summary").textContent, health);
    assert.match(row.textContent, /15551234567/); assert.match(row.textContent, /Disabled/);
    assert.match(row.textContent, /not delivery confirmation/);
    assert.equal(ui.calls.filter((call) => call.path.includes("synthetic-test") || call.method === "PUT").length, 0);
  });
});

test("health evidence labels stay source-specific and keyboard tab switching preserves focus", async () => {
  await harness({ handler: () => response({ ok: true }) }, async (ui) => {
    const tab = ui.document.getElementById("cp-notify-tab-health"); await tab.click();
    assert.match(ui.root.textContent, /Only Discord synthetic readback can be verified/);
    assert.match(ui.root.textContent, /Latest synthetic test:/);
    assert.equal(ui.document.activeElement.id, "cp-notify-tab-health");
    assert.equal(ui.document.getElementById("cp-notify-panel").getAttribute("aria-labelledby"), "cp-notify-tab-health");
    await tab.emit("keydown", { key: "Home" });
    assert.equal(globalThis.location.hash, "#/settings/notifications");
    assert.equal(ui.document.activeElement.id, "cp-notify-tab-channels");
  });
});

test("failed health loading is shown as unavailable, not healthy or no activity", async () => {
  await harness({ healthError: true }, async (ui) => {
    const summary = ui.byClass(ui.root, "cp-notify-health-summary");
    assert.match(summary.textContent, /Recorded health: Unavailable/);
    assert.match(summary.textContent, /could not be loaded/);
    assert.doesNotMatch(summary.textContent, /No alert activity|Healthy/);
    assert.ok(ui.byClass(ui.root, "cp-channel-row-actions").querySelector("button"));
  });
});

test("saved row sending locks competing actions, then displays failure without rewriting health", async () => {
  const wait = deferred();
  await harness({ handler: () => wait.promise }, async (ui) => {
    const row = ui.byClass(ui.root, "cp-channel-row"), buttons = ui.buttons(ui.byClass(row, "cp-channel-row-actions"));
    const health = ui.byClass(row, "cp-notify-health-summary").textContent;
    const pending = buttons[0].click();
    assert.ok(buttons.every((button) => button.disabled));
    assert.match(row.textContent, /Sending with the saved configuration/);
    wait.resolve(response({ ok: false, diagnosis: { code: "timeout" } })); await pending;
    assert.ok(buttons.every((button) => !button.disabled));
    assert.match(row.textContent, /may have received the message/);
    assert.equal(ui.byClass(row, "cp-notify-health-summary").textContent, health);
  });
});

test("adding a channel uses native submit and create endpoint without existing-channel reference", async () => {
  await harness({ channels: [], handler: () => response({ id: 1 }) }, async (ui) => {
    const dialog = await ui.openAdd();
    await ui.input(dialog, "cp-channel-field-webhook_url", "https://discord.com/api/webhooks/111/FAKE-ONLY");
    await dialog.querySelector("form").emit("submit");
    const create = ui.calls.find((call) => call.method === "POST");
    assert.equal(create.path, "/api/v1/alerts/channels");
    assert.equal("existing_channel_id" in create.body, false);
    assert.equal(create.body.name, "Discord");
    assert.equal(dialog.isConnected, false);
    assert.match(ui.announcements[0], /Added channel Discord/);
  });
});

test("native form submit saves edited destination with omitted stored secret; test and save are independent", async () => {
  const wait = deferred();
  await harness({ handler: (call) => call.method === "PUT" ? wait.promise : response({ ok: true }) }, async (ui) => {
    const dialog = await ui.openEdit(); await ui.input(dialog, "cp-channel-field-chat_id", "-100777");
    const form = dialog.querySelector("form"); const pending = form.emit("submit");
    assert.equal(ui.calls.at(-1).method, "PUT"); assert.equal(ui.calls.at(-1).path, "/api/v1/alerts/channels/7");
    assert.deepEqual(ui.calls.at(-1).body.config, { chat_id: "-100777", message_thread_id: "4" });
    assert.equal(ui.getTest(dialog).disabled, true); assert.equal(ui.getSave(dialog).disabled, true);
    assert.ok(form.querySelectorAll("input, select").every((node) => node.disabled));
    const escape = await dialog.emit("cancel"); assert.equal(escape.defaultPrevented, true);
    await form.emit("submit"); assert.equal(ui.calls.filter((call) => call.method === "PUT").length, 1);
    wait.resolve(response({ id: 7 })); await pending;
    assert.equal(dialog.isConnected, false); assert.match(ui.announcements[0], /Saved channel/);
  });
});

test("closing dialog or section teardown aborts pending test and suppresses late success/announcement", async () => {
  for (const action of ["close", "teardown"]) {
    const wait = deferred();
    await harness({ handler: () => wait.promise }, async (ui) => {
      const dialog = await ui.openEdit(); const pending = ui.getTest(dialog).click(); const signal = ui.calls.at(-1).signal;
      if (action === "close") await ui.buttons(ui.byClass(dialog, "cp-dialog-actions"))[0].click(); else ui.handle.teardown();
      assert.equal(signal.aborted, true); assert.equal(dialog.isConnected, false);
      wait.resolve(response({ ok: true })); await pending;
      assert.deepEqual(ui.announcements, []);
    });
  }
});

test("Korean workflow has natural accepted-not-delivered copy, labels, and matching delta placeholders", async () => {
  const en = (delta || catalogs).en.settings.notifications, ko = (delta || catalogs).ko.settings.notifications;
  assert.deepEqual(Object.keys(en).sort(), Object.keys(ko).sort());
  for (const key of Object.keys(en)) assert.deepEqual(en[key].match(/\{\w+\}/g), ko[key].match(/\{\w+\}/g), key);
  await harness({ lang: "ko", channels: [whatsapp], handler: () => response({ ok: true }) }, async (ui) => {
    const dialog = await ui.openEdit(); await ui.getTest(dialog).click();
    assert.match(dialog.textContent, /실제 수신이 확인된 것은 아닙니다/);
    assert.doesNotMatch(dialog.textContent, /settings\.notifications\./);
    assert.equal(dialog.querySelector("#cp-channel-field-access_token").getAttribute("type"), "password");
    assert.equal(dialog.querySelector("form").getAttribute("aria-label"), "채널 설정");
    assert.equal(ui.byClass(dialog, "cp-channel-dialog-test-result").getAttribute("aria-live"), "polite");
  });
});
