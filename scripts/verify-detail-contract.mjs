#!/usr/bin/env node
// Isolated detail-stage browser contract. It reuses the local Firefox BiDi
// session from verify-phase1-auth.mjs, reads the real detail APIs, and writes
// evidence only under .verification/. No notification or cloud fixture is
// created by this checker.
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
import { formatBitrate, formatBytes, formatDuration, formatLoad, formatPercent } from "../web/assets/js/core/format.js";
import { BidiSession, registerActiveSession } from "./verify-phase1-auth.mjs";
import { safeVerificationPath } from "./verify-round28-safety.mjs";

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = realpathSync(resolve(SCRIPT_DIR, ".."));
const VERIFY_ROOT = resolve(REPO_ROOT, ".verification");
const PRIMARY_HOST = "demo-aws-web-01";
const RANGE_LIST = ["1h", "6h", "24h", "7d", "30d"];
const SERIES_FIELDS = ["cpu", "mem", "disk", "net_rx", "net_tx", "disk_read", "disk_write", "load1"];
const RANGE_RESOLUTION = { "1h": 15, "6h": 15, "24h": 300, "7d": 300, "30d": 3600 };
const SPECIAL_HOSTS = [
  { id: "demo-oci-offline-01", kind: "offline" },
  { id: "verify-unknown-01", kind: "unknown-no-sample" },
  { id: "verify-long-name-01", kind: "long-name-no-sample" },
];
const sleep = (ms) => new Promise((resolveSleep) => setTimeout(resolveSleep, ms));

function parseArgs(argv) {
  const options = { stage: "detail", hub: null, passwordFile: null, out: null };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--help" || arg === "-h") return null;
    if (!["--stage", "--hub", "--password-file", "--out"].includes(arg)) throw new Error(`unknown argument: ${arg}`);
    const value = argv[++i];
    if (!value || value.startsWith("--")) throw new Error(`${arg} requires a value`);
    options[{ "--stage": "stage", "--hub": "hub", "--password-file": "passwordFile", "--out": "out" }[arg]] = value;
  }
  if (options.stage !== "detail") throw new Error("detail contract checker only accepts --stage detail");
  for (const key of ["hub", "passwordFile", "out"]) if (!options[key]) throw new Error(`--${key === "passwordFile" ? "password-file" : key} is required`);
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
  await session.evaluate(`(() => {
    const username = document.getElementById("cp-login-username");
    const passwordInput = document.getElementById("cp-login-password");
    username.value = "admin";
    passwordInput.value = ${JSON.stringify(password)};
    username.dispatchEvent(new Event("input", {bubbles:true}));
    passwordInput.dispatchEvent(new Event("input", {bubbles:true}));
    username.closest("form").requestSubmit();
    return true;
  })()`);
  await waitFor(session, `Boolean(localStorage.getItem("cp_session"))`, "authenticated session token");
  await waitFor(session, `location.hash === "#/" && Boolean(document.querySelector(".cp-systems-card"))`, "overview after login");
}

async function loadDetail(session, hub, hostID, lang, theme, width, height) {
  await session.setViewport(width, height);
  await session.evaluate(`(() => {
    localStorage.setItem("cp_lang", ${JSON.stringify(lang)});
    localStorage.setItem("cp_theme", ${JSON.stringify(theme)});
    localStorage.setItem("cp_host_layout", "2");
    window.scrollTo(0, 0);
    return true;
  })()`);
  await session.navigate(`${hub}/?cp_verify=${lang}-${theme}-${width}#/host/${encodeURIComponent(hostID)}`);
  await waitFor(session, `location.hash === ${JSON.stringify(`#/host/${encodeURIComponent(hostID)}`)} && document.documentElement.lang === ${JSON.stringify(lang)} && document.documentElement.classList.contains("dark") === ${theme === "dark"} && Boolean(document.querySelector(".cp-host-detail-header h1"))`, `detail header for ${hostID}`);
}

async function apiJSON(session, path) {
  return session.evaluate(`(async () => {
    const token = localStorage.getItem("cp_session") || "";
    const response = await fetch(${JSON.stringify(path)}, {headers:{Authorization:"Bearer " + token}});
    let body;
    try { body = await response.json(); } catch { body = {}; }
    return {status:response.status, body};
  })()`);
}

async function getSummary(session, hostID) {
  return apiJSON(session, `/api/v1/hosts/${encodeURIComponent(hostID)}`);
}

async function getInventory(session, hostID) {
  return apiJSON(session, `/api/v1/hosts/${encodeURIComponent(hostID)}/inventory`);
}

async function getSeriesStats(session, hostID, range) {
  const result = await apiJSON(session, `/api/v1/hosts/${encodeURIComponent(hostID)}/metrics?range=${encodeURIComponent(range)}`);
  if (result.status !== 200) return { status:result.status, error:result.body.error || "", code:result.body.code || "" };
  const series = result.body;
  const fields = Object.fromEntries(SERIES_FIELDS.map((field) => {
    const values = Array.isArray(series[field]) ? series[field] : [];
    return [field, {length:values.length, finite:values.every((value) => typeof value === "number" && Number.isFinite(value))}];
  }));
  const timestamps = Array.isArray(series.ts) ? series.ts : [];
  return {
    status:result.status,
    hostID:series.host_id,
    resolution:series.resolution,
    from:series.from,
    to:series.to,
    pointCount:timestamps.length,
    fields,
    firstTS:timestamps[0] ?? null,
    lastTS:timestamps.at(-1) ?? null,
    monotonic:timestamps.every((value, index) => index === 0 || value >= timestamps[index - 1]),
    last:Object.fromEntries(SERIES_FIELDS.map((field) => [field, Array.isArray(series[field]) ? (series[field].at(-1) ?? null) : null])),
  };
}

const EXTRACT_DETAIL = `(() => {
  const header = document.querySelector(".cp-host-detail-header");
  const heading = header?.querySelector("h1");
  const titleCopy = heading?.closest(".cp-detail-title-copy");
  const headingTextNodes = [];
  if (heading) {
    const walker = document.createTreeWalker(heading, NodeFilter.SHOW_TEXT);
    let textNode;
    while ((textNode = walker.nextNode())) headingTextNodes.push(textNode);
  }
  const headingRange = heading ? document.createRange() : null;
  if (heading && headingRange) headingRange.selectNodeContents(heading);
  const rangeLineTops = (range) => Array.from(range?.getClientRects() || []).map((rect) => Math.round(rect.top * 10) / 10);
  let suffix01LineTops = [];
  if (heading?.textContent.endsWith("-01") && headingTextNodes.length) {
    const lastNode = headingTextNodes.at(-1);
    if (lastNode.textContent.length >= 2) {
      const suffixRange = document.createRange();
      suffixRange.setStart(lastNode, lastNode.textContent.length - 2);
      suffixRange.setEnd(lastNode, lastNode.textContent.length);
      suffix01LineTops = rangeLineTops(suffixRange);
    }
  }
  const headingRect = heading?.getBoundingClientRect();
  const titleCopyRect = titleCopy?.getBoundingClientRect();
  const hostnameBreaks = Array.from(heading?.querySelectorAll("wbr") || []).map((node) => ({
    ariaHidden:node.getAttribute("aria-hidden") === "true",
    precedingSeparator:(node.previousSibling?.textContent || "").slice(-1),
  }));
  const range = header?.querySelector("#cp-host-range");
  const history = document.querySelector(".cp-detail-history");
  const resourceSection = document.querySelector(".cp-detail-resource-section");
  const chartCards = Array.from(document.querySelectorAll(".cp-chart-card")).map((card) => {
    const wrapper = card.querySelector(".cp-chart-wrap");
    return {
      title:card.querySelector("h2")?.textContent.trim() || "",
      labelledBy:card.getAttribute("aria-labelledby") || "",
      ariaTargetExists:Boolean(card.getAttribute("aria-labelledby") && document.getElementById(card.getAttribute("aria-labelledby"))),
      ariaLabel:wrapper?.getAttribute("aria-label") || "",
      summary:card.querySelector(".cp-detail-chart-summary,.cp-sr-only-summary")?.textContent.trim() || "",
      canvasCount:wrapper?.querySelectorAll("canvas").length || 0,
      noDataText:Array.from(card.querySelectorAll("p")).map((p) => p.textContent.trim()),
    };
  });
  const services = document.querySelector(".cp-detail-services");
  const servicesData = document.querySelector(".cp-services-card");
  const serviceSections = servicesData ? Array.from(servicesData.querySelectorAll(".cp-services-section")).map((section) => ({
    heading:section.querySelector("h3")?.textContent.trim() || "",
    rows:Array.from(section.querySelectorAll("tbody tr")).map((tr) => Array.from(tr.querySelectorAll("td")).map((td) => td.textContent.trim())),
    text:section.textContent.trim(),
  })) : [];
  const layout = header?.querySelector(".cp-layout-toggle");
  const back = Array.from(document.querySelectorAll("a.cp-back-link")).find((link) => link.getAttribute("href") === "#/");
  const editLimits = Array.from(document.querySelectorAll('.cp-egress-host a[href^="#/settings/limits?host="]')).find((link) => link.getAttribute("href").includes("host="));
  const labelledSections = Array.from(document.querySelectorAll(".cp-detail-history,.cp-detail-resource-section,.cp-detail-traffic-card,.cp-detail-services,.cp-detail-disks,.cp-chart-card")).map((section) => {
    const labelledBy = section.getAttribute("aria-labelledby") || "";
    return {className:section.className || "",labelledBy,targetExists:Boolean(labelledBy && document.getElementById(labelledBy))};
  });
  const metadata = Array.from(header?.querySelectorAll(".cp-detail-meta-pair") || []).map((pair) => ({
    label:pair.querySelector("dt")?.textContent.trim() || "",
    value:pair.querySelector("dd")?.textContent.trim() || "",
  }));
  const identityBadges = header?.querySelector(".cp-detail-identity-badges")?.textContent.trim() || "";
  const resources = Array.from(resourceSection?.querySelectorAll(".cp-detail-resource[data-detail-resource]") || []).map((resource) => ({
    key:resource.getAttribute("data-detail-resource") || "",
    label:resource.querySelector("h3")?.textContent.trim() || "",
    value:resource.querySelector(".cp-detail-resource-value")?.textContent.trim() || "",
    detail:resource.querySelector(".cp-detail-resource-description")?.textContent.trim() || "",
    hasUnknownTrack:Boolean(resource.querySelector(".cp-detail-empty-track")),
    unknownTrack:(() => {
      const track = resource.querySelector(".cp-detail-empty-track");
      if (!track) return null;
      const style = getComputedStyle(track);
      const rect = track.getBoundingClientRect();
      return {height:style.height,width:rect.width,flexGrow:style.flexGrow,left:rect.left,right:rect.right};
    })(),
  }));
  const networkReadings = Array.from(resourceSection?.querySelectorAll(".cp-detail-network-reading") || []).map((item) => ({
    direction:item.getAttribute("data-detail-network") || "",
    label:item.querySelector("span")?.textContent.trim() || "",
    value:item.querySelector("strong")?.textContent.trim() || "",
  }));
  const documentWidth = Math.max(document.documentElement.scrollWidth, document.body.scrollWidth);
  const overflowCandidates = documentWidth > innerWidth + 1 ? Array.from(document.body.querySelectorAll("*"))
    .map((node) => {
      const rect = node.getBoundingClientRect();
      const style = getComputedStyle(node);
      return {tag:node.tagName.toLowerCase(),id:node.id || "",className:typeof node.className === "string" ? node.className : "",
        left:rect.left,right:rect.right,width:rect.width,scrollWidth:node.scrollWidth,clientWidth:node.clientWidth,overflowX:style.overflowX};
    })
    .filter((node) => node.width > 0 && (node.right > innerWidth + 1 || node.left < -1))
    .sort((a,b) => b.right - a.right)
    .slice(0,12) : [];
  return {
    href:location.href,
    hash:location.hash,
    lang:document.documentElement.lang,
    viewportWidth:innerWidth,
    documentWidth,
    documentElementScrollWidth:document.documentElement.scrollWidth,
    documentElementClientWidth:document.documentElement.clientWidth,
    bodyScrollWidth:document.body.scrollWidth,
    overflowCandidates,
    hostnameHeading:heading ? {
      text:heading.textContent,
      ariaLabel:heading.getAttribute("aria-label") || "",
      width:headingRect?.width || 0,
      titleCopyWidth:titleCopyRect?.width || 0,
      clientWidth:heading.clientWidth,
      scrollWidth:heading.scrollWidth,
      left:headingRect?.left || 0,
      right:headingRect?.right || 0,
      lineTops:rangeLineTops(headingRange),
      wbrCount:hostnameBreaks.length,
      wbrs:hostnameBreaks,
      suffix01LineTops,
    } : null,
    title:header?.querySelector("h1")?.textContent.trim() || "",
    identityBadges,
    metadata,
    status:header?.querySelector(".cp-detail-identity-badges .cp-status-pill")?.textContent.trim() || "",
    offlineNote:header?.querySelector(".cp-detail-last-seen")?.textContent.trim() || "",
    rangeLabel:range?.getAttribute("aria-label") || document.querySelector('label[for="cp-host-range"]')?.textContent.trim() || "",
    rangeValue:range?.value || "",
    rangeOptions:Array.from(range?.options || []).map((option) => ({value:option.value,label:option.textContent.trim()})),
    layoutLabel:layout?.getAttribute("aria-label") || "",
    layoutButtons:Array.from(layout?.querySelectorAll("button") || []).map((button) => ({label:button.getAttribute("aria-label") || "",pressed:button.getAttribute("aria-pressed") || ""})),
    chartGridClass:document.querySelector(".cp-charts-grid")?.className || "",
    chartCards,
    chartWrapCount:document.querySelectorAll(".cp-chart-wrap").length,
    canvasCount:document.querySelectorAll(".cp-chart-wrap canvas").length,
    disksRows:Array.from(document.querySelectorAll(".cp-disks-host tbody tr")).map((row) => row.textContent.trim()),
    resourceHeading:resourceSection?.querySelector("h2")?.textContent.trim() || "",
    resources,
    networkReadings,
    egressText:document.querySelector(".cp-egress-host")?.textContent || "",
    servicesExists:Boolean(services),
    servicesDataExists:Boolean(servicesData),
    servicesHeading:services?.querySelector("h2")?.textContent.trim() || "",
    servicesStateText:services?.textContent.trim() || "",
    serviceSections,
    labelledSections,
    backHref:back?.getAttribute("href") || "",
    backText:back?.textContent.trim() || "",
    editLimitsHref:editLimits?.getAttribute("href") || "",
    tabCount:document.querySelectorAll('[role="tab"],[role="tablist"]').length,
  };
})()`;

async function extractDetail(session) {
  return session.evaluate(EXTRACT_DETAIL);
}

function rangeLabels(catalog) {
  return [
    {value:"1h",label:catalog.host.range_1h},
    {value:"6h",label:catalog.host.range_6h},
    {value:"24h",label:catalog.host.range_24h},
    {value:"7d",label:catalog.host.range_7d},
    {value:"30d",label:catalog.host.range_30d},
  ];
}

const chartSpecs = (catalog) => [
  {title:catalog.host.chart_cpu_title, fields:["cpu"], summary:(s) => catalog.host.chart_cpu_summary.replace("{value}", Number(s.last.cpu).toFixed(1))},
  {title:catalog.host.chart_memory_title, fields:["mem"], summary:(s) => catalog.host.chart_memory_summary.replace("{value}", Number(s.last.mem).toFixed(1))},
  {title:catalog.host.chart_disk_title, fields:["disk"], summary:(s) => catalog.host.chart_disk_summary.replace("{value}", Number(s.last.disk).toFixed(1))},
  {title:catalog.host.chart_bandwidth_title, fields:["net_rx","net_tx"], summary:(s) => catalog.host.chart_bandwidth_summary.replace("{rx}", formatBytes(Number(s.last.net_rx))).replace("{tx}", formatBytes(Number(s.last.net_tx)))},
  {title:catalog.host.chart_disk_io_title, fields:["disk_read","disk_write"], summary:(s) => catalog.host.chart_disk_io_summary.replace("{read}", formatBytes(Number(s.last.disk_read))).replace("{write}", formatBytes(Number(s.last.disk_write)))},
  {title:catalog.host.chart_load_title, fields:["load1"], summary:(s) => catalog.host.chart_load_summary.replace("{value}", formatLoad(Number(s.last.load1)))}
];

function seriesShapePass(series, hostID, range, requireSamples) {
  return series.status === 200 && series.hostID === hostID && series.resolution === RANGE_RESOLUTION[range] &&
    Object.values(series.fields || {}).every((field) => field.length === series.pointCount && field.finite) &&
    series.monotonic && (series.pointCount > 0) === requireSamples &&
    (series.pointCount === 0 || (series.firstTS >= series.from && series.lastTS <= series.to));
}

async function chartReady(session, hasPoints, catalog) {
  const expression = hasPoints
    ? `document.querySelectorAll(".cp-chart-card").length === 6 && document.querySelectorAll(".cp-chart-wrap").length === 6 && Array.from(document.querySelectorAll(".cp-chart-wrap canvas")).length >= 6`
    : `document.querySelectorAll(".cp-chart-card").length === 6 && Array.from(document.querySelectorAll(".cp-chart-card")).every((card) => Array.from(card.querySelectorAll("p")).some((p) => p.textContent.trim() === ${JSON.stringify(catalog.host.no_data_for_range)}))`;
  await waitFor(session, expression, hasPoints ? "six chart canvases" : "six no-data chart messages");
}

async function testLayouts(session, catalog) {
  const selectLayout = async (label) => {
    const clicked = await session.evaluate(`(() => {
      const button = Array.from(document.querySelectorAll(".cp-layout-toggle button")).find((item) => item.getAttribute("aria-label") === ${JSON.stringify(label)});
      if (!button) return false;
      button.click();
      return true;
    })()`);
    if (!clicked) throw new Error(`layout control not found: ${label}`);
    return extractDetail(session);
  };
  const one = await selectLayout(catalog.host.one_column);
  await selectLayout(catalog.host.two_columns);
  await session.evaluate("new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
  await sleep(180);
  const two = await extractDetail(session);
  return {
    oneColumn:one.chartGridClass.includes("cp-charts-grid-cols-1") && one.layoutButtons.some((button) => button.label === catalog.host.one_column && button.pressed === "true"),
    twoColumn:two.chartGridClass.includes("cp-charts-grid-cols-2") && two.layoutButtons.some((button) => button.label === catalog.host.two_columns && button.pressed === "true"),
    oneState:{className:one.chartGridClass,buttons:one.layoutButtons},
    twoState:{className:two.chartGridClass,buttons:two.layoutButtons},
  };
}

async function unusedLegacyRangeSuite(session, hostID, catalog, record, report) {
  const rangeResults = [];
  for (const range of RANGE_LIST) {
    const api = await getSeriesStats(session, hostID, range);
    const apiOK = seriesShapePass(api, hostID, range, true);
    record(`range ${range}: API returns aligned ${RANGE_RESOLUTION[range]}s series samples`, apiOK, api);
    if (!apiOK) throw new Error(`detail metrics API did not return expected range data for ${range}`);

    if (range !== "1h") {
      await session.evaluate("performance.clearResourceTimings(); true");
      const changed = await session.evaluate(`(() => {
        const select = document.querySelector("#cp-host-range");
        if (!select) return false;
        select.value = ${JSON.stringify(range)};
        select.dispatchEvent(new Event("change", {bubbles:true}));
        return true;
      })()`);
      if (!changed) throw new Error("time-range select is missing");
      await waitFor(session, `performance.getEntriesByType("resource").some((entry) => {
        try { const url = new URL(entry.name, location.href); return url.pathname.endsWith("/metrics") && url.searchParams.get("range") === ${JSON.stringify(range)}; }
        catch { return false; }
      })`, `detail UI metrics request for ${range}`);
    }

    const expected = chartSpecs(catalog).map((spec) => spec.summary(api));
    await waitFor(session, `(() => {
      const charts = Array.from(document.querySelectorAll(".cp-chart-card"));
      const cpu = charts.find((card) => card.querySelector("h2")?.textContent.trim() === ${JSON.stringify(catalog.host.chart_cpu_title)});
      return document.querySelector("#cp-host-range")?.value === ${JSON.stringify(range)} && cpu?.querySelector(".cp-chart-wrap")?.getAttribute("aria-label") === ${JSON.stringify(expected[0])};
    })()`, `charts refresh with ${range} API data`);
    const dom = await extractDetail(session);
    const expectedTitles = chartSpecs(catalog).map((spec) => spec.title);
    const summariesMatch = dom.chartCards.length === 6 && dom.chartCards.every((chart, index) => chart.title === expectedTitles[index] &&
      chart.ariaLabel === expected[index] && chart.summary === expected[index] && chart.canvasCount > 0);
    record(`range ${range}: UI selection, all chart summaries, percent/byte-rate/load field mappings and chart count match API series`,
      dom.rangeValue === range && summariesMatch && dom.chartWrapCount === 6 && dom.canvasCount >= 6,
      {range:dom.rangeValue,api:{pointCount:api.pointCount,resolution:api.resolution,last:api.last},titles:dom.chartCards.map((chart) => chart.title),aria:dom.chartCards.map((chart) => chart.ariaLabel),summariesMatch,chartWrapCount:dom.chartWrapCount,canvasCount:dom.canvasCount,
        units:{cpu_mem_disk:"percent",net_rx_net_tx_disk_read_disk_write:"bytes/second via IEC /s chart formatter",load1:"load average"}});
    rangeResults.push({range,pointCount:api.pointCount,resolution:api.resolution,last:api.last});
  }
  report.rangeResults = rangeResults;
}

async function unusedLegacyThemeLifecycleCheck(session, catalog, record) {
  const changed = await session.evaluate(`(() => {
    window.__detailOldChartWraps = Array.from(document.querySelectorAll(".cp-chart-wrap"));
    const button = document.querySelector(".cp-navbar-theme-btn");
    if (!button) return false;
    button.click();
    return true;
  })()`);
  if (!changed) throw new Error("navbar theme control is missing for chart lifecycle check");
  await waitFor(session, `localStorage.getItem("cp_theme") === "system" && document.querySelectorAll(".cp-chart-wrap").length === 6`, "chart rebuild after theme event");
  const state = await session.evaluate(`(() => ({
    storedTheme:localStorage.getItem("cp_theme"),
    oldWrappersDetached:Array.from(window.__detailOldChartWraps || []).every((node) => !node.isConnected),
    chartWrapCount:document.querySelectorAll(".cp-chart-wrap").length,
    canvasCount:document.querySelectorAll(".cp-chart-wrap canvas").length,
    titles:Array.from(document.querySelectorAll(".cp-chart-card h2")).map((h) => h.textContent.trim()),
  }))()`);
  const expectedTitles = chartSpecs(catalog).map((spec) => spec.title);
  record("theme event destroys old chart DOM and recreates six current charts", state.storedTheme === "system" && state.oldWrappersDetached &&
    state.chartWrapCount === 6 && state.canvasCount >= 6 && JSON.stringify(state.titles) === JSON.stringify(expectedTitles), state);
  return state;
}

async function unusedLegacyKeyboardBacklinkCheck(session, hub, catalog, record) {
  const focused = await session.evaluate(`(() => {
    const link = document.querySelector('a.cp-back-link[href="#/"]');
    if (!link) return null;
    link.focus();
    const style = getComputedStyle(link);
    return {href:link.getAttribute("href"),text:link.textContent.trim(),outlineStyle:style.outlineStyle,outlineWidth:style.outlineWidth};
  })()`);
  if (!focused?.href) throw new Error("back-to-overview link is missing");
  await session.pressKey("\uE007");
  await waitFor(session, `location.hash === "#/" && Boolean(document.querySelector(".cp-systems-card"))`, "detail backlink to overview");
  const result = await session.evaluate(`({hash:location.hash,title:document.querySelector("main h1")?.textContent.trim() || ""})`);
  record("keyboard Enter on detail backlink returns to overview", focused.href === "#/" && focused.text === catalog.host.back_to_overview && result.hash === "#/" && result.title === catalog.overview.page_title,
    {focused,result});
  return {focused,result};
}

async function testInventoryChipLayout(session, inventory, catalog, viewportWidth, outDir, report, record, captureTarget = false) {
  const containers = inventory?.docker?.containers || [];
  const expectedTargets = containers.filter((container) => container.state === "running" || container.state === "exited");
  const dom = await session.evaluate(`(() => {
    const card = document.querySelector(".cp-services-card");
    const docker = Array.from(card?.querySelectorAll(".cp-services-section") || []).find((section) =>
      section.querySelector("h3")?.textContent.trim() === ${JSON.stringify(catalog.host.docker_containers)});
    const wrap = docker?.querySelector(".cp-table-wrap");
    const wrapStyle = wrap ? getComputedStyle(wrap) : null;
    const rows = Array.from(docker?.querySelectorAll("tbody tr") || []).map((row) => {
      const cells = Array.from(row.cells || []);
      const chip = cells[3]?.querySelector(".cp-chip");
      const style = chip ? getComputedStyle(chip) : null;
      let textRects = [];
      if (chip) {
        const range = document.createRange();
        range.selectNodeContents(chip);
        textRects = Array.from(range.getClientRects()).map((rect) => ({top:Math.round(rect.top * 10) / 10,left:rect.left,right:rect.right,height:rect.height}));
      }
      const rect = chip?.getBoundingClientRect();
      return {
        name:(cells[0]?.textContent || "").trim(), state:(cells[3]?.textContent || "").trim(),
        chipText:(chip?.textContent || "").trim(), chipClass:chip?.className || "",
        whiteSpace:style?.whiteSpace || "", overflowWrap:style?.overflowWrap || "",
        chipWidth:rect?.width || 0, chipHeight:rect?.height || 0,
        textLines:[...new Set(textRects.map((item) => item.top))].length,
        textRects,
      };
    });
    return {
      wrap:wrap ? {
        exists:true, role:wrap.getAttribute("role") || "", label:wrap.getAttribute("aria-label") || "",
        tabIndex:wrap.tabIndex, overflowX:wrapStyle?.overflowX || "", scrollWidth:wrap.scrollWidth,
        clientWidth:wrap.clientWidth, scrollLeft:wrap.scrollLeft,
      } : {exists:false},
      rows,
    };
  })()`);
  const actualByName = new Map(dom.rows.map((row) => [row.name, row]));
  const targetResults = expectedTargets.map((expected) => {
    const actual = actualByName.get(expected.name);
    const expectedClass = expected.state === "running" ? "cp-chip-ok" : "cp-chip-other";
    return {
      name:expected.name, expectedState:expected.state, actual,
      pass:Boolean(actual && actual.state === expected.state && actual.chipText === expected.state &&
        actual.chipClass.split(/\s+/).includes(expectedClass) && actual.whiteSpace === "nowrap" &&
        actual.overflowWrap === "normal" && actual.textLines === 1 && actual.chipWidth > 0 && actual.chipHeight > 0),
    };
  });
  const scrollRegionReady = dom.wrap.exists && dom.wrap.role === "region" && dom.wrap.label === catalog.host.docker_containers &&
    dom.wrap.tabIndex === 0 && ["auto", "scroll"].includes(dom.wrap.overflowX);
  const hasScrollRange = dom.wrap.scrollWidth > dom.wrap.clientWidth + 1;
  const focusable = await session.evaluate(`(() => {
    const section = Array.from(document.querySelectorAll(".cp-services-card .cp-services-section")).find((item) =>
      item.querySelector("h3")?.textContent.trim() === ${JSON.stringify(catalog.host.docker_containers)});
    const wrap = section?.querySelector(".cp-table-wrap");
    if (!wrap) return false;
    wrap.scrollLeft = 0;
    wrap.focus();
    return document.activeElement === wrap;
  })()`);
  let keyboardMoved = false;
  if (scrollRegionReady && focusable && hasScrollRange) {
    await session.pressKey("\uE014"); // ArrowRight scrolls the focused overflow region without a pointer.
    await sleep(80);
    keyboardMoved = await session.evaluate(`(() => {
      const section = Array.from(document.querySelectorAll(".cp-services-card .cp-services-section")).find((item) =>
        item.querySelector("h3")?.textContent.trim() === ${JSON.stringify(catalog.host.docker_containers)});
      const wrap = section?.querySelector(".cp-table-wrap");
      return Boolean(wrap && wrap.scrollLeft > 0 && document.activeElement === wrap);
    })()`);
  }
  record("768px inventory running/exited chips match API state and remain nowrap single-line chips",
    expectedTargets.some((container) => container.state === "running") && expectedTargets.some((container) => container.state === "exited") &&
      targetResults.length === expectedTargets.length && targetResults.every((result) => result.pass),
    {expectedTargetCount:expectedTargets.length,results:targetResults});
  const keyboardScrollExpected = viewportWidth === 360;
  const keyboardScrollPass = scrollRegionReady && focusable && (hasScrollRange ? keyboardMoved : !keyboardScrollExpected);
  record(`${viewportWidth}px Docker inventory table exposes a labelled keyboard-focusable horizontal scroll region${hasScrollRange ? " and ArrowRight scrolls it" : " (all columns fit at this width)"}`,
    keyboardScrollPass,
    {wrap:dom.wrap,focusable,hasScrollRange,keyboardMoved,keyboardScrollExpected,keyboardScrollNotRequired:!keyboardScrollExpected});
  report.inventoryChipGeometry ||= [];
  report.inventoryChipGeometry.push({viewport:`${viewportWidth}px`,wrap:dom.wrap,focusable,hasScrollRange,keyboardMoved,keyboardScrollExpected,results:targetResults});
  if (captureTarget) {
    await session.evaluate(`(() => {
      const section = Array.from(document.querySelectorAll(".cp-services-card .cp-services-section")).find((item) =>
        item.querySelector("h3")?.textContent.trim() === ${JSON.stringify(catalog.host.docker_containers)});
      section?.querySelector(".cp-table-wrap")?.scrollIntoView({block:"center",inline:"nearest"});
      return true;
    })()`);
    const screenshot = join(outDir, "detail-inventory-chips-en-dark-768x1024.png");
    await session.screenshot(screenshot, "viewport");
    report.screenshots.push(screenshot);
    report.targetedScreenshots ||= [];
    report.targetedScreenshots.push(screenshot);
  }
}

async function checkOneHost(session, hostID, kind, hub, lang, theme, width, height, catalog, outDir, report, record) {
  await loadDetail(session, hub, hostID, lang, theme, width, height);
  const [hostResponse, metrics, inventory] = await Promise.all([
    getSummary(session, hostID),
    getSeriesStats(session, hostID, "1h"),
    getInventory(session, hostID),
  ]);
  if (hostResponse.status !== 200) throw new Error(`GET host summary ${hostID} returned ${hostResponse.status}`);
  const summary = hostResponse.body;
  const key = `${kind}-${lang}-${theme}-${width}x${height}`;
  const expectedStatus = summary.status === "up" ? catalog.host.detail_online : summary.status === "down" ? catalog.host.detail_offline : catalog.host.detail_unknown;
  const expectedMetrics = Boolean(summary.latest);
  await chartReady(session, expectedMetrics, catalog);
  if (inventory.status === 200) {
    await waitFor(session, `Boolean(document.querySelector(".cp-services-card"))`, `${hostID} services inventory card`);
  } else {
    await waitFor(session, `performance.getEntriesByType("resource").filter((entry) => {
      try { return new URL(entry.name, location.href).pathname.endsWith(${JSON.stringify(`/api/v1/hosts/${encodeURIComponent(hostID)}/inventory`)}); }
      catch { return false; }
    }).length >= 2`, `${hostID} inventory requests resolve`);
    await sleep(80);
  }
  const readyState = await extractDetail(session);

  record(`${key}: detail heading, route, locale and status match API`, readyState.hash === `#/host/${encodeURIComponent(hostID)}` &&
    readyState.title === summary.host.hostname && readyState.lang === lang && readyState.status === expectedStatus,
    {hostID,apiHost:summary.host.hostname,renderedTitle:readyState.title,hash:readyState.hash,apiStatus:summary.status,renderedStatus:readyState.status});
  if (width === 360) {
    const heading = readyState.hostnameHeading;
    const expectedName = summary.host.hostname;
    const separatorCount = (expectedName.match(/[._-]/g) || []).length;
    const safeBreaks = heading?.wbrCount === separatorCount && heading.wbrs.every((item) =>
      item.ariaHidden && [".", "-", "_"].includes(item.precedingSeparator));
    const fullWidth = heading && heading.titleCopyWidth > 0 && heading.width >= heading.titleCopyWidth - 1;
    const noHeadingOverflow = heading && heading.left >= -1 && heading.right <= readyState.viewportWidth + 1 &&
      heading.scrollWidth <= heading.clientWidth + 1;
    const suffixCheckApplicable = expectedName.endsWith("-01");
    const noLoneSuffixOne = !suffixCheckApplicable || heading?.suffix01LineTops.length === 1;
    const mobileHeadingPass = heading?.text === expectedName && heading.ariaLabel === expectedName && safeBreaks &&
      fullWidth && noHeadingOverflow && noLoneSuffixOne && heading.lineTops.length > 0;
    record(`${key}: 360px hostname heading spans full copy width, preserves exact API/ARIA text and separator wbrs without overflow or an orphan trailing 1`, mobileHeadingPass,
      {hostID,expectedName,heading,separatorCount,safeBreaks,fullWidth,noHeadingOverflow,suffixCheckApplicable,noLoneSuffixOne});
    report.mobileHeadingGeometry ||= [];
    report.mobileHeadingGeometry.push({hostID,kind,lang,theme,pass:mobileHeadingPass,separatorCount,safeBreaks,fullWidth,noHeadingOverflow,suffixCheckApplicable,noLoneSuffixOne,heading});
  }
  const unknown = catalog.host.detail_unknown;
  const metaValue = (label) => readyState.metadata.find((pair) => pair.label === label)?.value || "";
  const expectedPlatform = `${summary.host.platform || summary.host.os || unknown} ${summary.host.platform_version || ""}`.trim();
  const expectedCores = typeof summary.host.cpu_cores === "number" && summary.host.cpu_cores > 0 ? String(summary.host.cpu_cores) : unknown;
  const expectedProcessor = `${summary.host.cpu_model || unknown} · ${catalog.host.detail_cores.replace("{cores}", expectedCores)}`;
  const expectedUptime = summary.latest ? formatDuration(summary.latest.uptime_seconds) : "–";
  record(`${key}: HostSummary identity/latest metadata matches localized detail fields`,
    readyState.identityBadges.includes(summary.host.provider === "other" ? catalog.host.provider_other : summary.host.provider.toUpperCase()) &&
    metaValue(catalog.host.detail_platform) === expectedPlatform && metaValue(catalog.host.detail_architecture) === (summary.host.arch || unknown) &&
    metaValue(catalog.host.detail_processor) === expectedProcessor && metaValue(catalog.host.detail_kernel) === (summary.host.kernel_version || unknown) &&
    metaValue(catalog.host.detail_agent) === (summary.host.agent_version || unknown) && metaValue(catalog.host.detail_uptime) === expectedUptime,
    {hostID,api:{hostname:summary.host.hostname,os:summary.host.os,platform:summary.host.platform,arch:summary.host.arch,kernel:summary.host.kernel_version,cpu:summary.host.cpu_model,cores:summary.host.cpu_cores,agent:summary.host.agent_version,uptime:summary.latest?.uptime_seconds},identityBadges:readyState.identityBadges,metadata:readyState.metadata});
  record(`${key}: range control has localized options and starts at 1h`, readyState.rangeLabel === catalog.host.time_range && readyState.rangeValue === "1h" &&
    JSON.stringify(readyState.rangeOptions) === JSON.stringify(rangeLabels(catalog)),
    {rangeLabel:readyState.rangeLabel,rangeValue:readyState.rangeValue,rangeOptions:readyState.rangeOptions});
  record(`${key}: layout controls start in two-column mode`, readyState.layoutLabel === catalog.host.chart_layout &&
    readyState.layoutButtons.some((button) => button.label === catalog.host.one_column && button.pressed === "false") &&
    readyState.layoutButtons.some((button) => button.label === catalog.host.two_columns && button.pressed === "true") && readyState.chartGridClass.includes("cp-charts-grid-cols-2"),
    {layoutLabel:readyState.layoutLabel,buttons:readyState.layoutButtons,gridClass:readyState.chartGridClass});
  const layoutResult = await testLayouts(session, catalog);
  record(`${key}: one/two-column controls change chart layout and aria state`, layoutResult.oneColumn && layoutResult.twoColumn, layoutResult);
  const afterLayout = await extractDetail(session);
  record(`${key}: backlink and edit-limits action are visible`, afterLayout.backHref === "#/" &&
    afterLayout.backText === catalog.host.back_to_overview && afterLayout.editLimitsHref === `#/settings/limits?host=${encodeURIComponent(hostID)}`,
    {backHref:afterLayout.backHref,backText:afterLayout.backText,editLimitsHref:afterLayout.editLimitsHref});
  record(`${key}: no horizontal overflow in detail content`, afterLayout.documentWidth <= afterLayout.viewportWidth,
    {viewportWidth:afterLayout.viewportWidth,documentWidth:afterLayout.documentWidth,documentElementScrollWidth:afterLayout.documentElementScrollWidth,
      documentElementClientWidth:afterLayout.documentElementClientWidth,bodyScrollWidth:afterLayout.bodyScrollWidth,overflowCandidates:afterLayout.overflowCandidates});
  record(`${key}: monthly traffic fields use API byte totals`, readyState.egressText.includes(formatBytes(summary.egress.tx_bytes)) &&
    readyState.egressText.includes(formatBytes(summary.egress.rx_bytes)), {apiOutbound:summary.egress.tx_bytes,apiInbound:summary.egress.rx_bytes,renderedText:readyState.egressText.slice(0,260)});
  const availableTabs = readyState.tabCount;
  record(`${key}: named detail sections resolve their accessible heading IDs`, readyState.labelledSections.length >= 10 &&
    readyState.labelledSections.every((section) => section.targetExists), {labelledSections:readyState.labelledSections});
  record(`${key}: host.js range/layout controls are not represented as tabs`, availableTabs === 0,
    {tabCount:availableTabs,note:"The inspected host.js detail contract has a native range select and a chart-layout group; no tablist/tab controls exist."});

  const expectedTitles = chartSpecs(catalog).map((spec) => spec.title);
  record(`${key}: six localized chart headings match`, readyState.chartCards.length === 6 &&
    JSON.stringify(readyState.chartCards.map((chart) => chart.title)) === JSON.stringify(expectedTitles),
    {expectedTitles,actualTitles:readyState.chartCards.map((chart) => chart.title)});
  const expectedResourceHeading = summary.latest
    ? (summary.status === "up" ? catalog.host.detail_latest_metrics : catalog.host.detail_last_reported)
    : catalog.host.no_samples_yet;
  record(`${key}: latest/no-sample resource snapshot labels and readings match HostSummary`, readyState.resourceHeading === expectedResourceHeading &&
    (summary.latest
      ? readyState.resources.some((resource) => resource.key === "cpu" && resource.value === formatPercent(summary.latest.cpu_percent)) &&
        readyState.resources.some((resource) => resource.key === "memory" && resource.value === formatPercent(summary.latest.mem_used_percent)) &&
        readyState.resources.some((resource) => resource.key === "disk" && resource.value === formatPercent(summary.latest.disk_used_percent)) &&
        readyState.networkReadings.some((reading) => reading.direction === "rx" && reading.label === catalog.host.detail_receive && reading.value === formatBitrate(summary.latest.net_rx_bps)) &&
        readyState.networkReadings.some((reading) => reading.direction === "tx" && reading.label === catalog.host.detail_send && reading.value === formatBitrate(summary.latest.net_tx_bps))
      : readyState.resources.filter((resource) => resource.key !== "network").every((resource) => resource.value === "–") && readyState.networkReadings.every((reading) => reading.value === "–")),
    {expectedHeading:expectedResourceHeading,actualHeading:readyState.resourceHeading,apiLatest:summary.latest,resources:readyState.resources,networkReadings:readyState.networkReadings});
  record(`${key}: 1h metrics API series schema/units align`, seriesShapePass(metrics, hostID, "1h", expectedMetrics),
    {hostID,range:"1h",status:metrics.status,resolution:metrics.resolution,pointCount:metrics.pointCount,fieldLengths:metrics.fields,last:metrics.last});
  if (summary.latest) {
    const latestMatches = metrics.lastTS === summary.latest.ts &&
      metrics.last.cpu === summary.latest.cpu_percent && metrics.last.mem === summary.latest.mem_used_percent &&
      metrics.last.disk === summary.latest.disk_used_percent && metrics.last.net_rx === summary.latest.net_rx_bps &&
      metrics.last.net_tx === summary.latest.net_tx_bps && metrics.last.disk_read === summary.latest.disk_read_bps &&
      metrics.last.disk_write === summary.latest.disk_write_bps && metrics.last.load1 === summary.latest.load1;
    record(`${key}: HostSummary.latest matches the final real 1h series point`, latestMatches,
      {summaryLatest:{ts:summary.latest.ts,cpu:summary.latest.cpu_percent,mem:summary.latest.mem_used_percent,disk:summary.latest.disk_used_percent,
        netRx:summary.latest.net_rx_bps,netTx:summary.latest.net_tx_bps,diskRead:summary.latest.disk_read_bps,diskWrite:summary.latest.disk_write_bps,load1:summary.latest.load1},
       seriesLast:{ts:metrics.lastTS,...metrics.last}});
  }

  if (!summary.latest) {
    const unknownTracks = readyState.resources.filter((resource) => resource.key !== "network").map((resource) => ({
      key:resource.key,hasTrack:resource.hasUnknownTrack,...(resource.unknownTrack || {}),
    }));
    record(`${key}: unknown CPU/memory/disk tracks retain their 6px non-growing geometry`, unknownTracks.length === 3 &&
      unknownTracks.every((track) => track.hasTrack && track.height === "6px" && track.flexGrow === "0" && track.width > 0), unknownTracks);
  }

  if (expectedMetrics) {
    const expectedSummaries = chartSpecs(catalog).map((spec) => spec.summary(metrics));
    record(`${key}: chart canvases and accessible summaries use real API series values`, readyState.chartCards.length === 6 &&
      readyState.chartCards.every((chart, index) => chart.ariaLabel === expectedSummaries[index] && chart.summary === expectedSummaries[index] && chart.canvasCount > 0) &&
      readyState.chartWrapCount === 6 && readyState.canvasCount >= 6,
      {expected:expectedSummaries,actual:readyState.chartCards.map((chart) => ({title:chart.title,aria:chart.ariaLabel,summary:chart.summary,canvases:chart.canvasCount})),chartWrapCount:readyState.chartWrapCount,canvasCount:readyState.canvasCount,
        units:{cpu_mem_disk:"percent",net_rx:`${formatBitrate(Number(metrics.last.net_rx))} (bytes/second)`,net_tx:`${formatBitrate(Number(metrics.last.net_tx))} (bytes/second)`,
          disk_read:`${formatBitrate(Number(metrics.last.disk_read))} (bytes/second)`,disk_write:`${formatBitrate(Number(metrics.last.disk_write))} (bytes/second)`,load1:"load average"}});
  } else {
    record(`${key}: empty API series renders six no-data placeholders without canvases`, readyState.chartCards.length === 6 &&
      readyState.chartCards.every((chart) => chart.noDataText.includes(catalog.host.no_data_for_range) && chart.canvasCount === 0) &&
      readyState.chartWrapCount === 0 && readyState.canvasCount === 0,
      {pointCount:metrics.pointCount,chartCards:readyState.chartCards.map((chart) => ({title:chart.title,noDataText:chart.noDataText,canvases:chart.canvasCount})),chartWrapCount:readyState.chartWrapCount});
  }

  const disks = summary.latest?.disks || [];
  record(`${key}: disk rows match latest HostSummary disks`, readyState.disksRows.length === disks.length,
    {apiDiskCount:disks.length,renderedRows:readyState.disksRows.length});
  if (kind === "inventory") {
    const docker = inventory.body?.docker;
    record(`${key}: real inventory services and listening ports match API fixture`, inventory.status === 200 && readyState.servicesExists && readyState.servicesDataExists &&
      readyState.servicesHeading === catalog.host.services_and_ports && docker?.status === "ok" && readyState.serviceSections.length === 2 &&
      readyState.serviceSections[0].rows.length === docker.containers.length && readyState.serviceSections[1].rows.length === inventory.body.ports.length &&
      readyState.serviceSections[0].rows.some((cells) => cells[0] === "web") && readyState.serviceSections[1].rows.some((cells) => cells.some((cell) => cell.includes("sshd"))),
      {apiStatus:inventory.status,dockerStatus:docker?.status,apiContainers:docker?.containers?.length,renderedContainers:readyState.serviceSections[0]?.rows.length,
        apiPorts:inventory.body?.ports?.length,renderedPorts:readyState.serviceSections[1]?.rows.length,sectionHeadings:readyState.serviceSections.map((section) => section.heading)});
  } else {
    record(`${key}: absent inventory returns no_inventory and localized pending Services state`, inventory.status === 404 && inventory.body?.code === "no_inventory" &&
      readyState.servicesExists && !readyState.servicesDataExists && readyState.servicesHeading === catalog.host.services_and_ports &&
      readyState.servicesStateText.includes(catalog.host.detail_inventory_pending) && readyState.serviceSections.length === 0,
      {inventoryStatus:inventory.status,code:inventory.body?.code,servicesExists:readyState.servicesExists,servicesDataExists:readyState.servicesDataExists,heading:readyState.servicesHeading,stateText:readyState.servicesStateText});
  }

  if (kind === "offline") {
    record(`${key}: offline detail shows localized status and last-seen state`, summary.status === "down" && readyState.status === catalog.host.detail_offline &&
      readyState.offlineNote.includes(catalog.host.detail_last_seen.replace("{relative}", "").trim()),
      {apiStatus:summary.status,offlineNote:readyState.offlineNote});
  }
  if (kind === "unknown-no-sample") {
    const expectedName = summary.host.hostname;
    record(`${key}: unknown/no-sample detail preserves hostname and localized unknown metadata`, !summary.latest && readyState.title === expectedName &&
      !summary.host.os && !summary.host.arch && !summary.host.agent_version && readyState.metadata.some((pair) => pair.value === unknown) && !metrics.pointCount &&
      readyState.resourceHeading === catalog.host.no_samples_yet,
      {hostname:expectedName,hostnameLength:expectedName.length,apiLatest:Boolean(summary.latest),metricsPoints:metrics.pointCount,metadata:readyState.metadata,resourceHeading:readyState.resourceHeading});
  }
  if (kind === "long-name-no-sample") {
    const expectedName = summary.host.hostname;
    record(`${key}: long no-sample hostname and metadata match the API fixture`, !summary.latest && metrics.pointCount === 0 &&
      expectedName.length >= 50 && readyState.title === expectedName && metaValue(catalog.host.detail_platform) === `${summary.host.platform} ${summary.host.platform_version}`.trim() &&
      readyState.resourceHeading === catalog.host.no_samples_yet,
      {hostname:expectedName,hostnameLength:expectedName.length,apiLatest:Boolean(summary.latest),metricsPoints:metrics.pointCount,metadata:readyState.metadata});
  }

  const screenshotPath = join(outDir, `detail-${kind}-${lang}-${theme}-${width}x${height}.png`);
  await session.screenshot(screenshotPath, "viewport");
  report.screenshots.push(screenshotPath);
  if (kind === "inventory" || width === 360) {
    await session.evaluate("window.scrollTo(0, 0); true");
    const fullPath = kind === "inventory"
      ? join(outDir, `detail-full-inventory-${lang}-${theme}-${width}x${height}.png`)
      : join(outDir, `detail-full-${kind}-${lang}-${theme}-360x800.png`);
    await session.screenshot(fullPath, "document");
    report.screenshots.push(fullPath);
    report.fullBodyScreenshots.push(fullPath);
  }
  if (kind === "inventory" && lang === "en" && theme === "dark" && (width === 360 || width === 768)) {
    await testInventoryChipLayout(session, inventory.body, catalog, width, outDir, report, record, width === 768);
  }
  report.visited.push({hostID,kind,lang,theme,width,height,apiStatus:summary.status,apiMetricsPoints:metrics.pointCount,inventoryStatus:inventory.status,servicesRendered:readyState.servicesExists});
  return {summary,metrics,inventory,dom:readyState};
}

async function testAllRanges(session, hostID, catalog, record, report) {
  const results = [];
  for (const range of RANGE_LIST) {
    const api = await getSeriesStats(session, hostID, range);
    const apiPass = seriesShapePass(api, hostID, range, true);
    record(`${range}: range API returns aligned samples at expected resolution`, apiPass, api);
    if (!apiPass) throw new Error(`API series fixture missing expected ${range} rollup/sample data`);
    if (range !== "1h") {
      await session.evaluate("performance.clearResourceTimings(); true");
      const changed = await session.evaluate(`(() => {
        const select = document.querySelector("#cp-host-range");
        if (!select) return false;
        select.value = ${JSON.stringify(range)};
        select.dispatchEvent(new Event("change", {bubbles:true}));
        return true;
      })()`);
      if (!changed) throw new Error("time-range control missing during range matrix");
      await waitFor(session, `performance.getEntriesByType("resource").some((entry) => {
        try { const url = new URL(entry.name, location.href); return url.pathname.endsWith("/metrics") && url.searchParams.get("range") === ${JSON.stringify(range)}; }
        catch { return false; }
      })`, `detail UI request for ${range}`);
    }
    const expectedSummaries = chartSpecs(catalog).map((spec) => spec.summary(api));
    const cpuTitle = catalog.host.chart_cpu_title;
    await waitFor(session, `(() => {
      const select = document.querySelector("#cp-host-range");
      const cards = Array.from(document.querySelectorAll(".cp-chart-card"));
      const cpu = cards.find((card) => card.querySelector("h2")?.textContent.trim() === ${JSON.stringify(cpuTitle)});
      return select?.value === ${JSON.stringify(range)} && cpu?.querySelector(".cp-chart-wrap")?.getAttribute("aria-label") === ${JSON.stringify(expectedSummaries[0])};
    })()`, `detail charts update to ${range}`);
    const dom = await extractDetail(session);
    const titles = chartSpecs(catalog).map((spec) => spec.title);
    const summariesMatch = dom.chartCards.length === 6 && dom.chartCards.every((chart, index) => chart.title === titles[index] &&
      chart.ariaLabel === expectedSummaries[index] && chart.summary === expectedSummaries[index] && chart.canvasCount > 0);
    record(`${range}: selected range rebuilds six real-value charts without duplicates`, dom.rangeValue === range && summariesMatch &&
      dom.chartWrapCount === 6 && dom.canvasCount >= 6, {range:dom.rangeValue,expectedSummaries,actual:dom.chartCards.map((chart) => ({aria:chart.ariaLabel,summary:chart.summary,canvases:chart.canvasCount})),chartWrapCount:dom.chartWrapCount,canvasCount:dom.canvasCount});
    results.push({range,pointCount:api.pointCount,resolution:api.resolution,last:api.last});
  }
  report.ranges = results;
}

async function testKeyboardRange(session, hostID, catalog, record) {
  const expectedSeries = await getSeriesStats(session, hostID, "6h");
  if (!seriesShapePass(expectedSeries, hostID, "6h", true)) throw new Error("keyboard range fixture has no 6h API samples");
  const keyboardStart = await session.evaluate(`(() => {
    const back = document.querySelector('a.cp-back-link[href="#/"]');
    if (!back) return null;
    back.focus();
    return {active:document.activeElement === back, outline:getComputedStyle(back).outlineStyle, outlineWidth:getComputedStyle(back).outlineWidth};
  })()`);
  await session.pressTab();
  const rangeFocus = await session.evaluate(`(() => {
    const select = document.querySelector("#cp-host-range");
    const active = document.activeElement;
    const label = document.querySelector('label[for="cp-host-range"]')?.textContent.trim() || select?.labels?.[0]?.textContent.trim() || "";
    return {active:active === select, id:active?.id || "", label:select?.getAttribute("aria-label") || label, value:select?.value || "", outline:getComputedStyle(active).outlineStyle, outlineWidth:getComputedStyle(active).outlineWidth};
  })()`);
  await session.evaluate("performance.clearResourceTimings(); true");
  await session.pressKey("\uE015");
  await session.pressKey("\uE007");
  const expectedCPU = chartSpecs(catalog).find((spec) => spec.title === catalog.host.chart_cpu_title).summary(expectedSeries);
  await waitFor(session, `document.querySelector("#cp-host-range")?.value === "6h" && performance.getEntriesByType("resource").some((entry) => {
    try { const url = new URL(entry.name, location.href); return url.pathname.endsWith("/metrics") && url.searchParams.get("range") === "6h"; }
    catch { return false; }
  })`, "keyboard-selecting the 6h range");
  await waitFor(session, `document.querySelector(".cp-chart-card .cp-chart-wrap")?.getAttribute("aria-label") === ${JSON.stringify(expectedCPU)}`, "keyboard-selected range chart refresh");
  const after = await extractDetail(session);
  record("Tab reaches the range select and ArrowDown+Enter loads real 6h data", keyboardStart?.active && rangeFocus.active &&
    rangeFocus.label === catalog.host.time_range && rangeFocus.value === "1h" && after.rangeValue === "6h" &&
    rangeFocus.outline !== "none" && rangeFocus.outlineWidth !== "0px", {keyboardStart,rangeFocus,afterRange:after.rangeValue,expectedCPU});
}

async function testChartThemeLifecycle(session, catalog, record) {
  const clicked = await session.evaluate(`(() => {
    window.__detailOldChartWraps = Array.from(document.querySelectorAll(".cp-chart-wrap"));
    const button = document.querySelector(".cp-navbar-theme-btn");
    if (!button) return false;
    button.click();
    return true;
  })()`);
  if (!clicked) throw new Error("navbar theme action missing for chart lifecycle check");
  await waitFor(session, `localStorage.getItem("cp_theme") === "system" && document.querySelectorAll(".cp-chart-wrap").length === 6`, "charts recreated on theme change");
  const state = await session.evaluate(`(() => ({
    theme:localStorage.getItem("cp_theme"),
    oldDetached:Array.from(window.__detailOldChartWraps || []).every((item) => !item.isConnected),
    wraps:document.querySelectorAll(".cp-chart-wrap").length,
    canvases:document.querySelectorAll(".cp-chart-wrap canvas").length,
    titles:Array.from(document.querySelectorAll(".cp-chart-card h2")).map((item) => item.textContent.trim()),
  }))()`);
  const expectedTitles = chartSpecs(catalog).map((spec) => spec.title);
  record("cp-theme-change destroys old detail charts and recreates the six chart canvases", state.theme === "system" && state.oldDetached &&
    state.wraps === 6 && state.canvases >= 6 && JSON.stringify(state.titles) === JSON.stringify(expectedTitles), state);
}

async function testKeyboardBacklink(session, hub, catalog, record) {
  const focused = await session.evaluate(`(() => {
    const link = document.querySelector('a.cp-back-link[href="#/"]');
    if (!link) return null;
    link.focus();
    const style = getComputedStyle(link);
    return {href:link.getAttribute("href"),text:link.textContent.trim(),outline:style.outlineStyle,outlineWidth:style.outlineWidth};
  })()`);
  if (!focused?.href) throw new Error("detail backlink missing for keyboard activation");
  await session.pressKey("\uE007");
  await waitFor(session, `location.hash === "#/" && Boolean(document.querySelector(".cp-systems-card"))`, "keyboard backlink to overview");
  const result = await session.evaluate(`({hash:location.hash,title:document.querySelector("main h1")?.textContent.trim() || ""})`);
  record("Enter activates the detail backlink and restores overview", focused.href === "#/" && focused.text === catalog.host.back_to_overview &&
    result.hash === "#/" && result.title === catalog.overview.page_title, {focused,result});
  return {focused,result};
}

function usage() {
  process.stdout.write(
    "usage: node scripts/verify-detail-contract.mjs --stage detail --hub http://127.0.0.1:18190 --password-file FILE --out DIR\n" +
      "Checks the existing detail summary/series/inventory/range/layout/backlink contract only.\n",
  );
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (!options) { usage(); return 0; }
  const passwordPath = localPath(options.passwordFile, "password file");
  const outDir = localPath(options.out, "output directory");
  if (!existsSync(passwordPath)) throw new Error(`password file missing: ${passwordPath}`);
  if ((statSync(passwordPath).mode & 0o077) !== 0) throw new Error("password file must be private (mode 0600 or stricter)");
  const password = readFileSync(passwordPath, "utf8").replace(/\r?\n$/, "");
  if (!password) throw new Error("password file is empty");
  mkdirSync(outDir, {recursive:true,mode:0o700});
  const realOutDir = realpathSync(outDir);
  if (realOutDir !== VERIFY_ROOT && !realOutDir.startsWith(`${VERIFY_ROOT}/`)) throw new Error("resolved detail output escaped .verification");

  const report = {
    generatedAt:new Date().toISOString(),stage:options.stage,hub:options.hub,
    matrix:[],visited:[],checks:[],screenshots:[],fullBodyScreenshots:[],consoleErrors:[],javascriptErrors:[],profileRemoved:false,
    scopeNote:"Current host.js has range/layout controls but no tablist/tab roles; no new tab behavior is assumed.",
  };
  const record = (name, pass, detail = undefined) => {
    report.checks.push({name,pass:Boolean(pass),...(detail === undefined ? {} : {detail})});
    return Boolean(pass);
  };
  let session = null;
  let failure = null;
  try {
    session = new BidiSession();
    registerActiveSession(session);
    await session.start();
    await login(session, options.hub, password);
    report.loginSucceeded = true;

    for (const lang of ["en","ko"]) {
      const catalog = loadCatalog(lang);
      for (const theme of ["dark","light"]) {
        for (const [width,height] of [[360,800],[768,1024],[1440,900]]) {
          const primary = await checkOneHost(session, PRIMARY_HOST, "inventory", options.hub, lang, theme, width, height, catalog, realOutDir, report, record);
          report.matrix.push({lang,theme,width,height,hostID:PRIMARY_HOST,apiMetricsPoints:primary.metrics.pointCount,inventoryStatus:primary.inventory.status});
          if (width === 360) {
            if (lang === "en" && theme === "dark") await testKeyboardRange(session, PRIMARY_HOST, catalog, record);
            for (const target of SPECIAL_HOSTS) {
              const detail = await checkOneHost(session, target.id, target.kind, options.hub, lang, theme, width, height, catalog, realOutDir, report, record);
              report.targetStates ||= [];
              report.targetStates.push({hostID:target.id,kind:target.kind,lang,theme,apiMetricsPoints:detail.metrics.pointCount,inventoryStatus:detail.inventory.status});
            }
          }
          if (lang === "en" && theme === "dark" && width === 1440) {
            await testAllRanges(session, PRIMARY_HOST, catalog, record, report);
            await testChartThemeLifecycle(session, catalog, record);
          }
          if (lang === "en" && theme === "dark" && width === 360) {
            report.keyboardBacklink = await testKeyboardBacklink(session, options.hub, catalog, record);
          }
        }
      }
    }
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
      {consoleErrors:report.consoleErrors,javascriptErrors:report.javascriptErrors,networkFailures:report.networkFailures});
    report.passed = !failure && report.checks.every((check) => check.pass) && report.profileRemoved;
    writeFileSync(safeVerificationPath(join(realOutDir,"detail-contract-report.json")),`${JSON.stringify(report,null,2)}\n`,{mode:0o600});
  }
  process.stdout.write(`detail contract: ${report.passed ? "PASS" : "FAIL"} (${report.checks.filter((check) => check.pass).length}/${report.checks.length} checks)\n`);
  process.stdout.write(`report: ${join(realOutDir,"detail-contract-report.json")}\n`);
  if (failure) process.stderr.write(`${failure.stack || failure}\n`);
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
    process.stderr.write(`verify-detail-contract.mjs: ${error?.stack || error}\n`);
    process.exit(1);
  });
}
