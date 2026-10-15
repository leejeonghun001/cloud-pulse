#!/usr/bin/env node
// Isolated browser contract for the existing #/alerts event-history page.
// The runner owns the loopback hub and database fixture; this checker reads
// its real APIs, drives the current controls, and writes only under .verification/.
import {
  existsSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { formatDuration, formatRelativeTimeFromUnixSeconds } from "../web/assets/js/core/format.js";
import { BidiSession, registerActiveSession } from "./verify-phase1-auth.mjs";
import { safeVerificationPath } from "./verify-round28-safety.mjs";

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = realpathSync(resolve(SCRIPT_DIR, ".."));
const VERIFY_ROOT = resolve(REPO_ROOT, ".verification");
const EXPECTED_ROUTE = "#/alerts";
const PAGE_SIZE = 50;
const MATRIX = [
  ["en", "dark", 360, 800], ["en", "dark", 768, 1024], ["en", "dark", 1440, 900],
  ["en", "light", 360, 800], ["en", "light", 768, 1024], ["en", "light", 1440, 900],
  ["ko", "dark", 360, 800], ["ko", "dark", 768, 1024], ["ko", "dark", 1440, 900],
  ["ko", "light", 360, 800], ["ko", "light", 768, 1024], ["ko", "light", 1440, 900],
];
const sleep = (ms) => new Promise((resolveSleep) => setTimeout(resolveSleep, ms));

function usage() {
  process.stdout.write(
    "usage: node scripts/verify-activity-contract.mjs --stage activity --hub http://127.0.0.1:18190 --password-file FILE --fixture-file FILE --ui-token TOKEN --out DIR\n" +
      "Checks the existing Alerts event-history page and its authenticated APIs only.\n",
  );
}

function parseArgs(argv) {
  const options = { stage: "activity", hub: null, passwordFile: null, fixtureFile: null, uiToken: null, out: null };
  const keys = {
    "--stage": "stage", "--hub": "hub", "--password-file": "passwordFile",
    "--fixture-file": "fixtureFile", "--ui-token": "uiToken", "--out": "out",
  };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--help" || arg === "-h") return null;
    if (!Object.hasOwn(keys, arg)) throw new Error(`unknown argument: ${arg}`);
    const value = argv[++i];
    if (!value || value.startsWith("--")) throw new Error(`${arg} requires a value`);
    options[keys[arg]] = value;
  }
  if (options.stage !== "activity") throw new Error("this contract checker only accepts --stage activity");
  for (const key of ["hub", "passwordFile", "fixtureFile", "uiToken", "out"]) {
    if (!options[key]) throw new Error(`--${key === "passwordFile" ? "password-file" : key === "fixtureFile" ? "fixture-file" : key === "uiToken" ? "ui-token" : key} is required`);
  }
  const hub = new URL(options.hub);
  if (hub.protocol !== "http:" || hub.hostname !== "127.0.0.1" || hub.port !== "18190" || hub.pathname !== "/" || hub.username || hub.password || hub.search || hub.hash) {
    throw new Error("refusing hub URL; only http://127.0.0.1:18190/ is accepted");
  }
  options.hub = hub.origin;
  return options;
}

function localPath(path, label) {
  return safeVerificationPath(path, label);
}

function loadCatalog(lang) {
  return JSON.parse(readFileSync(join(REPO_ROOT, "web/assets/i18n", `${lang}.json`), "utf8"));
}

function fill(template, values) {
  return template.replace(/\{([a-zA-Z0-9_]+)\}/g, (_, key) => String(values[key] ?? ""));
}

async function waitFor(session, expression, label, timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  let lastError = null;
  while (Date.now() < deadline) {
    try {
      if (await session.evaluate(expression)) return;
      lastError = null;
    } catch (error) {
      lastError = error;
    }
    await sleep(100);
  }
  throw new Error(`timed out waiting for ${label}${lastError ? ` (${lastError.message})` : ""}`);
}

async function login(session, hub, password) {
  await session.navigate(`${hub}/#/login`);
  await waitFor(session, `Boolean(document.getElementById("cp-login-username") && document.getElementById("cp-login-password"))`, "login form");
  const result = await session.evaluateJSON(`(() => {
    const username = document.getElementById("cp-login-username");
    const passwordInput = document.getElementById("cp-login-password");
    const form = username?.closest("form");
    if (!username || !passwordInput || !form) return {ok:false,reason:"login fields unavailable"};
    username.value = "admin";
    passwordInput.value = ${JSON.stringify(password)};
    username.dispatchEvent(new Event("input", {bubbles:true}));
    passwordInput.dispatchEvent(new Event("input", {bubbles:true}));
    form.requestSubmit();
    return {ok:true};
  })()`);
  if (!result.ok) throw new Error(result.reason);
  await waitFor(session, `Boolean(localStorage.getItem("cp_session"))`, "authenticated admin session");
  await waitFor(session, `location.hash !== "#/login" && Boolean(document.querySelector("main"))`, "authenticated app shell");
}

async function openAlerts(session, hub, lang, theme, width, height) {
  await session.setViewport(width, height);
  // Set preferences on the current same-origin document before a full
  // navigation. Waiting only for the old SPA DOM after calling reload can
  // mistake a still-rendered previous locale for the requested variant.
  await session.evaluate(`(() => {
    localStorage.setItem("cp_lang", ${JSON.stringify(lang)});
    localStorage.setItem("cp_theme", ${JSON.stringify(theme)});
    return true;
  })()`);
  await session.navigate(`${hub}/?cp_verify=${encodeURIComponent(`${lang}-${theme}-${width}x${height}`)}${EXPECTED_ROUTE}`);
  await waitFor(session, `location.hash === ${JSON.stringify(EXPECTED_ROUTE)} && document.documentElement.lang === ${JSON.stringify(lang)} && document.documentElement.classList.contains("dark") === ${theme === "dark"} && Boolean(document.querySelector(".cp-alerts-page h1"))`, `Alerts route (${lang}/${theme}/${width})`);
  await waitFor(session, `document.querySelector(".cp-activity-card")?.getAttribute("aria-busy") === "false" && document.querySelectorAll(".cp-activity-card .cp-activity-event").length > 0`, "initial alert timeline events");
}

async function browserJSON(session, path, token = "session") {
  return session.evaluate(`(async () => {
    const token = ${token === "session" ? "(localStorage.getItem('cp_session') || '')" : JSON.stringify(token)};
    const headers = token ? {Authorization:"Bearer " + token} : {};
    const response = await fetch(${JSON.stringify(path)}, {headers,credentials:token ? "same-origin" : "omit"});
    let body;
    try { body = await response.json(); } catch { body = {}; }
    return {status:response.status,body};
  })()`);
}

async function readAllEvents(session) {
  const first = await browserJSON(session, `/api/v1/alerts/events?limit=${PAGE_SIZE}`);
  const firstPage = first.body?.events || [];
  let second = { status: 0, body: { events: [] } };
  if (firstPage.length === PAGE_SIZE) {
    second = await browserJSON(session, `/api/v1/alerts/events?limit=${PAGE_SIZE}&before=${encodeURIComponent(firstPage.at(-1)?.started_at ?? 0)}`);
  }
  return { first, firstPage, second, all: firstPage.concat(second.body?.events || []) };
}

function timeCandidates(unixSeconds, duration, nowMs) {
  const candidates = new Set();
  for (let delta = -5; delta <= 5; delta++) {
    const atMs = nowMs + delta * 1000;
    candidates.add(duration
      ? formatDuration(Math.max(0, Math.floor(atMs / 1000) - unixSeconds))
      : formatRelativeTimeFromUnixSeconds(unixSeconds, atMs));
  }
  return [...candidates];
}

function displayThreshold(event, catalog) {
  if (event.metric === "storage_usage_pct") return fill(catalog.alerts.threshold_pct_of_quota, { value: event.threshold });
  if (event.metric === "egress_out_pct") return fill(catalog.alerts.threshold_pct_of_limit, { value: event.threshold });
  return String(event.threshold);
}

function localeFor(lang) { return lang === "ko" ? "ko-KR" : "en-US"; }

function expectedTimestamp(seconds, lang, full = false) {
  const date = new Date(seconds * 1000);
  return {
    datetime: date.toISOString(),
    text: new Intl.DateTimeFormat(localeFor(lang), full
      ? { dateStyle: "medium", timeStyle: "short" }
      : { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(date),
    title: new Intl.DateTimeFormat(localeFor(lang), { dateStyle: "full", timeStyle: "long" }).format(date),
  };
}

function expectedTimelineGroups(events, lang) {
  const groups = [];
  for (const event of events) {
    const date = new Date(event.started_at * 1000);
    const key = `${date.getFullYear()}-${date.getMonth() + 1}-${date.getDate()}`;
    const previous = groups.at(-1);
    if (previous?.key === key) previous.events.push(event);
    else groups.push({
      key,
      heading: new Intl.DateTimeFormat(localeFor(lang), { dateStyle: "full" }).format(date),
      events: [event],
    });
  }
  return groups;
}

function expectedDeliveryChips(event, catalog) {
  return (event.deliveries || []).map((delivery) => {
    const channel = delivery.channel_name || catalog.alerts_page.activity_channel_unknown;
    const label = delivery.ok
      ? fill(catalog.alerts_page.delivered, { channel })
      : fill(catalog.alerts_page.delivery_failed, {
          channel,
          error: delivery.error || catalog.alerts_page.delivery_failed_generic,
        });
    return { label, ok: delivery.ok };
  });
}

function expectedEventCondition(event, catalog) {
  const metric = catalog.alerts[`metric_${event.metric}`] || event.metric || catalog.alerts_page.activity_unknown;
  const threshold = displayThreshold(event, catalog);
  return fill(catalog.alerts_page.value_vs_threshold, {
    metric,
    value: Number.isFinite(event.value) ? Number(event.value).toFixed(1) : "–",
    threshold,
  });
}

function timelineEventMatches(item, event, catalog, lang, nowMs) {
  if (!event) return {pass:false,expected:"missing API event at this timeline position",actual:item};
  const status = catalog.alerts_page[event.state] || catalog.alerts_page.activity_unknown;
  const tone = event.state === "firing" ? "cp-status-pill-exceeded" : event.state === "resolved" ? "cp-status-pill-ok" : "cp-status-pill-neutral";
  const hostLabel = event.hostname || event.host_id || catalog.alerts_page.activity_host_unknown;
  const storageScoped = event.metric === "storage_usage_pct";
  const expectedHostTag = event.host_id && !storageScoped ? "A" : "SPAN";
  const expectedHostHref = expectedHostTag === "A" ? `#/host/${encodeURIComponent(event.host_id)}` : "";
  const started = expectedTimestamp(event.started_at, lang);
  const expectedDurations = event.state === "firing"
    ? timeCandidates(event.started_at, true, nowMs).map((duration) => fill(catalog.alerts_page.firing_for, { duration }))
    : event.state === "resolved" && Number.isFinite(event.resolved_at) && event.resolved_at >= event.started_at
      ? [fill(catalog.alerts_page.activity_duration, { duration: formatDuration(event.resolved_at - event.started_at) })]
      : [catalog.alerts_page.activity_duration_unknown];
  const expectedResolvedAt = event.state === "resolved" ? expectedTimestamp(event.resolved_at, lang, true) : null;
  const deliveries = expectedDeliveryChips(event, catalog);
  const deliveryMatches = deliveries.length === 0
    ? item.deliveries.length === 0 && item.deliveryText === catalog.alerts_page.activity_no_deliveries
    : deliveries.length === item.deliveries.length && deliveries.every((expected, index) =>
        item.deliveries[index]?.text === expected.label && item.deliveries[index]?.title === expected.label &&
        item.deliveries[index]?.className.includes(expected.ok ? "cp-alerts-delivery-ok" : "cp-alerts-delivery-fail"));
  const resolvedLabel = item.resolvedAt?.label === catalog.alerts_page.activity_resolved_at;
  return {
    pass: item.id === String(event.id) && item.status === status && item.statusClass.includes(tone) && item.started.datetime === started.datetime &&
      item.started.text === started.text && item.started.title === started.title && item.rule === (event.rule_name || catalog.alerts_page.activity_rule_unknown) &&
      item.host === hostLabel && item.hostTag === expectedHostTag && item.hostHref === expectedHostHref &&
      item.condition === expectedEventCondition(event, catalog) && expectedDurations.includes(item.lifecycle[0]) &&
      (expectedResolvedAt ? resolvedLabel && item.resolvedAt.datetime === expectedResolvedAt.datetime &&
        item.resolvedAt.text === expectedResolvedAt.text && item.resolvedAt.title === expectedResolvedAt.title
        : !item.resolvedAt) && deliveryMatches,
    expected: {
      id: String(event.id), state: status, tone, started, rule: event.rule_name || catalog.alerts_page.activity_rule_unknown,
      host: hostLabel, hostTag: expectedHostTag, hostHref: expectedHostHref,
      condition: expectedEventCondition(event, catalog), lifecycleCandidates: expectedDurations,
      resolvedAt: expectedResolvedAt, deliveries,
    },
    actual: item,
  };
}

function validateTimeline(groups, events, catalog, lang, nowMs) {
  const expectedGroups = expectedTimelineGroups(events, lang);
  const actualEvents = groups.flatMap((group) => group.events);
  const comparisons = actualEvents.map((item, index) => timelineEventMatches(item, events[index], catalog, lang, nowMs));
  const groupChecks = expectedGroups.map((expected, index) => ({
    expectedHeading: expected.heading,
    actualHeading: groups[index]?.heading,
    expectedIDs: expected.events.map((event) => String(event.id)),
    actualIDs: groups[index]?.events.map((event) => event.id) || [],
    pass: groups[index]?.heading === expected.heading && groups[index]?.ariaLinked && groups[index]?.listRole === "list" &&
      JSON.stringify(groups[index]?.events.map((event) => event.id)) === JSON.stringify(expected.events.map((event) => String(event.id))),
  }));
  return {
    pass: groups.length === expectedGroups.length && actualEvents.length === events.length &&
      comparisons.every((result) => result.pass) && groupChecks.every((group) => group.pass) &&
      new Set(actualEvents.map((item) => item.id)).size === actualEvents.length,
    rowCount: actualEvents.length, expectedCount: events.length,
    groupCount: groups.length, expectedGroupCount: expectedGroups.length,
    duplicateIDs: actualEvents.map((item) => item.id).filter((id, index, ids) => ids.indexOf(id) !== index),
    mismatchIndexes: comparisons.map((result, index) => result.pass ? -1 : index).filter((index) => index >= 0),
    mismatches: comparisons.filter((result) => !result.pass).slice(0, 5),
    groupMismatches: groupChecks.filter((group) => !group.pass).slice(0, 5),
  };
}

async function readActivityDOM(session) {
  return session.evaluateJSON(`(() => {
    const text = (node) => (node?.textContent || "").trim();
    const page = document.querySelector(".cp-alerts-page");
    const activeCard = page?.querySelector(".cp-alerts-active-card");
    const activityCard = page?.querySelector(".cp-activity-card");
    const select = page?.querySelector("#cp-alerts-state-filter");
    const root = document.documentElement;
    const body = document.body;
    const activeItems = Array.from(activeCard?.querySelectorAll(".cp-alerts-active-item") || []).map((item) => {
      const name = item.querySelector(".cp-alerts-active-name");
      const host = item.querySelector(".cp-alerts-active-host");
      const secondary = Array.from(item.querySelectorAll("span.cp-muted-small"));
      return {
        rule:text(name), host:text(host), hostTag:host?.tagName || "", hostHref:host?.getAttribute("href") || "",
        valueLine:text(secondary[0]), durationLine:text(secondary[1]),
      };
    });
    const groups = Array.from(activityCard?.querySelectorAll(".cp-activity-day") || []).map((section) => {
      const heading = section.querySelector(".cp-activity-date");
      const labelledBy = section.getAttribute("aria-labelledby") || "";
      const list = section.querySelector("ol.cp-activity-list");
      const events = Array.from(list?.querySelectorAll(":scope > .cp-activity-event") || []).map((item) => {
        const host = item.querySelector(".cp-activity-host");
        const started = item.querySelector(".cp-activity-top time");
        const lifecycle = item.querySelector(".cp-activity-lifecycle");
        const resolved = lifecycle?.querySelector(".cp-activity-resolved-at");
        const resolvedTime = resolved?.querySelector("time");
        const chips = Array.from(item.querySelectorAll(".cp-alerts-delivery-chip")).map((chip) => ({
          text:text(chip), title:chip.getAttribute("title") || "", className:chip.className,
        }));
        return {
          id:item.getAttribute("data-event-id") || "",
          status:text(item.querySelector(".cp-status-pill")),
          statusClass:item.querySelector(".cp-status-pill")?.className || "",
          started:{text:text(started), datetime:started?.getAttribute("datetime") || "", title:started?.getAttribute("title") || ""},
          rule:text(item.querySelector(".cp-activity-rule")),
          host:text(host), hostTag:host?.tagName || "", hostHref:host?.getAttribute("href") || "",
          condition:text(item.querySelector(".cp-activity-condition")),
          lifecycle:Array.from(lifecycle?.querySelectorAll(":scope > span") || []).map(text),
          resolvedAt:resolvedTime ? {label:text(resolved?.querySelector("span")),text:text(resolvedTime),datetime:resolvedTime.getAttribute("datetime") || "",title:resolvedTime.getAttribute("title") || ""} : null,
          deliveries:chips, deliveryText:text(item.querySelector(".cp-alerts-delivery-chips")),
        };
      });
      return {heading:text(heading),ariaLinked:Boolean(labelledBy && heading?.id === labelledBy && document.getElementById(labelledBy) === heading),listRole:list?.getAttribute("role") || "",events};
    });
    const controls = Array.from(page?.querySelectorAll("button,a") || []).map((node) => ({
      tag:node.tagName, text:text(node), label:node.getAttribute("aria-label") || "", href:node.getAttribute("href") || "",
    }));
    return {
      hash:location.hash, lang:document.documentElement.lang || "", theme:localStorage.getItem("cp_theme") || "",
      stateValue:select?.value ?? null, stateLabel:select?.getAttribute("aria-label") || select?.labels?.[0]?.textContent.trim() || "",
      headings:Array.from(page?.querySelectorAll("h1,h2,h3,h4") || []).map(text), activeItems, groups,
      activeCountChip:text(activeCard?.querySelector(".cp-chip")),
      activityTitle:text(activityCard?.querySelector("h2")),
      activityDescription:text(activityCard?.querySelector(".cp-activity-title-group p")),
      ariaBusy:activityCard?.getAttribute("aria-busy") || "",
      loadingText:text(activityCard?.querySelector(".cp-activity-notice")),
      errorText:text(activityCard?.querySelector(".cp-error-banner")),
      emptyText:text(activityCard?.querySelector(".cp-empty-state")),
      retryText:text(activityCard?.querySelector("#cp-activity-retry")),
      showingText:text(activityCard?.querySelector(".cp-activity-footer .cp-muted-small")),
      loadMoreText:text(activityCard?.querySelector("#cp-activity-load-more")),
      loadMorePresent:!!activityCard?.querySelector("#cp-activity-load-more"),
      loadMoreDisabled:!!activityCard?.querySelector("#cp-activity-load-more")?.disabled,
      hasRuleControls:controls.some((item) => /add rule|edit rule|delete rule/i.test(item.text + " " + item.label)),
      hasMuteControls:controls.some((item) => /mute|snooze|acknowledge/i.test(item.text + " " + item.label)),
      controls,
      viewportWidth:innerWidth, documentWidth:Math.max(root.scrollWidth,body.scrollWidth), nowMs:Date.now(),
    };
  })()`);
}

async function pressSelectKey(session, key) {
  const focused = await session.evaluate(`(() => {
    const select = document.querySelector("#cp-alerts-state-filter");
    if (!select) return false;
    select.focus();
    return document.activeElement === select;
  })()`);
  if (!focused) throw new Error("state filter select is missing or could not receive focus");
  await session.pressKey(key);
  await session.pressKey("\uE007");
}

async function installFetchProbe(session, { state = null, mode }) {
  return session.evaluate(`(() => {
    if (window.__activityFetchOriginal) return {installed:false,reason:"fetch probe already installed"};
    const original = window.fetch.bind(window);
    const probe = {mode:${JSON.stringify(mode)},state:${JSON.stringify(state)},used:false,calls:0,release:null,url:""};
    window.__activityFetchOriginal = original;
    window.__activityFetchProbe = probe;
    window.fetch = async (input, init) => {
      const raw = typeof input === "string" ? input : input?.url || String(input);
      let url;
      try { url = new URL(raw, location.href); } catch { return original(input, init); }
      const matches = url.pathname === "/api/v1/alerts/events" && (!probe.state || url.searchParams.get("state") === probe.state);
      if (!matches || probe.used) return original(input, init);
      probe.used = true;
      probe.calls++;
      probe.url = url.href;
      if (probe.mode === "empty") return new Response(JSON.stringify({events:[]}), {status:200,headers:{"Content-Type":"application/json"}});
      if (probe.mode === "failure") return new Response(JSON.stringify({error:"verification-only transient GET failure",code:"internal_error"}), {status:503,headers:{"Content-Type":"application/json"}});
      const response = await original(input, init);
      if (probe.mode === "gate") return await new Promise((resolveResponse) => { probe.release = () => resolveResponse(response); });
      return response;
    };
    return {installed:true};
  })()`);
}

async function fetchProbeState(session) {
  return session.evaluate(`(() => {
    const probe = window.__activityFetchProbe;
    return probe ? {mode:probe.mode,state:probe.state,used:probe.used,calls:probe.calls,url:probe.url} : null;
  })()`);
}

async function releaseAndRestoreFetchProbe(session) {
  return session.evaluate(`(() => {
    const probe = window.__activityFetchProbe;
    probe?.release?.();
    if (window.__activityFetchOriginal) window.fetch = window.__activityFetchOriginal;
    delete window.__activityFetchOriginal;
    delete window.__activityFetchProbe;
    return true;
  })()`);
}

async function readActivityEventIDs(session) {
  return session.evaluateJSON(`Array.from(document.querySelectorAll(".cp-activity-event")).map((item) => item.getAttribute("data-event-id") || "")`);
}

async function captureActivityScreens(session, outDir, name, report, fullPage = true) {
  const viewportPath = join(outDir, `${name}-viewport.png`);
  await session.screenshot(viewportPath, "viewport");
  report.screenshots.push(viewportPath);
  if (fullPage) {
    const fullPath = join(outDir, `${name}-full.png`);
    await session.screenshot(fullPath, "document");
    report.screenshots.push(fullPath);
    report.fullPageScreenshots ||= [];
    report.fullPageScreenshots.push(fullPath);
  }
}

async function runFilterAndPagingTests(session, hub, fixture, catalog, outDir, report, record) {
  await openAlerts(session, hub, "en", "dark", 360, 800);
  const full = await readAllEvents(session);
  const allEvents = full.all;
  const firingEvents = allEvents.filter((event) => event.state === "firing");
  const resolvedEvents = allEvents.filter((event) => event.state === "resolved");

  const initial = await readActivityDOM(session);
  const initialTimeline = validateTimeline(initial.groups, full.firstPage, catalog, "en", initial.nowMs);
  record("initial Activity timeline matches API order, local-date groups, occurrence snapshots, timestamps, host links, and delivery outcomes",
    initialTimeline.pass && initial.stateValue === "" && initialTimeline.rowCount === PAGE_SIZE && initial.loadMorePresent,
    { timeline:initialTimeline,stateValue:initial.stateValue,loadMore:initial.loadMoreText });

  const focus = await session.evaluate(`(() => {
    const select = document.querySelector("#cp-alerts-state-filter");
    select?.focus();
    const style = select ? getComputedStyle(select) : null;
    return {active:document.activeElement === select, label:select?.getAttribute("aria-label") || select?.labels?.[0]?.textContent.trim() || "",
      outline:style?.outlineStyle || "", outlineWidth:style?.outlineWidth || ""};
  })()`);
  await session.evaluate("performance.clearResourceTimings(); true");
  const gate = await installFetchProbe(session, { state:"firing", mode:"gate" });
  await pressSelectKey(session, "\uE015"); // ArrowDown: All states -> Firing.
  await waitFor(session, `window.__activityFetchProbe?.used === true && document.querySelector("#cp-alerts-state-filter")?.value === "firing" && document.querySelector(".cp-activity-card")?.getAttribute("aria-busy") === "true"`, "state filter loading transition on the real firing API response");
  const duringFilter = await readActivityDOM(session);
  const loadingStatePass = gate.installed && duringFilter.stateValue === "firing" && duringFilter.ariaBusy === "true" &&
    duringFilter.loadingText === catalog.alerts_page.activity_loading && duringFilter.groups.length === 0 && !duringFilter.emptyText;
  record("state changes clear stale all-state items and expose the accessible loading state while the actual firing response is pending",
    loadingStatePass,{fetchProbe:await fetchProbeState(session),stateValue:duringFilter.stateValue,ariaBusy:duringFilter.ariaBusy,
      loadingText:duringFilter.loadingText,timelineGroups:duringFilter.groups.length,emptyText:duringFilter.emptyText});
  const loadingShot = join(outDir,"activity-filter-loading-firing-en-dark-360x800.png");
  await session.screenshot(loadingShot,"viewport");
  report.screenshots.push(loadingShot);
  report.targetedScreenshots ||= [];
  report.targetedScreenshots.push(loadingShot);
  await releaseAndRestoreFetchProbe(session);
  await waitFor(session, `document.querySelector("#cp-alerts-state-filter")?.value === "firing" && document.querySelector(".cp-activity-card")?.getAttribute("aria-busy") === "false" && document.querySelectorAll(".cp-activity-event").length === ${firingEvents.length}`, "keyboard state filter to populated firing timeline");
  const firingDOM = await readActivityDOM(session);
  const firingRows = validateTimeline(firingDOM.groups, firingEvents, catalog, "en", firingDOM.nowMs);
  const firingRequest = await session.evaluate(`performance.getEntriesByType("resource").some((entry) => {
    try { const url = new URL(entry.name, location.href); return url.pathname === "/api/v1/alerts/events" && url.searchParams.get("state") === "firing"; }
    catch { return false; }
  })`);
  record("keyboard state filter selects firing and renders exactly the API-backed firing records",
    focus.active && focus.label === catalog.alerts_page.filter_by_state && firingRequest && firingRows.pass &&
      new Set(firingDOM.groups.flatMap((group) => group.events.map((event) => event.id))).size === firingEvents.length,
    { focus, rowCheck: firingRows, filteredCount: firingEvents.length, requestObserved: firingRequest });
  await captureActivityScreens(session,outDir,"activity-filter-firing-en-dark-360x800",report);

  await session.evaluate("performance.clearResourceTimings(); true");
  await pressSelectKey(session, "\uE015"); // ArrowDown: Firing -> Resolved.
  await waitFor(session, `document.querySelector("#cp-alerts-state-filter")?.value === "resolved" && document.querySelector(".cp-activity-card")?.getAttribute("aria-busy") === "false" && document.querySelectorAll(".cp-activity-event").length === ${resolvedEvents.length}`, "keyboard state filter to resolved");
  const resolvedDOM = await readActivityDOM(session);
  const resolvedRows = validateTimeline(resolvedDOM.groups, resolvedEvents, catalog, "en", resolvedDOM.nowMs);
  const resolvedRequest = await session.evaluate(`performance.getEntriesByType("resource").some((entry) => {
    try { const url = new URL(entry.name, location.href); return url.pathname === "/api/v1/alerts/events" && url.searchParams.get("state") === "resolved"; }
    catch { return false; }
  })`);
  record("keyboard state filter selects resolved and renders exactly the API-backed resolved records",
    resolvedRequest && resolvedRows.pass,
    { rowCheck: resolvedRows, filteredCount: resolvedEvents.length, requestObserved: resolvedRequest });
  await captureActivityScreens(session,outDir,"activity-filter-resolved-en-dark-360x800",report);

  // A one-shot, client-side empty success response exercises the available empty renderer;
  // it creates no alert event and never changes the isolated database.
  await session.evaluate("performance.clearResourceTimings(); true");
  await installFetchProbe(session,{state:"firing",mode:"empty"});
  await pressSelectKey(session,"\uE011"); // Home: Resolved -> All states.
  await waitFor(session,`document.querySelector("#cp-alerts-state-filter")?.value === "" && document.querySelector(".cp-activity-card")?.getAttribute("aria-busy") === "false" && document.querySelectorAll(".cp-activity-event").length === ${PAGE_SIZE}`,"keyboard reset to all real history");
  await pressSelectKey(session,"\uE015"); // All states -> the one-shot empty firing response.
  await waitFor(session,`window.__activityFetchProbe?.used === true && document.querySelector("#cp-alerts-state-filter")?.value === "firing" && document.querySelector(".cp-empty-state")`,"empty matching-state UI");
  const emptyDOM=await readActivityDOM(session);
  const emptyPass=emptyDOM.stateValue==="firing" && emptyDOM.ariaBusy==="false" &&
    emptyDOM.emptyText.includes(catalog.alerts_page.activity_no_match) && emptyDOM.emptyText.includes(catalog.alerts_page.activity_try_all) &&
    emptyDOM.groups.length===0 && emptyDOM.loadMoreText==="";
  record("test-only empty API success response renders the localized no-match state without fabricating or persisting an event",emptyPass,
    {emptyText:emptyDOM.emptyText,groups:emptyDOM.groups.length,fetchProbe:await fetchProbeState(session),fixtureEventsPersisted:allEvents.length});
  const emptyShot=join(outDir,"activity-empty-firing-en-dark-360x800.png");
  await session.screenshot(emptyShot,"document");
  report.screenshots.push(emptyShot); report.targetedScreenshots.push(emptyShot);
  report.fullPageScreenshots ||= []; report.fullPageScreenshots.push(emptyShot);
  await releaseAndRestoreFetchProbe(session);
  await pressSelectKey(session,"\uE011");
  await waitFor(session,`document.querySelector("#cp-alerts-state-filter")?.value === "" && document.querySelector(".cp-activity-card")?.getAttribute("aria-busy")==="false" && document.querySelectorAll(".cp-activity-event").length === ${PAGE_SIZE}`,"restoring real unfiltered history after empty-state check");
  const reset=await readActivityDOM(session);
  record("keyboard Home restores API-backed all-state history after the empty-state probe",reset.stateValue==="" && validateTimeline(reset.groups,full.firstPage,catalog,"en",reset.nowMs).pass,
    {stateValue:reset.stateValue,events:reset.groups.flatMap((group)=>group.events).length});

  const buttonFocus = await session.evaluate(`(() => {
    const button = document.querySelector("#cp-activity-load-more");
    if (!button) return null;
    button.focus();
    const style = getComputedStyle(button);
    return {active:document.activeElement === button, text:button.textContent.trim(), outline:style.outlineStyle, outlineWidth:style.outlineWidth};
  })()`);
  await session.evaluate("performance.clearResourceTimings(); true");
  if (!buttonFocus?.active) throw new Error("Load more button was not available for keyboard pagination");
  await session.pressKey("\uE007");
  await waitFor(session, `document.querySelector(".cp-activity-card")?.getAttribute("aria-busy")==="false" && document.querySelectorAll(".cp-activity-event").length === ${allEvents.length}`, "keyboard Load more second page");
  const page2DOM = await readActivityDOM(session);
  const page2Rows = validateTimeline(page2DOM.groups, allEvents, catalog, "en", page2DOM.nowMs);
  const before = full.firstPage.at(-1)?.started_at;
  const page2Request = await session.evaluate(`performance.getEntriesByType("resource").some((entry) => {
    try { const url = new URL(entry.name, location.href); return url.pathname === "/api/v1/alerts/events" && url.searchParams.get("before") === ${JSON.stringify(String(before))}; }
    catch { return false; }
  })`);
  const page2Items = page2DOM.groups.flatMap((group) => group.events);
  const deletedHostRow = page2Items.at(-1);
  const secondPageOnly = allEvents.slice(PAGE_SIZE);
  const deletedHostEvent = secondPageOnly.find((event) => event.host_id === fixture.deleted_host_id);
  record("Enter on Load more requests the real before cursor and appends the remaining API rows in order",
    page2Rows.pass && page2Request && !page2DOM.loadMorePresent && secondPageOnly.length === allEvents.length - PAGE_SIZE,
    { buttonFocus, rowCheck: page2Rows, requestObserved: page2Request, expectedBefore: before,
      page2Count: secondPageOnly.length, deletedHostEvent: deletedHostEvent ? { id: deletedHostEvent.id, host_id: deletedHostEvent.host_id, started_at: deletedHostEvent.started_at } : null,
      finalRow: deletedHostRow });
  record("deleted-host historical event retains its snapshot name and navigable host route", Boolean(deletedHostEvent) &&
    deletedHostRow?.hostHref === `#/host/${encodeURIComponent(fixture.deleted_host_id)}` && deletedHostRow?.host === fixture.deleted_hostname && deletedHostRow?.hostTag === "A",
    { expectedHostID: fixture.deleted_host_id, expectedHostname: fixture.deleted_hostname, finalRow: deletedHostRow });

  record("API first/second pages and rendered timeline have no duplicate event IDs", new Set(allEvents.map((event)=>event.id)).size===allEvents.length &&
    new Set(page2Items.map((event)=>event.id)).size===allEvents.length && page2Items.length===allEvents.length,
    {apiIDs:allEvents.length,renderedIDs:page2Items.length,uniqueRenderedIDs:new Set(page2Items.map((event)=>event.id)).size});
  await captureActivityScreens(session,outDir,"activity-load-more-page2-en-dark-360x800",report);

  await session.navigate(`${hub}/?cp_verify=reload-en-dark-360${EXPECTED_ROUTE}`);
  await waitFor(session, `location.hash === "#/alerts" && document.querySelector("#cp-alerts-state-filter")?.value === "" && document.querySelector(".cp-activity-card")?.getAttribute("aria-busy")==="false" && document.querySelectorAll(".cp-activity-event").length === ${PAGE_SIZE}`, "reload resets history to the first page");
  const reloaded = await readActivityDOM(session);
  record("browser reload restores the first unfiltered 50-event page and Load more control",
    validateTimeline(reloaded.groups, full.firstPage, catalog, "en", reloaded.nowMs).pass && reloaded.loadMorePresent,
    { stateValue: reloaded.stateValue, rowCount: reloaded.groups.flatMap((group)=>group.events).length, loadMorePresent: reloaded.loadMorePresent });

  const longHostLink = await session.evaluate(`(() => {
    const items = Array.from(document.querySelectorAll(".cp-activity-event"));
    const link = items.map((item) => item.querySelector(".cp-activity-host")).find((node) => node?.getAttribute("href") === ${JSON.stringify(`#/host/${encodeURIComponent(fixture.long_host_id)}`)});
    if (!link) return null;
    const href = link.getAttribute("href");
    link.click();
    return {href, text:link.textContent.trim()};
  })()`);
  await waitFor(session, `location.hash === ${JSON.stringify(`#/host/${fixture.long_host_id}`)}`, "clicking a history host link");
  await waitFor(session, `Boolean(document.querySelector(".cp-host-detail-header h1"))`, "linked host detail page");
  const hostRouteWorked = await session.evaluate(`Boolean(document.querySelector(".cp-host-detail-header h1"))`);
  record("history host anchors navigate through the application's host-detail route", longHostLink?.href === `#/host/${fixture.long_host_id}` && hostRouteWorked,
    { link: longHostLink, detailRendered: hostRouteWorked });
  await session.evaluate(`location.hash = ${JSON.stringify(EXPECTED_ROUTE)}; true`);
  await waitFor(session, `location.hash === "#/alerts" && document.querySelector(".cp-activity-card")?.getAttribute("aria-busy")==="false" && document.querySelectorAll(".cp-activity-event").length === ${PAGE_SIZE}`, "returning from host detail to Alerts");

  // Let the real 15-second page refresh run once with a one-shot 503 transport fault.
  // The isolated server/database remain untouched; the existing timeline must survive.
  await installFetchProbe(session,{mode:"failure"});
  await waitFor(session,`window.__activityFetchProbe?.used===true && document.querySelector(".cp-activity-card")?.getAttribute("aria-busy")==="false" && document.querySelector(".cp-activity-card .cp-error-banner") && document.querySelectorAll(".cp-activity-event").length===${PAGE_SIZE}`,"failed background history refresh preserving rendered items",25_000);
  const failedRefresh=await readActivityDOM(session);
  const failedRefreshIDs=failedRefresh.groups.flatMap((group)=>group.events.map((event)=>event.id));
  record("one verification-only failed auto-refresh shows an error and preserves the previously loaded API timeline",
    failedRefresh.errorText.length>0 && failedRefreshIDs.length===PAGE_SIZE &&
      JSON.stringify(failedRefreshIDs)===JSON.stringify(full.firstPage.map((event)=>String(event.id))) &&
      failedRefresh.loadMorePresent,
    {errorText:failedRefresh.errorText,renderedCount:failedRefreshIDs.length,retainedFirstPage:failedRefreshIDs[0],retainedLastPage:failedRefreshIDs.at(-1),loadMorePresent:failedRefresh.loadMorePresent,fetchProbe:await fetchProbeState(session)});
  const failedRefreshShot=join(outDir,"activity-failed-refresh-retained-history-en-dark-360x800.png");
  await session.screenshot(failedRefreshShot,"document");
  report.screenshots.push(failedRefreshShot); report.targetedScreenshots.push(failedRefreshShot);
  report.fullPageScreenshots ||= []; report.fullPageScreenshots.push(failedRefreshShot);
  await releaseAndRestoreFetchProbe(session);
}

async function inspectRuleControls(session, hub, catalog, report, record) {
  await session.setViewport(1440, 900);
  await session.evaluate(`(() => {
    localStorage.setItem("cp_lang", "en");
    localStorage.setItem("cp_theme", "dark");
    return true;
  })()`);
  await session.navigate(`${hub}/?cp_verify=rules-en-dark#/settings/alert-rules`);
  await waitFor(session, `location.hash === "#/settings/alert-rules" && Boolean(document.querySelector(".cp-rule-list .cp-rule-row"))`, "admin alert-rules section");
  const state = await session.evaluateJSON(`(() => {
    const rows = Array.from(document.querySelectorAll(".cp-rule-list .cp-rule-row"));
    const ruleCard = document.querySelector(".cp-rule-list")?.closest(".cp-card");
    return {
      names:rows.map((row) => row.querySelector(".cp-rule-row-name")?.textContent.trim() || ""),
      enabled:rows.map((row) => row.querySelector(".cp-chip")?.textContent.trim() || ""),
      controls:Array.from(ruleCard?.querySelectorAll("button") || []).map((button) => button.textContent.trim()),
      hasMute:Array.from(document.querySelectorAll("button,a")).some((node) => /mute|snooze|acknowledge/i.test(node.textContent + " " + (node.getAttribute("aria-label") || ""))),
      hasHostFilter:Boolean(document.querySelector("#cp-alerts-host-filter")),
    };
  })()`);
  const apiRules = await browserJSON(session, "/api/v1/alerts/rules");
  const expectedNames = [
    "Outbound traffic 80% (warning)", "Outbound traffic 95% (critical)", "Outbound traffic 100% (exceeded)",
  ];
  record("admin alert-rules section renders its three disabled fixture rules and existing add/edit/delete controls",
    locationIsSettings(await session.evaluate("location.hash")) && apiRules.status === 200 && state.names.length === 3 &&
      expectedNames.every((name) => state.names.includes(name)) && state.controls.some((label) => /add rule/i.test(label)) &&
      state.controls.filter((label) => /edit/i.test(label)).length === 3 && state.controls.filter((label) => /delete/i.test(label)).length === 3,
    { ruleAPIStatus: apiRules.status, ui: state, expectedNames });
  record("no mute/snooze/acknowledge action is exposed in the current alert-rule section", !state.hasMute, state);
  report.ruleControls = { names: state.names, enabled: state.enabled, controls: state.controls, adminAPIStatus: apiRules.status };
  const screenshot = join(report.outputDir, "alerts-rule-controls-en-dark-1440x900.png");
  await session.screenshot(screenshot);
  report.screenshots.push(screenshot);
  const fullScreenshot = join(report.outputDir, "alerts-rule-controls-en-dark-1440x900-full.png");
  await session.screenshot(fullScreenshot, "document");
  report.screenshots.push(fullScreenshot);
  report.fullPageScreenshots ||= [];
  report.fullPageScreenshots.push(fullScreenshot);
}

function locationIsSettings(hash) {
  return hash === "#/settings/alert-rules";
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (!options) { usage(); return 0; }
  const passwordPath = localPath(options.passwordFile, "password file");
  const fixturePath = localPath(options.fixtureFile, "fixture manifest");
  const outDir = localPath(options.out, "output directory");
  if (!existsSync(passwordPath)) throw new Error(`password file missing: ${passwordPath}`);
  if (!existsSync(fixturePath)) throw new Error(`alert fixture manifest missing: ${fixturePath}`);
  if ((statSync(passwordPath).mode & 0o077) !== 0) throw new Error("password file must be private (mode 0600 or stricter)");
  const password = readFileSync(passwordPath, "utf8").replace(/\r?\n$/, "");
  if (!password) throw new Error("password file is empty");
  const fixture = JSON.parse(readFileSync(fixturePath, "utf8"));
  if (fixture.total !== 55 || fixture.page_size !== PAGE_SIZE || fixture.notification_channels !== 0 || fixture.distinct_utc_dates < 2 ||
      fixture.storage_metric !== "storage_usage_pct" || fixture.storage_host_id !== "42" || !fixture.storage_event_id) {
    throw new Error("refusing unexpected Activity fixture manifest; expected 55 events, a 50-row page, no channels, multiple dates, and one storage-scoped snapshot");
  }
  mkdirSync(outDir, { recursive: true, mode: 0o700 });
  const realOutDir = realpathSync(outDir);
  if (realOutDir !== VERIFY_ROOT && !realOutDir.startsWith(`${VERIFY_ROOT}/`)) throw new Error("resolved Activity output directory escaped .verification");

  const report = {
    generatedAt: new Date().toISOString(), stage: options.stage, hub: options.hub,
    fixture: { ...fixture }, matrix: [], checks: [], screenshots: [], fullPageScreenshots: [], targetedScreenshots: [],
    storageHostlinkEvidence: [], consoleErrors: [], javascriptErrors: [],
    profileRemoved: false, outputDir: realOutDir,
    currentRoute: "#/alerts (existing Active alerts plus a state-filtered, paginated, local-date-grouped alert lifecycle timeline)",
    contractNotes: {
      activityMeans: "Existing alert event history in the Alerts route; not audit_log and not a combined activity feed.",
      severity: "AlertEvent has state/value/threshold/rule snapshot but no severity field; the UI presents lifecycle state and event rule snapshot without inventing severity.",
      hostFilter: "GET /api/v1/alerts/events supports host=; current Alerts UI exposes only the state filter.",
      dateGrouping: "Timeline sections group contiguous newest-first API occurrences by the browser's local start date; rule resolved dates do not reorder the started_at cursor.",
      emptyAndLoading: "Loading and the zero-match empty renderer are tested; the empty success response is a one-shot browser fetch stub only and adds no event to the isolated database.",
      errorState: "A one-shot browser fetch failure exercises a real automatic refresh path and verifies loaded events survive; no API/backend data is mutated.",
      permissions: "Role differential is NOT APPLICABLE: auth.go has one fixed admin account, requireAdmin aliases requireUser, and both session and CP_UI_TOKEN authentication use that same single-account tier. Unauthenticated 401s and both authenticated methods are checked.",
      permissionMap: {
        events: "GET /api/v1/alerts/events requires user authentication.",
        active: "GET /api/v1/alerts/active requires user authentication.",
        rulesAndChannels: "Management routes are wrapped in requireAdmin, which aliases requireUser for the one fixed admin account; session and CP_UI_TOKEN access are both checked with GETs only.",
        roleDifferential: "NOT APPLICABLE: the hub has no non-admin account or distinct user/admin tier to compare.",
        mute: "No event mute/snooze/acknowledge route is registered in the current Alerts routes.",
      },
      controls: "Rule management lives at Settings > Alert rules; current Alerts timeline has no mute/snooze/acknowledge actions or event-level controls.",
      storageScopedHostLinks: "One resolved storage_usage_pct account snapshot uses host_id='42' and a long account label; GET /hosts/42 is not found, and the timeline must render it as text without a host href. No storage account, credential, channel, or provider is seeded.",
    },
  };
  const record = (name, pass, detail = undefined) => {
    report.checks.push({ name, pass: Boolean(pass), ...(detail === undefined ? {} : { detail }) });
    return Boolean(pass);
  };
  let session = null;
  let failure = null;
  try {
    session = new BidiSession();
    registerActiveSession(session);
    await session.start();
    await session.setViewport(360, 800);
    await login(session, options.hub, password);
    report.loginSucceeded = true;

    // Read endpoint permission/status behavior and the exact API-backed fixture.
    const unauthenticated = {};
    for (const path of ["/api/v1/alerts/events?limit=1", "/api/v1/alerts/active", "/api/v1/alerts/rules", "/api/v1/alerts/channels"]) {
      unauthenticated[path] = await browserJSON(session, path, "");
    }
    const pages = await readAllEvents(session);
    const active = await browserJSON(session, "/api/v1/alerts/active");
    const firing = await browserJSON(session, "/api/v1/alerts/events?state=firing&limit=50");
    const resolved = await browserJSON(session, "/api/v1/alerts/events?state=resolved&limit=50");
    const ghostHost = await browserJSON(session, `/api/v1/alerts/events?host=${encodeURIComponent(fixture.deleted_host_id)}&limit=50`);
    const storageIDHostLookup = await browserJSON(session, `/api/v1/hosts/${encodeURIComponent(fixture.storage_host_id)}`);
    const invalidState = await browserJSON(session, "/api/v1/alerts/events?state=unknown&limit=1");
    const rules = await browserJSON(session, "/api/v1/alerts/rules");
    const channels = await browserJSON(session, "/api/v1/alerts/channels");
    const principal = await browserJSON(session, "/api/v1/auth/me");
    const uiTokenEvents = await browserJSON(session, "/api/v1/alerts/events?limit=1", options.uiToken);
    const uiTokenRules = await browserJSON(session, "/api/v1/alerts/rules", options.uiToken);
    const uiTokenChannels = await browserJSON(session, "/api/v1/alerts/channels", options.uiToken);
    const allEvents = pages.all;
    const eventIDs = allEvents.map((event) => event.id);
    const dateCount = new Set(allEvents.map((event) => new Date(event.started_at * 1000).toISOString().slice(0, 10))).size;
    const descending = allEvents.every((event, index) => index === 0 || event.started_at < allEvents[index - 1].started_at);
    const fixtureRowsMatch = allEvents.length === fixture.total &&
      allEvents.filter((event) => event.state === "firing").length === fixture.firing &&
      allEvents.filter((event) => event.state === "resolved").length === fixture.resolved &&
      allEvents.filter((event) => (event.deliveries || []).length > 0).length === fixture.with_deliveries &&
      new Set(eventIDs).size === eventIDs.length && dateCount >= 2;
    record("authenticated event history returns the seeded total with unique newest-first cursor order over multiple dates",
      pages.first.status === 200 && pages.second.status === 200 && pages.firstPage.length === PAGE_SIZE &&
      pages.second.body?.events?.length === fixture.total - PAGE_SIZE && descending && fixtureRowsMatch,
      { firstStatus: pages.first.status, firstCount: pages.firstPage.length, secondStatus: pages.second.status,
        secondCount: pages.second.body?.events?.length ?? 0, total: allEvents.length, uniqueIDs: new Set(eventIDs).size,
        descending, dateCount, fixtureRowsMatch });
    record("state and host API filters return the matching real event snapshots",
      firing.status === 200 && resolved.status === 200 && ghostHost.status === 200 &&
        (firing.body.events || []).length === fixture.firing && (resolved.body.events || []).length === fixture.resolved &&
        (ghostHost.body.events || []).length === 1 && ghostHost.body.events[0]?.host_id === fixture.deleted_host_id &&
        (firing.body.events || []).every((event) => event.state === "firing") && (resolved.body.events || []).every((event) => event.state === "resolved"),
      { firingCount: firing.body?.events?.length, expectedFiring: fixture.firing,
        resolvedCount: resolved.body?.events?.length, expectedResolved: fixture.resolved,
        deletedHostCount: ghostHost.body?.events?.length, deletedHostID: ghostHost.body?.events?.[0]?.host_id || "" });
    record("storage snapshot ID is not a host record (no storage account or fake host was seeded)",
      storageIDHostLookup.status === 404,
      {storageAccountID:fixture.storage_host_id,hostSummaryStatus:storageIDHostLookup.status,apiBody:storageIDHostLookup.body});
    record("active-alert API contains only current firing events in newest-first order",
      active.status === 200 && (active.body.events || []).length === fixture.firing &&
        (active.body.events || []).every((event, index, rows) => event.state === "firing" && (index === 0 || event.started_at < rows[index - 1].started_at)),
      { status: active.status, count: active.body?.events?.length, expectedCount: fixture.firing });
    record("API rejects invalid state filters instead of silently changing query semantics",
      invalidState.status === 400 && invalidState.body?.code === "invalid_state",
      { status: invalidState.status, body: invalidState.body });
    const unauthStatuses = Object.fromEntries(Object.entries(unauthenticated).map(([path, response]) => [path, response.status]));
    record("event, active, rule, and channel endpoints reject unauthenticated requests",
      Object.values(unauthenticated).every((response) => response.status === 401 || response.status === 403), unauthStatuses);
    record("admin session can read rules and channels; fixture contains no notification channel",
      rules.status === 200 && channels.status === 200 && Array.isArray(rules.body) && rules.body.length === 3 &&
        Array.isArray(channels.body) && channels.body.length === 0 && rules.body.every((rule) => !rule.enabled),
      { rulesStatus: rules.status, ruleCount: rules.body?.length, channelsStatus: channels.status, channelCount: channels.body?.length,
        allRulesDisabled: rules.body?.every((rule) => !rule.enabled) });
    record("role differential is NOT APPLICABLE: the auth model exposes one fixed admin principal",
      principal.status === 200 && principal.body?.username === "admin",
      { status:principal.status,username:principal.body?.username,authMethod:principal.body?.auth_method,
        roleDifferential:"NOT APPLICABLE: the hub has one fixed admin account and requireAdmin aliases requireUser." });
    record("CP_UI_TOKEN and admin session authenticate to the same existing event/rule/channel access tier",
      uiTokenEvents.status === 200 && uiTokenEvents.body?.events?.[0]?.id === pages.firstPage[0]?.id &&
        uiTokenRules.status === 200 && uiTokenChannels.status === 200 &&
        JSON.stringify((uiTokenRules.body || []).map((rule) => rule.id)) === JSON.stringify((rules.body || []).map((rule) => rule.id)),
      {sessionEventsStatus:pages.first.status,uiTokenEventsStatus:uiTokenEvents.status,
        sessionRulesStatus:rules.status,uiTokenRulesStatus:uiTokenRules.status,
        sessionChannelsStatus:channels.status,uiTokenChannelsStatus:uiTokenChannels.status});
    const storageEvents = allEvents.filter((event) => event.metric === fixture.storage_metric);
    const storageEvent = storageEvents.find((event) => event.id === fixture.storage_event_id);
    const ordinaryHostEvents = allEvents.filter((event) => event.metric !== fixture.storage_metric);
    record("event fixture preserves 54 host-scoped egress snapshots plus one resolved storage-quota snapshot without a fake host row",
      ordinaryHostEvents.length === fixture.total - 1 && ordinaryHostEvents.every((event) => event.metric === "egress_out_pct") &&
        storageEvents.length === 1 && storageEvent?.host_id === fixture.storage_host_id && storageEvent?.hostname === fixture.storage_hostname &&
        storageEvent?.state === "resolved" && storageEvent?.rule_id > 0 && storageEvent?.value === fixture.storage_value &&
        storageEvent?.threshold === fixture.storage_threshold && (storageEvent?.deliveries || []).length === 0,
      {ordinaryHostEventCount:ordinaryHostEvents.length,storageEventCount:storageEvents.length,storageEvent});
    record("event fixture has real snapshot fields for firing/resolved states, threshold/rule names, host links, and inert delivery outcomes",
      allEvents.some((event) => event.state === "firing") && allEvents.some((event) => event.state === "resolved") &&
        allEvents.every((event) => event.rule_id > 0 && event.threshold > 0 && event.value > event.threshold) &&
        ordinaryHostEvents.every((event) => event.metric === "egress_out_pct") &&
        allEvents.some((event) => (event.deliveries || []).some((delivery) => delivery.ok)) &&
        allEvents.some((event) => (event.deliveries || []).some((delivery) => !delivery.ok)),
      { eventCount: allEvents.length, states: [...new Set(allEvents.map((event) => event.state))],
        deliveriesWithSuccess: allEvents.filter((event) => event.deliveries?.some((delivery) => delivery.ok)).length,
        deliveriesWithFailure: allEvents.filter((event) => event.deliveries?.some((delivery) => !delivery.ok)).length,
        severityFieldPresent: allEvents.some((event) => Object.hasOwn(event, "severity")),storageEventID:storageEvent?.id,
        storageHostname:storageEvent?.hostname,storageMetric:storageEvent?.metric,storageHostID:storageEvent?.host_id });
    report.apiSummary = {
      total: allEvents.length, firstPage: pages.firstPage.length, secondPage: pages.second.body?.events?.length || 0,
      firing: fixture.firing, resolved: fixture.resolved, distinctUTCDateCount: dateCount,
      active: active.body?.events?.length || 0, invalidStateStatus: invalidState.status,
      unauthenticatedStatuses: unauthStatuses,
      pageTwoDeletedHost: (pages.second.body?.events || []).some((event) => event.host_id === fixture.deleted_host_id),
      storageEvent:storageEvent ? {id:storageEvent.id,host_id:storageEvent.host_id,hostname:storageEvent.hostname,metric:storageEvent.metric,
        state:storageEvent.state,value:storageEvent.value,threshold:storageEvent.threshold,deliveries:storageEvent.deliveries} : null,
      storageHostSummaryStatus:storageIDHostLookup.status,
      rules: rules.body || [], channelCount: channels.body?.length ?? null,
      principal:principal.body || {},
      uiToken:{eventsStatus:uiTokenEvents.status,rulesStatus:uiTokenRules.status,channelsStatus:uiTokenChannels.status},
    };

    for (const [lang, theme, width, height] of MATRIX) {
      const catalog = loadCatalog(lang);
      await openAlerts(session, options.hub, lang, theme, width, height);
      const dom = await readActivityDOM(session);
      const timelineCheck = validateTimeline(dom.groups, pages.firstPage, catalog, lang, dom.nowMs);
      const activeEvents = active.body.events || [];
      const activeNamesPass = dom.activeItems.length === activeEvents.length && dom.activeItems.every((item, index) => {
        const event = activeEvents[index];
        const metric = catalog.alerts[`metric_${event.metric}`] || event.metric;
        const value = expectedEventCondition(event, catalog);
        const duration = fill(catalog.alerts_page.firing_for, { duration: formatDuration(Math.max(0, Math.floor(dom.nowMs / 1000) - event.started_at)) });
        return item.hostTag === "A" && item.hostHref === `#/host/${encodeURIComponent(event.host_id)}` &&
          item.rule === (event.rule_name || metric) && item.host === (event.hostname || event.host_id) && item.valueLine === value &&
          timeCandidates(event.started_at, true, dom.nowMs).some((candidate) => item.durationLine === fill(catalog.alerts_page.firing_for, { duration: candidate }));
      });
      const activeCountPass = dom.activeCountChip === fill(catalog.nav.alerts_firing_count, { count: activeEvents.length });
      const localizedHeadingsPass = dom.headings.includes(catalog.alerts_page.active_alerts) && dom.activityTitle === catalog.alerts_page.activity_title &&
        dom.activityDescription === catalog.alerts_page.activity_description;
      const showingPass = dom.showingText === fill(catalog.alerts_page.activity_showing, { count: PAGE_SIZE });
      const hasOverflow = dom.documentWidth > dom.viewportWidth;
      const controls = {
        hostFilter: Boolean(await session.evaluate(`Boolean(document.querySelector("#cp-alerts-host-filter"))`)),
        ruleControls: dom.hasRuleControls,
        muteControls: dom.hasMuteControls,
        stateFilterAccessibleName: dom.stateLabel,
        loadMoreTabbable: !!await session.evaluate(`(() => { const b=document.querySelector("#cp-activity-load-more"); return Boolean(b && b.tabIndex >= 0); })()`),
      };
      const matrixPass = dom.hash === EXPECTED_ROUTE && dom.lang === lang && dom.theme === theme &&
        dom.stateValue === "" && timelineCheck.rowCount === PAGE_SIZE && dom.loadMorePresent && !dom.loadMoreDisabled &&
        timelineCheck.pass && activeNamesPass && activeCountPass && localizedHeadingsPass && showingPass &&
        dom.ariaBusy === "false" && !dom.loadingText && !hasOverflow;
      record(`${lang}/${theme}/${width}x${height}: localized Active alerts/date-grouped Activity timeline, API-backed content/order, accessibility and no overflow`, matrixPass,
        { timeline:timelineCheck,activeCount:dom.activeItems.length,activeNamesPass,activeCountPass,localizedHeadingsPass,showingPass,
          lang:dom.lang,theme:dom.theme,ariaBusy:dom.ariaBusy,loadingText:dom.loadingText,
          viewportWidth:dom.viewportWidth,documentWidth:dom.documentWidth,controls,hasOverflow });
      const screenshot = join(realOutDir, `activity-${lang}-${theme}-${width}x${height}-viewport.png`);
      await session.screenshot(screenshot,"viewport");
      report.screenshots.push(screenshot);
      const fullScreenshot = join(realOutDir, `activity-full-${lang}-${theme}-${width}x${height}.png`);
      await session.screenshot(fullScreenshot,"document");
      report.screenshots.push(fullScreenshot);
      report.fullPageScreenshots ||= [];
      report.fullPageScreenshots.push(fullScreenshot);
      report.matrix.push({lang,theme,width,height,renderedEvents:timelineCheck.rowCount,dateGroupCount:dom.groups.length,
        activeAlertRows:dom.activeItems.length,noHorizontalOverflow:!hasOverflow,timelineContractPassed:timelineCheck.pass});
      if ((width === 360 || width === 1440) && (lang === "en" || lang === "ko")) {
        const storageOccurrence = dom.groups.flatMap((group) => group.events).find((event) => event.id === String(fixture.storage_event_id));
        const storageNoHostRoute = Boolean(storageOccurrence) && storageOccurrence.hostTag === "SPAN" &&
          storageOccurrence.hostHref === "" && storageOccurrence.host === fixture.storage_hostname;
        record(`${lang}/${theme}/${width}x${height}: storage quota occurrence keeps its account snapshot label without a fake host link`, storageNoHostRoute,
          {storageEventID:fixture.storage_event_id,hostID:fixture.storage_host_id,label:storageOccurrence?.host || "",
            hostElement:storageOccurrence?.hostTag || "",href:storageOccurrence?.hostHref || null,
            valueCondition:storageOccurrence?.condition || "",status:storageOccurrence?.status || ""});
        report.storageHostlinkEvidence.push({lang,theme,width,height,pass:storageNoHostRoute,hostElement:storageOccurrence?.hostTag || "",
          href:storageOccurrence?.hostHref || null,eventID:fixture.storage_event_id});
        const storageShot = join(realOutDir,`activity-storage-event-${lang}-${theme}-${width}x${height}.png`);
        await session.screenshot(storageShot,"document");
        report.screenshots.push(storageShot);
        report.fullPageScreenshots.push(storageShot);
        report.targetedScreenshots.push(storageShot);
      }
      if (lang === "en" && theme === "dark" && width === 360) {
        record("Alerts route exposes no host filter, rule editor, or event mute/acknowledge controls; timeline items remain read-only",
          !controls.hostFilter && !controls.ruleControls && !controls.muteControls,
          {controls,visibleButtonsAndLinks:dom.controls,groupHeadings:dom.groups.length});
        report.currentControls = {...controls,groupHeadings:dom.groups.length};
      }
    }

    await runFilterAndPagingTests(session, options.hub, fixture, loadCatalog("en"), realOutDir, report, record);
    await inspectRuleControls(session, options.hub, loadCatalog("en"), report, record);
  } catch (error) {
    failure = error;
    report.failure = error?.stack || String(error);
  } finally {
    if (session) {
      report.consoleErrors = session.consoleErrors;
      report.javascriptErrors = session.javascriptErrors;
      report.networkFailures = session.networkFailures;
      const profile = session.profile;
      await session.stop().catch((error) => { report.cleanupError = String(error); });
      report.profileRemoved = !profile || !existsSync(profile);
      registerActiveSession(null);
    }
    record("browser console, JavaScript and missing-asset errors absent", report.consoleErrors.length === 0 && report.javascriptErrors.length === 0 && !report.networkFailures?.length,
      { consoleErrors: report.consoleErrors, javascriptErrors: report.javascriptErrors, networkFailures: report.networkFailures });
    report.passed = !failure && report.checks.every((check) => check.pass) && report.profileRemoved;
    delete report.outputDir;
    writeFileSync(safeVerificationPath(join(realOutDir, "activity-contract-report.json")), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 });
  }
  process.stdout.write(`Activity history browser contract: ${report.passed ? "PASS" : "FAIL"} (${report.checks.filter((check) => check.pass).length}/${report.checks.length} checks)\n`);
  process.stdout.write(`report: ${join(realOutDir, "activity-contract-report.json")}\n`);
  if (failure) process.stderr.write(`${failure.stack || failure}\n`);
  for (const failed of report.checks.filter((check) => !check.pass)) process.stderr.write(`FAIL: ${failed.name}\n`);
  return report.passed ? 0 : 1;
}

const invokedDirectly = Boolean(process.argv[1]) && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (invokedDirectly) {
  const parsed = parseArgs(process.argv.slice(2));
  if (parsed === null) {
    usage();
    process.exit(0);
  }
  main().then((code) => process.exit(code)).catch((error) => {
    process.stderr.write(`verify-activity-contract.mjs: ${error?.stack || error}\n`);
    process.exit(1);
  });
}
