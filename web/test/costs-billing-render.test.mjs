// Real page renderers + real API wrapper, with a small local DOM/fetch harness.
// No browser/server dependency. Pending Round28 copy is overlaid in memory only;
// once integrated, clean checkouts exercise the merged catalogs directly.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { setCatalogs, setLang, t } from "../assets/js/core/i18n.js";
import { formatUSD, formatDisplayAmount } from "../assets/js/core/currency.js";
import { mountCostsPage } from "../assets/js/pages/costs.js";
import { mountBillingSection } from "../assets/js/pages/settings/billing.js";

const catalogs = Object.fromEntries(["en", "ko"].map((lang) => [lang, JSON.parse(readFileSync(new URL(`../assets/i18n/${lang}.json`, import.meta.url), "utf8"))]));
const deltaURL = new URL("../../.verification/round28/billing-ui/locale-delta.json", import.meta.url);
if (existsSync(deltaURL)) {
  const delta = JSON.parse(readFileSync(deltaURL, "utf8"));
  for (const lang of ["en", "ko"]) {
    for (const namespace of ["costs", "billing"]) Object.assign(catalogs[lang][namespace], delta[lang][namespace]);
  }
}
setCatalogs(catalogs);

class TestNode {
  constructor(tagName, document) {
    this.tagName = tagName;
    this.ownerDocument = document;
    this.children = [];
    this.attributes = {};
    this.className = "";
    this.events = {};
    this.value = "";
    this.classList = { add: (...names) => { this.className += ` ${names.join(" ")}`; } };
  }
  get textContent() { return (this.text || "") + this.children.map((child) => child.textContent).join(""); }
  set textContent(value) { this.text = String(value); this.children = []; }
  get firstChild() { return this.children[0] || null; }
  get isConnected() { return this === this.ownerDocument.body || Boolean(this.parentNode?.isConnected); }
  append(...children) { for (const child of children) { child.parentNode = this; this.children.push(child); } }
  removeChild(child) { this.children.splice(this.children.indexOf(child), 1); child.parentNode = null; }
  setAttribute(name, value) { this.attributes[name] = String(value); if (name === "value") this.value = String(value); }
  removeAttribute(name) { delete this.attributes[name]; }
  getAttribute(name) { return this.attributes[name] ?? null; }
  set id(value) { this.setAttribute("id", value); }
  get id() { return this.getAttribute("id"); }
  addEventListener(type, handler) { (this.events[type] ||= []).push(handler); }
  async emit(type) {
    const event = { prevented: false, preventDefault() { this.prevented = true; } };
    for (const handler of this.events[type] || []) await handler(event);
    return event;
  }
  focus() { this.ownerDocument.activeElement = this; }
  showModal() { this.open = true; }
}

function walk(node) { return [node, ...node.children.flatMap(walk)]; }
function byClass(node, name) { return walk(node).filter((child) => child.className.split(/\s+/).includes(name)); }
function byTag(node, name) { return walk(node).filter((child) => child.tagName === name); }
function assertHeadingTargets(root) {
  for (const node of walk(root).filter((node) => node.getAttribute("aria-labelledby"))) {
    for (const id of node.getAttribute("aria-labelledby").split(" ")) {
      const matches = walk(root).filter((child) => child.id === id);
      assert.equal(matches.length, 1, `exactly one real target for ${id}`);
      assert.match(matches[0].tagName, /^h[123]$/);
      assert.ok(matches[0].textContent);
    }
  }
}

async function withHarness(fn, language = "en") {
  const previous = { document: globalThis.document, localStorage: globalThis.localStorage, fetch: globalThis.fetch };
  const document = {
    createElement(tag) { return new TestNode(tag, this); },
    createElementNS(_namespace, tag) { return this.createElement(tag); },
    createTextNode(text) { const node = this.createElement("#text"); node.textContent = text; return node; },
    getElementById(id) { return walk(this.body).find((node) => node.id === id); },
  };
  document.body = document.createElement("body");
  const container = document.createElement("main");
  document.body.append(container);
  const store = new Map([["cp_session", "unit-test-session"]]);
  globalThis.document = document;
  globalThis.localStorage = { getItem: (key) => store.get(key) ?? null, setItem: (key, value) => store.set(key, String(value)), removeItem: (key) => store.delete(key) };
  setLang(language);
  const requests = [];
  const responses = new Map();
  globalThis.fetch = async (path, init = {}) => {
    requests.push({ path, ...init });
    const response = responses.get(`${init.method || "GET"} ${path}`);
    assert.notEqual(response, undefined, `unexpected request ${init.method || "GET"} ${path}`);
    return typeof response === "function" ? response(init) : Response.json(response);
  };
  try { await fn({ document, container, requests, responses, store }); }
  finally {
    // The audit card loads independently of the settings refresh. Let its
    // real Response.json() promise settle before restoring the test DOM.
    await new Promise(setImmediate);
    Object.assign(globalThis, previous);
  }
}

const snapshot = (overrides = {}) => ({ provider: "aws", status: "ok", currency: "USD", mtd_cost: 12.34, forecast_cost: 56.78, last_success_at: Math.floor(Date.now() / 1000), account_level: false, stale: false, ...overrides });
const host = (overrides = {}) => ({ host_id: "resource/one", hostname: "서울-프로덕션-아주긴호스트이름-<literal>", provider: "aws", matched: true, cloud_mtd: 10.01, cloud_forecast: 21.02, network_estimate: { plan_id: 3, mtd: 1.23, projected: 4.56 }, total_mtd: 11.24, total_forecast: 25.58, ...overrides });
function billingResponses(responses, billing = {}) {
  responses.set("GET /api/v1/billing", { interval_seconds: 43200, snapshots: [snapshot()], hosts: [], ...billing });
  responses.set("GET /api/v1/billing/plans", []);
  responses.set("GET /api/v1/settings/billing/currency", { currency: "KRW", krw_per_usd: 1385.5, rate_updated_at: 1780128000 });
  responses.set("GET /api/v1/audit?limit=20", { entries: [], has_more: false });
}

test("Costs renderer has valid headings, explicit amount labels and exact USD charges/projections", async () => {
  await withHarness(async ({ container, responses, requests }) => {
    const h = host();
    responses.set("GET /api/v1/billing", { snapshots: [snapshot()], hosts: [h], display_currency: { currency: "USD" }, interval_seconds: 43200 });
    const handle = mountCostsPage(container, {});
    await handle.refresh();
    assertHeadingTargets(container);
    assert.equal(byTag(container, "h1").length, 1);
    const cards = byClass(container, "cp-costs-provider-card");
    assert.equal(byClass(cards[0], "cp-costs-provider-value")[0].textContent, formatUSD(12.34, "USD", { taxNote: false }));
    assert.equal(byTag(cards[0], "dt")[0].textContent, t("costs.mtd_actual"));
    const table = byClass(container, "cp-costs-table")[0];
    assert.equal(table.getAttribute("role"), "table");
    const row = byTag(table, "tbody")[0].children[0];
    assert.equal(row.children[0].getAttribute("scope"), "row");
    assert.equal(byTag(row, "a")[0].getAttribute("href"), "#/host/resource%2Fone");
    assert.ok(row.textContent.includes(h.hostname));
    assert.deepEqual(byClass(row, "cp-costs-amount").map((node) => node.textContent), ["$10.01", "$21.02", "$1.23", "$4.56", "$11.24", "$25.58"]);
    assert.deepEqual(byTag(row, "dt").map((node) => node.textContent), Array(3).fill([t("costs.mtd"), t("costs.forecast")]).flat());
    assert.ok(table.textContent.includes(t("costs.col_network_estimate")));
    assert.ok(container.textContent.includes(t("costs.all_amounts_excl_tax")));
    assert.ok(!row.textContent.includes("$10.01 /"));
    assert.equal(requests[0].headers.get("Authorization"), "Bearer unit-test-session");
    handle.teardown();
  });
});

test("Costs renderer preserves all KRW primary values, USD originals, saved rate and date in Korean", async () => {
  await withHarness(async ({ container, responses }) => {
    const h = host({ total_mtd: 999.99, total_forecast: 2000.01 }); // deliberately not recomputable from the cells
    const currency = { currency: "KRW", krw_per_usd: 1385.5, rate_updated_at: 1780128000 };
    responses.set("GET /api/v1/billing", { snapshots: [snapshot({ currency: "EUR" })], hosts: [h], display_currency: currency });
    await mountCostsPage(container, {}).refresh();
    const row = byTag(byClass(container, "cp-costs-table")[0], "tbody")[0].children[0];
    const dds = byTag(row, "dd");
    const values = [h.cloud_mtd, h.cloud_forecast, h.network_estimate.mtd, h.network_estimate.projected, h.total_mtd, h.total_forecast];
    assert.deepEqual(dds.map((node) => node.textContent), values.map((value) => formatDisplayAmount(value, currency, { taxNote: false })));
    assert.equal(byClass(row, "cp-costs-conversion").length, 6);
    assert.ok(container.textContent.includes(t("costs.manual_conversion_note")));
    assert.equal(byClass(container, "cp-costs-provider-value")[0].textContent, formatUSD(12.34, "EUR", { taxNote: false }));
    assertHeadingTargets(container);
  }, "ko");
});

test("Costs renderer marks stale/last-known/account-only amounts and preserves API totals without allocation", async () => {
  await withHarness(async ({ container, responses }) => {
    const snap = snapshot({ status: "auth_failed", status_detail: "CLI credentials expired", account_level: true, stale: true, last_success_at: 1 });
    responses.set("GET /api/v1/billing", { snapshots: [snap], hosts: [host({ matched: false, cloud_mtd: 500, cloud_forecast: 900, total_mtd: 501.23, total_forecast: 904.56 })], display_currency: { currency: "USD" }, interval_seconds: 43200 });
    await mountCostsPage(container, {}).refresh();
    assert.ok(container.textContent.includes("CLI credentials expired"));
    assert.ok(container.textContent.includes(t("costs.last_known_note")));
    assert.ok(container.textContent.includes(t("costs.account_total_only")));
    assert.ok(container.textContent.includes(t("costs.account_projection_note")));
    assert.ok(container.textContent.includes(t("costs.stale_data")));
    const cloudCell = byClass(container, "cp-costs-table")[0].children[1].children[0].children[1];
    const sourceNotes = byClass(cloudCell, "cp-costs-source-note");
    assert.deepEqual(sourceNotes.map((node) => node.tagName), ["p", "p"]);
    assert.deepEqual(sourceNotes.map((node) => node.textContent), [t("costs.stale_data"), t("costs.last_known_note")]);
    assert.equal(byClass(container, "cp-costs-provider-value")[0].textContent, formatUSD(12.34, "USD", { taxNote: false }));
    assert.deepEqual(byClass(byClass(container, "cp-costs-total-cell")[0], "cp-costs-amount").map((node) => node.textContent), ["$501.23", "$904.56"]);
  });
});

test("Costs renderer distinguishes null from zero, no plan and no successful collection", async () => {
  await withHarness(async ({ container, responses }) => {
    responses.set("GET /api/v1/billing", { snapshots: [snapshot({ mtd_cost: null, forecast_cost: 0 }), snapshot({ provider: "oci", status: "not_configured", last_success_at: 0 })], hosts: [host({ cloud_mtd: null, cloud_forecast: 0, network_estimate: { plan_id: 0 }, total_mtd: null, total_forecast: 0 })] });
    await mountCostsPage(container, {}).refresh();
    assert.deepEqual(byClass(container, "cp-costs-provider-value").map((node) => node.textContent), [t("costs.unavailable"), formatUSD(0, "USD", { taxNote: false })]);
    assert.equal(byClass(container, "cp-costs-provider-setup").length, 1);
    assert.ok(container.textContent.includes(t("costs.provider_not_setup", { provider: "OCI" })));
    assert.ok(byClass(container, "cp-costs-provider-setup")[0].children.some((node) => node.getAttribute("href") === "#/settings/billing"));
    assert.ok(container.textContent.includes(t("costs.no_plan")));
    assert.deepEqual(byClass(container, "cp-costs-amount").map((node) => node.textContent), [t("costs.unavailable"), "$0.00", t("costs.unavailable"), "$0.00"]);
  });
});

test("Costs loading, error/retry and empty setup states are real, not fabricated measurements", async () => {
  await withHarness(async ({ container, responses }) => {
    let resolve;
    responses.set("GET /api/v1/billing", () => new Promise((done) => { resolve = done; }));
    const handle = mountCostsPage(container, {});
    const pending = handle.refresh();
    assert.equal(byClass(container, "cp-costs-content")[0].getAttribute("aria-busy"), "true");
    assert.ok(container.textContent.includes(t("costs.loading")));
    assert.equal(byClass(container, "cp-costs-amount").length, 0);
    resolve(Response.json({ error: "billing unavailable" }, { status: 503 }));
    await pending;
    assert.ok(walk(container).some((node) => node.getAttribute("role") === "alert"));
    assert.ok(container.textContent.includes("billing unavailable"));
    assert.equal(byClass(container, "cp-error-state").length, 1);
    const retry = byTag(container, "button")[0];
    assert.equal(retry.getAttribute("type"), "button");
    responses.set("GET /api/v1/billing", { snapshots: [], hosts: [] });
    await retry.emit("click");
    assert.ok(container.textContent.includes(t("costs.billing_not_enabled_message")));
    assert.ok(container.textContent.includes(t("costs.no_hosts_reporting_yet")));
    assert.ok(byTag(container, "a").some((node) => node.getAttribute("href") === "#/settings/billing"));
    assertHeadingTargets(container);
  });
});

test("Costs slow responses cannot overwrite a newer refresh or render after teardown", async () => {
  await withHarness(async ({ container, responses }) => {
    let resolve;
    responses.set("GET /api/v1/billing", () => new Promise((done) => { resolve = done; }));
    const handle = mountCostsPage(container, {});
    const old = handle.refresh();
    responses.set("GET /api/v1/billing", { snapshots: [], hosts: [host({ hostname: "newer" })] });
    await handle.refresh();
    resolve(Response.json({ snapshots: [], hosts: [host({ hostname: "older" })] }));
    await old;
    assert.ok(container.textContent.includes("newer"));
    assert.ok(!container.textContent.includes("older"));
    responses.set("GET /api/v1/billing", () => new Promise((done) => { resolve = done; }));
    const pending = handle.refresh();
    handle.teardown();
    resolve(Response.json({ snapshots: [], hosts: [host({ hostname: "aborted" })] }));
    await pending;
    assert.ok(!container.textContent.includes("aborted"));
  });
});

test("Billing native forms retain exact interval/currency selections, hints, tax and provider details", async () => {
  await withHarness(async ({ container, responses, document }) => {
    billingResponses(responses, { snapshots: [snapshot({ status: "permission_denied", status_detail: "Cost Explorer permission missing", stale: true })] });
    await mountBillingSection(container, {}).refresh();
    assertHeadingTargets(container);
    assert.equal(byTag(container, "form").length, 2);
    const interval = document.getElementById("cp-billing-interval");
    const currency = document.getElementById("cp-billing-currency");
    const rate = document.getElementById("cp-billing-krw-rate");
    assert.equal(interval.value, "12h");
    assert.equal(currency.value, "KRW");
    assert.equal(rate.value, "1385.5");
    assert.equal(rate.required, true);
    assert.ok(container.textContent.includes(t("billing.interval_12h")));
    assert.ok(container.textContent.includes("Cost Explorer permission missing"));
    assert.ok(container.textContent.includes(t("billing.provider_setup_note")));
    assert.ok(container.textContent.includes(t("billing.tax_note")));
    for (const control of [interval, currency, rate]) {
      assert.ok(byTag(container, "label").some((label) => label.getAttribute("for") === control.id));
    }
    interval.value = "6h";
    await interval.emit("change");
    assert.equal(document.getElementById("cp-billing-interval-hint").textContent, t("billing.interval_6h"));
    currency.value = "USD";
    await currency.emit("change");
    assert.equal(rate.parentNode.hidden, true);
    assert.equal(rate.required, false);
    assert.equal(rate.value, "1385.5");
  }, "ko");
});

test("Billing submit preserves the currency/rate API payload and visible feedback/focus after reload", async () => {
  await withHarness(async ({ container, responses, document, requests }) => {
    billingResponses(responses);
    let resolve;
    responses.set("PUT /api/v1/settings/billing/currency", () => new Promise((done) => { resolve = done; }));
    const announcements = [];
    await mountBillingSection(container, { announce: (message) => announcements.push(message) }).refresh();
    const form = byTag(container, "form")[1];
    const save = document.getElementById("cp-billing-save-currency");
    const pending = form.emit("submit");
    assert.equal(save.disabled, true);
    assert.equal(document.getElementById("cp-billing-currency-status").textContent, t("billing.saving"));
    assert.deepEqual(JSON.parse(requests.find((request) => request.method === "PUT").body), { currency: "KRW", krw_per_usd: 1385.5 });
    resolve(Response.json({ currency: "KRW", krw_per_usd: 1385.5 }));
    assert.equal((await pending).prevented, true);
    assert.equal(document.getElementById("cp-billing-currency-status").textContent, t("billing.saved"));
    assert.equal(document.activeElement.id, "cp-billing-save-currency");
    assert.ok(announcements.includes(t("billing.currency_set_to", { currency: "KRW" })));
    assert.equal(document.getElementById("cp-billing-currency-status").getAttribute("aria-live"), "polite");
    assert.equal(document.getElementById("cp-billing-save-currency").getAttribute("type"), "submit");
  });
});

test("Billing rejects an invalid manual rate locally and honestly reports API permission errors", async () => {
  await withHarness(async ({ container, responses, document, requests }) => {
    billingResponses(responses);
    responses.set("PUT /api/v1/settings/billing/currency", () => Response.json({ error: "Admin access required" }, { status: 403 }));
    await mountBillingSection(container, {}).refresh();
    const rate = document.getElementById("cp-billing-krw-rate");
    const form = byTag(container, "form")[1];
    rate.value = "0";
    await form.emit("submit");
    assert.equal(requests.filter((request) => request.method === "PUT").length, 0);
    assert.equal(rate.getAttribute("aria-invalid"), "true");
    assert.equal(document.activeElement, rate);
    assert.equal(document.getElementById("cp-billing-currency-status").textContent, t("billing.enter_positive_rate"));
    rate.value = "1500.25";
    await rate.emit("input");
    assert.equal(rate.getAttribute("aria-invalid"), null);
    await form.emit("submit");
    assert.ok(document.getElementById("cp-billing-currency-status").textContent.includes("Admin access required"));
    assert.equal(document.getElementById("cp-billing-save-currency").disabled, false);
    assert.equal(rate.value, "1500.25");
    responses.set("PUT /api/v1/settings/billing/currency", () => Response.json({ error: "Rate required", code: "rate_required" }, { status: 400 }));
    await form.emit("submit");
    assert.equal(document.getElementById("cp-billing-currency-status").textContent, t("billing.krw_requires_positive_rate"));
    assert.equal(rate.getAttribute("aria-invalid"), "true");
    assert.equal(document.activeElement, rate);
  });
});

test("Costs preserves negative API charges, unallocated account totals and no-plan warning without adding tax", async () => {
  await withHarness(async ({ container, responses }) => {
    responses.set("GET /api/v1/billing", { snapshots: [snapshot({ mtd_cost: -3.21, forecast_cost: 0 })], hosts: [host({ provider: "", matched: false, cloud_mtd: 0, cloud_forecast: 0, network_estimate: { plan_id: 0 }, total_mtd: -5, total_forecast: 0 })] });
    await mountCostsPage(container, {}).refresh();
    assert.equal(byClass(container, "cp-costs-provider-value")[0].textContent, formatUSD(-3.21, "USD", { taxNote: false }));
    assert.deepEqual(byClass(container, "cp-costs-amount").map((node) => node.textContent), ["$-5.00", "$0.00"]);
    assert.ok(container.textContent.includes(t("costs.incomplete_total_note")));
    assert.ok(container.textContent.includes(t("costs.all_amounts_excl_tax")));
  });
});

test("Billing interval submit keeps API semantics and durable next-poll confirmation", async () => {
  await withHarness(async ({ container, responses, document, requests }) => {
    billingResponses(responses);
    responses.set("PUT /api/v1/settings/billing/interval", { interval: "6h" });
    await mountBillingSection(container, {}).refresh();
    document.getElementById("cp-billing-interval").value = "6h";
    responses.set("GET /api/v1/billing", { snapshots: [], hosts: [], interval_seconds: 21600 });
    await byTag(container, "form")[0].emit("submit");
    assert.deepEqual(JSON.parse(requests.find((request) => request.method === "PUT").body), { interval: "6h" });
    assert.equal(document.getElementById("cp-billing-interval").value, "6h");
    assert.equal(document.getElementById("cp-billing-interval-status").textContent, t("billing.saved_takes_effect"));
    assert.equal(document.activeElement.id, "cp-billing-save-interval");
    assert.ok(container.textContent.includes(t("billing.billing_disabled_note")));
  });
});

test("Billing pricing plans keep exact allowances, native labeled actions and clearly hypothetical preview", async () => {
  await withHarness(async ({ container, responses, document }) => {
    billingResponses(responses);
    const name = "서울 네트워크 요금제 <literal>";
    responses.set("GET /api/v1/billing/plans", [{ id: 2, provider: "oci-na-eu", name, builtin: false, egress_free_gb: 100, pool_free_tier: true, egress_tiers: [{ up_to_gb: 0, price_per_gb: 0.02 }], ingress_price_per_gb: 0 }]);
    await mountBillingSection(container, {}).refresh();
    const row = byClass(container, "cp-billing-plan-row")[0];
    assert.ok(row.textContent.includes(name));
    assert.ok(row.textContent.includes(t("billing.free_gb_pooled", { gb: 100 })));
    assert.ok(row.textContent.includes("OCI"));
    assert.ok(byTag(row, "button").some((button) => button.getAttribute("aria-label") === t("billing.delete_plan_aria", { name })));
    const edit = byTag(row, "button").find((button) => button.getAttribute("aria-label") === t("billing.edit_plan_aria", { name }));
    await edit.emit("click");
    const dialog = byTag(document.body, "dialog")[0];
    assertHeadingTargets(document.body);
    assert.equal(document.getElementById("cp-plan-free").value, "100");
    assert.ok(dialog.textContent.includes(t("billing.preview_1tb", { cost: formatUSD(18, undefined, { taxNote: false }) })));
  }, "ko");
});

test("Billing loading/error/retry and empty plans/history do not imply a connected account", async () => {
  await withHarness(async ({ container, responses }) => {
    billingResponses(responses, { snapshots: [] });
    let resolve;
    responses.set("GET /api/v1/settings/billing/currency", () => new Promise((done) => { resolve = done; }));
    const handle = mountBillingSection(container, {});
    const pending = handle.refresh();
    assert.equal(byClass(container, "cp-billing-page")[0].getAttribute("aria-busy"), "true");
    assert.ok(container.textContent.includes(t("billing.loading")));
    resolve(Response.json({ error: "Settings unavailable" }, { status: 503 }));
    await pending;
    assert.ok(container.textContent.includes("Settings unavailable"));
    assert.ok(walk(container).some((node) => node.getAttribute("role") === "alert"));
    responses.set("GET /api/v1/settings/billing/currency", { currency: "USD" });
    await byTag(container, "button")[0].emit("click");
    await new Promise(setImmediate);
    assert.ok(container.textContent.includes(t("billing.plans_empty")));
    assert.ok(container.textContent.includes(t("billing.history_empty")));
    assert.ok(container.textContent.includes(t("billing.billing_disabled_note")));
    handle.teardown();
  });
});
