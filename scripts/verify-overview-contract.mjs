#!/usr/bin/env node
// Card-grid stage checks for the existing overview data/interaction contract.
// Uses the shared Firefox BiDi session from verify-phase1-auth.mjs and only
// writes screenshots/report/profile data under .verification/.
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
import { BidiSession, registerActiveSession } from "./verify-phase1-auth.mjs";
import { safeVerificationPath } from "./verify-round28-safety.mjs";

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = realpathSync(resolve(SCRIPT_DIR, ".."));
const VERIFY_ROOT = resolve(REPO_ROOT, ".verification");
const sleep = (ms) => new Promise((resolveSleep) => setTimeout(resolveSleep, ms));
const STAGES = new Set(["phase1", "card-grid", "detail", "activity"]);

function parseArgs(argv) {
  const options = { stage: "card-grid", hub: null, passwordFile: null, out: null };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--help" || arg === "-h") return null;
    if (!["--stage", "--hub", "--password-file", "--out"].includes(arg)) throw new Error(`unknown argument: ${arg}`);
    const value = argv[++i];
    if (!value || value.startsWith("--")) throw new Error(`${arg} requires a value`);
    const key = { "--stage": "stage", "--hub": "hub", "--password-file": "passwordFile", "--out": "out" }[arg];
    options[key] = value;
  }
  if (!STAGES.has(options.stage)) throw new Error(`invalid stage: ${options.stage}`);
  if (options.stage !== "card-grid") throw new Error("overview contract checks currently cover only the card-grid stage; no detail/activity UI assertions are implied");
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
  try {
  await waitFor(session, `Boolean(document.querySelector(".cp-systems-card h2"))`, "overview after login");
  } catch (error) {
    const diagnostic = await session.evaluate(`({
      href:location.href,
      hash:location.hash,
      hasSession:Boolean(localStorage.getItem("cp_session")),
      username:document.querySelector('[aria-label="User menu"]') ? "authenticated-shell" : "no-user-menu",
      hasSystemsHeading:Boolean(document.querySelector(".cp-systems-card h2")),
      h1:document.querySelector("main h1")?.textContent.trim() || "",
      bodyText:(document.body.innerText || "").slice(0,600),
    })`).catch((evalError) => ({evaluationError:String(evalError)}));
    throw new Error(`${error.message}; post-login DOM: ${JSON.stringify(diagnostic)}`);
  }
  const route = await session.evaluate(`(() => {
    const section = document.querySelector(".cp-systems-card");
    const labelledBy = section?.getAttribute("aria-labelledby") || "";
    return {
      hash:location.hash,
      hasSession:Boolean(localStorage.getItem("cp_session")),
      heading:section?.querySelector("h2")?.textContent.trim() || "",
      sectionLabelledBy:labelledBy,
      ariaTargetExists:Boolean(labelledBy && document.getElementById(labelledBy)),
    };
  })()`);
  if (!route.hasSession || !route.heading || route.hash.includes("/login")) throw new Error(`login did not land on the overview: ${JSON.stringify(route)}`);
  return route;
}

async function loadOverviewVariant(session, hub, { lang, theme, width, height }) {
  await session.setViewport(width, height);
  await session.evaluate(`(() => {
    localStorage.setItem("cp_lang", ${JSON.stringify(lang)});
    localStorage.setItem("cp_theme", ${JSON.stringify(theme)});
    localStorage.setItem("cp_overview_view", "grid");
    localStorage.setItem("cp_overview_sort", JSON.stringify({key:"system",direction:"asc"}));
    window.scrollTo(0, 0);
    return true;
  })()`);
  await session.navigate(`${hub}/?cp_verify=${lang}-${theme}-${width}#/`);
  await waitFor(session, `document.documentElement.lang === ${JSON.stringify(lang)} && document.documentElement.classList.contains("dark") === ${theme === "dark"} && Boolean(document.querySelector(".cp-systems-card h2") && document.querySelector(".cp-host-grid"))`, "grid-view overview");
}

async function getAPIHosts(session) {
  const result = await session.evaluate(`(async () => {
    const token = localStorage.getItem("cp_session") || "";
    const response = await fetch("/api/v1/hosts", {headers:{Authorization:"Bearer " + token}});
    const body = await response.json();
    return {status: response.status, hosts: body.hosts || [], error: body.error || ""};
  })()`);
  if (result.status !== 200 || !Array.isArray(result.hosts)) throw new Error(`GET /api/v1/hosts returned ${result.status}: ${result.error}`);
  return result.hosts;
}

const EXTRACT_GRID = `(() => {
  const section = document.querySelector(".cp-systems-card");
  const heading = section?.querySelector("h2");
  if (!section) return {missingSection:true, cards:[]};
  const visible = (node) => {
    const style = getComputedStyle(node);
    const rect = node.getBoundingClientRect();
    return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0;
  };
  const cards = Array.from(section.querySelectorAll('a[href^="#/host/"]')).filter(visible).map((node) => {
    const badge = node.querySelector(".cp-badge-aws,.cp-badge-oci,.cp-badge-other");
    const sortSelect = section.querySelector(".cp-host-grid-sort select");
    const sortButton = section.querySelector(".cp-host-grid-sort button");
    return {
      href: node.getAttribute("href") || "",
      name: node.querySelector(".cp-host-grid-name,.cp-host-card-name,.cp-systems-system-name")?.textContent.trim() || "",
      text: node.textContent || "",
      classes: Array.from(node.classList),
      statusLabel: node.querySelector(".cp-host-card-status")?.textContent.trim() || "",
      providerClass: Array.from(badge?.classList || []).find((c) => /^cp-badge-(aws|oci|other)$/.test(c)) || "",
      providerLabel: badge?.textContent.trim() || "",
      metadata: Array.from(node.querySelectorAll(".cp-host-card-meta")).map((x) => x.textContent.trim()),
      snapshot: node.querySelector(".cp-host-snapshot")?.textContent.trim() || "",
      resourceLabels: Array.from(node.querySelectorAll(".cp-host-resource-label")).map((x) => x.textContent.trim()),
      resourceValues: Array.from(node.querySelectorAll(".cp-host-resource-value")).map((x) => x.textContent.trim()),
    };
  });
  const input = section.querySelector('input[type="search"]');
  const view = section.querySelector('[role="group"].cp-view-toggle');
  const subtitle = heading.parentElement?.querySelector("p")?.textContent.trim() || "";
  const statCards = Array.from(document.querySelectorAll(".cp-stat-card")).map((card) => ({
    label: card.querySelector(".cp-stat-card-label")?.textContent.trim() || "",
    value: card.querySelector(".cp-stat-card-value")?.textContent.trim() || "",
  }));
  const docWidth = Math.max(document.documentElement.scrollWidth, document.body.scrollWidth);
  return {
    missingSection:false,
    cards,
    lang: document.documentElement.lang,
    width: innerWidth,
    docWidth,
    subtitle,
    statCards,
    filterLabel: input?.getAttribute("aria-label") || "",
    gridPressed: view?.querySelector('button[aria-pressed="true"]')?.getAttribute("aria-label") || "",
    gridSortLabel: section.querySelector(".cp-host-grid-sort select")?.getAttribute("aria-label") || "",
    gridSortValue: section.querySelector(".cp-host-grid-sort select")?.value || "",
    gridSortDirectionLabel: section.querySelector(".cp-host-grid-sort button")?.getAttribute("aria-label") || "",
    title: heading.textContent.trim(),
  };
})()`;

async function extractGrid(session) {
  return session.evaluate(EXTRACT_GRID);
}

async function inspectUnknownTrackStyles(session, apiHost) {
  return session.evaluate(`(async () => {
    const {hostGridCard} = await import("/assets/js/pages/hostgrid.js");
    const row = structuredClone(${JSON.stringify(apiHost)});
    row.latest = null;
    row.egress = {
      ...row.egress,
      tx_bytes: null,
      rx_bytes: null,
      limit_bytes: 1024 * 1024,
      rx_limit_bytes: 2 * 1024 * 1024,
      level: "warning",
      rx_level: "critical",
    };
    const mount = document.createElement("div");
    mount.className = "cp-host-grid";
    Object.assign(mount.style, {position:"fixed",left:"-10000px",top:"0",width:String(innerWidth) + "px"});
    const card = hostGridCard(row, Date.now());
    mount.append(card);
    document.body.append(mount);
    try {
      await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      const selectors = [
        ["cpu", '.cp-host-resource[data-resource="cpu"] > .cp-bar-track'],
        ["memory", '.cp-host-resource[data-resource="memory"] > .cp-bar-track'],
        ["disk", '.cp-host-resource[data-resource="disk"] > .cp-bar-track'],
        ["outbound", '.cp-host-traffic-row[data-traffic="out"] > .cp-bar-track'],
        ["inbound", '.cp-host-traffic-row[data-traffic="in"] > .cp-bar-track'],
      ];
      return selectors.map(([name, selector]) => {
        const node = card.querySelector(selector);
        if (!node) return {name,exists:false};
        const style = getComputedStyle(node);
        const rect = node.getBoundingClientRect();
        return {name,exists:true,height:style.height,flexGrow:style.flexGrow,width:rect.width,selector};
      });
    } finally {
      mount.remove();
    }
  })()`);
}

function apiFilter(hosts, query) {
  const q = String(query).trim().toLowerCase();
  return hosts.filter((row) => [row.host?.hostname, row.host?.id, row.host?.provider, row.status, row.host?.os, row.host?.arch]
    .filter(Boolean).join(" ").toLowerCase().includes(q));
}

function providerCounts(rows) {
  const counts = {};
  for (const row of rows) counts[row.host.provider] = (counts[row.host.provider] || 0) + 1;
  return counts;
}

async function setFilterAndRead(session, value) {
  await session.evaluate(`(() => {
    const section = document.querySelector(".cp-systems-card");
    const input = section?.querySelector('input[type="search"]');
    if (!input) return false;
    input.value = ${JSON.stringify(value)};
    input.dispatchEvent(new Event("input", {bubbles:true}));
    return true;
  })()`);
  await sleep(120);
  return extractGrid(session);
}

async function openView(session, label) {
  const opened = await session.evaluate(`(() => {
    const section = document.querySelector(".cp-systems-card");
    const button = Array.from(section?.querySelectorAll(".cp-view-toggle button") || []).find((item) => item.getAttribute("aria-label") === ${JSON.stringify(label)});
    if (!button) return false;
    button.click();
    return true;
  })()`);
  if (!opened) throw new Error(`view control not found: ${label}`);
}

async function testTableSort(session, rows, catalog) {
  await openView(session, catalog.overview.table_view);
  await waitFor(session, `Boolean(document.querySelector("table.cp-systems-table tbody tr"))`, "table-view host rows");
  const action = await session.evaluate(`(() => {
    const section = document.querySelector(".cp-systems-card");
    const systemHead = Array.from(section?.querySelectorAll("th") || []).find((th) => th.querySelector("button")?.textContent.trim() === ${JSON.stringify(catalog.overview.column_system)});
    if (!systemHead) return false;
    const button = systemHead.querySelector("button");
    const before = systemHead.getAttribute("aria-sort");
    button.click();
    return {before, button:true};
  })()`);
  if (!action) throw new Error(`sortable System column not found for ${catalog.overview.column_system}`);
  await sleep(120);
  const tableState = await session.evaluate(`(() => {
    const section = document.querySelector(".cp-systems-card");
    const systemHead = Array.from(section?.querySelectorAll("th") || []).find((th) => th.querySelector("button")?.textContent.trim() === ${JSON.stringify(catalog.overview.column_system)});
    return {
      direction: systemHead?.getAttribute("aria-sort") || "",
      ids: Array.from(section?.querySelectorAll("table.cp-systems-table tbody tr") || []).map((row) => {
        const link = row.querySelector("a.cp-systems-row-link[href^='#/host/']");
        return link ? decodeURIComponent(link.getAttribute("href").slice("#/host/".length)) : "";
      }),
    };
  })()`);
  const expected = rows.slice().sort((a, b) => {
    const x = String(a.host.hostname || "").toLowerCase();
    const y = String(b.host.hostname || "").toLowerCase();
    return x < y ? 1 : x > y ? -1 : 0;
  }).map((row) => row.host.id);
  return { action, tableState, expected };
}

async function testGridSort(session, rows, catalog) {
  await openView(session, catalog.overview.grid_view);
  const selection = await session.evaluate(`(() => {
    const section = document.querySelector(".cp-systems-card");
    const select = section?.querySelector(".cp-host-grid-sort select");
    if (!select) return null;
    const label = select.getAttribute("aria-label") || "";
    select.value = "cpu";
    select.dispatchEvent(new Event("change", {bubbles:true}));
    return {label, value:select.value};
  })()`);
  if (!selection) throw new Error("grid sort select is missing");
  await sleep(120);
  const ascendingState = await extractGrid(session);
  const expectedCPU = rows.slice().sort((a, b) => {
    const x = a.latest?.cpu_percent ?? -1;
    const y = b.latest?.cpu_percent ?? -1;
    return x < y ? -1 : x > y ? 1 : 0;
  }).map((row) => row.host.id);
  const ascendingIDs = ascendingState.cards.map((card) => decodeURIComponent(card.href.replace(/^#\/host\//, "")));
  const flipped = await session.evaluate(`(() => {
    const section = document.querySelector(".cp-systems-card");
    const button = section?.querySelector(".cp-host-grid-sort button");
    if (!button) return false;
    button.click();
    return true;
  })()`);
  if (!flipped) throw new Error("grid sort direction button is missing");
  await sleep(120);
  const descendingState = await extractGrid(session);
  const expectedDescending = rows.slice().sort((a, b) => {
    const x = a.latest?.cpu_percent ?? -1;
    const y = b.latest?.cpu_percent ?? -1;
    return x > y ? -1 : x < y ? 1 : 0;
  }).map((row) => row.host.id);
  const descendingIDs = descendingState.cards.map((card) => decodeURIComponent(card.href.replace(/^#\/host\//, "")));
  return {
    selection,
    ascending: {
      directionLabel: ascendingState.gridSortDirectionLabel,
      selectedValue: ascendingState.gridSortValue,
      ids: ascendingIDs,
      expected: expectedCPU,
    },
    descending: {
      directionLabel: descendingState.gridSortDirectionLabel,
      selectedValue: descendingState.gridSortValue,
      ids: descendingIDs,
      expected: expectedDescending,
    },
    expectedAscendingLabel: catalog.overview.card_sort_ascending,
    expectedDescendingLabel: catalog.overview.card_sort_descending,
    expectedSelectLabel: catalog.overview.card_sort_by,
  };
}

async function captureMobileStateEvidence(session, lang, theme, hosts, outDir, report, requirePass) {
  await session.evaluate("window.scrollTo(0, 0); true");
  const fullPath = join(outDir, `overview-full-${lang}-${theme}-360x800.png`);
  await session.screenshot(fullPath, "document");
  report.screenshots.push(fullPath);
  report.fullBodyScreenshots.push(fullPath);

  const offline = hosts.find((host) => host.status === "down");
  const unknown = hosts.find((host) => host.host.id === "verify-unknown-01");
  const longName = hosts.find((host) => host.host.id === "verify-long-name-01");
  const targets = [
    { label: "offline", row: offline },
    { label: "unknown-no-sample", row: unknown },
    { label: "long-hostname-no-sample", row: longName },
  ];
  for (const target of targets) {
    requirePass(`${lang}-${theme}-360x800: ${target.label} API target is present`, Boolean(target.row),
      { hostId: target.row?.host.id || "missing" });
    const evidence = await session.evaluate(`(() => {
      const section = document.querySelector(".cp-systems-card");
      const wanted = ${JSON.stringify(`#/host/${encodeURIComponent(target.row.host.id)}`)};
      const card = Array.from(section?.querySelectorAll("a.cp-host-grid-card[href^='#/host/']") || []).find((item) => item.getAttribute("href") === wanted);
      if (!card) return null;
      card.scrollIntoView({block:"center", inline:"nearest"});
      const rect = card.getBoundingClientRect();
      return {
        href:card.getAttribute("href"),
        name:card.querySelector(".cp-host-grid-name")?.textContent.trim() || "",
        status:card.querySelector(".cp-host-card-status")?.textContent.trim() || "",
        documentWidth:Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
        viewportWidth:innerWidth,
        visibleHeight:Math.max(0, Math.min(rect.bottom, innerHeight) - Math.max(rect.top, 0)),
        cardHeight:rect.height,
      };
    })()`);
    requirePass(`${lang}-${theme}-360x800: ${target.label} card scrolls into the viewport`, Boolean(evidence && evidence.visibleHeight > 0 && evidence.cardHeight > 0 && evidence.name === target.row.host.hostname), evidence);
    await sleep(150);
    const path = join(outDir, `overview-card-${target.label}-${lang}-${theme}-360x800.png`);
    await session.screenshot(path, "viewport");
    report.screenshots.push(path);
    report.targetStateScreenshots.push({ label: target.label, hostId: target.row.host.id, hostname: target.row.host.hostname, lang, theme, path });
  }
}

async function runFocusNavigation(session, hub, catalog, hosts) {
  await openView(session, catalog.overview.grid_view);
  await waitFor(session, `Boolean(document.querySelector(".cp-host-grid a[href^='#/host/']"))`, "grid cards for keyboard navigation");
  const started = await session.evaluate(`(() => {
    const input = document.querySelector(".cp-systems-card")?.querySelector('input[type="search"]');
    input?.focus();
    return Boolean(input);
  })()`);
  if (!started) throw new Error("overview filter control is missing before keyboard navigation");
  let focused = null;
  for (let i = 0; i < 12; i++) {
    await session.pressTab();
    focused = await session.evaluate(`(() => {
      const node = document.activeElement;
      const style = getComputedStyle(node);
      const href = node?.getAttribute?.("href") || "";
      const section = document.querySelector(".cp-systems-card");
      const isHostLink = Boolean(section && node?.matches?.("a[href^='#/host/']") && section.contains(node));
      return {
        href,
        id: isHostLink ? decodeURIComponent(href.slice("#/host/".length)) : "",
        focused: isHostLink && document.activeElement === node,
        outlineVisible: style.outlineStyle !== "none" && style.outlineWidth !== "0px",
        outlineStyle: style.outlineStyle,
        outlineWidth: style.outlineWidth,
        tag: node?.tagName || "",
      };
    })()`);
    if (focused.focused) break;
  }
  if (!focused?.focused || !focused.outlineVisible) throw new Error(`host card does not expose visible focus: ${JSON.stringify(focused)}`);
  const expected = hosts.find((host) => host.host.id === focused.id);
  if (!expected) throw new Error(`focused card ID is absent from API fixture: ${focused.id}`);
  await session.pressKey("\uE007");
  await waitFor(session, `location.hash === ${JSON.stringify(`#/host/${encodeURIComponent(focused.id)}`)} && document.querySelector("main h1")?.textContent.trim() === ${JSON.stringify(expected.host.hostname)}`, "host detail after Enter activation");
  const target = await session.evaluate(`({hash:location.hash, heading:document.querySelector("main h1")?.textContent.trim() || ""})`);
  await session.navigate(`${hub}/#/`);
  await waitFor(session, `Boolean(document.querySelector(".cp-systems-card h2"))`, "overview after keyboard route check");
  return { focused, target, expectedName: expected.host.hostname };
}

function usage() {
  process.stdout.write(
    "usage: node scripts/verify-overview-contract.mjs --stage card-grid --hub http://127.0.0.1:18190 --password-file FILE --out DIR\n" +
      "Checks existing overview host/filter/sort/view/locale/offline/unknown contracts; no detail or Activity UI assumptions.\n",
  );
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (!options) { usage(); return 0; }
  const passwordPath = localPath(options.passwordFile, "password file");
  const outDir = localPath(options.out, "output directory");
  if (!existsSync(passwordPath)) throw new Error(`password file does not exist: ${passwordPath}`);
  if ((statSync(passwordPath).mode & 0o077) !== 0) throw new Error("password file must be mode 0600 or stricter");
  const password = readFileSync(passwordPath, "utf8").replace(/\r?\n$/, "");
  if (!password) throw new Error("password file is empty");
  mkdirSync(outDir, { recursive: true, mode: 0o700 });
  const realOutDir = realpathSync(outDir);
  if (realOutDir !== VERIFY_ROOT && !realOutDir.startsWith(`${VERIFY_ROOT}/`)) throw new Error("resolved output directory escaped .verification");

  const report = {
    generatedAt: new Date().toISOString(),
    stage: options.stage,
    hub: options.hub,
    matrix: [],
    checks: [],
    screenshots: [],
    fullBodyScreenshots: [],
    targetStateScreenshots: [],
    consoleErrors: [],
    javascriptErrors: [],
    profileRemoved: false,
  };
  const record = (name, pass, detail = undefined) => {
    report.checks.push({ name, pass: Boolean(pass), ...(detail === undefined ? {} : { detail }) });
    return Boolean(pass);
  };
  const requirePass = (name, pass, detail = undefined) => {
    if (!record(name, pass, detail)) throw new Error(`check failed: ${name}`);
  };

  let session = null;
  let failure = null;
  try {
    session = new BidiSession();
    registerActiveSession(session);
    await session.start();
    report.loginRoute = await login(session, options.hub, password);
    report.loginSucceeded = true;
    record("All Systems region references an existing accessible heading", Boolean(report.loginRoute.ariaTargetExists), report.loginRoute);

    for (const lang of ["en", "ko"]) {
      const catalog = loadCatalog(lang);
      for (const theme of ["dark", "light"]) {
        for (const [width, height] of [[360, 800], [768, 1024], [1440, 900]]) {
          const variant = { lang, theme, width, height };
          const key = `${lang}-${theme}-${width}x${height}`;
          await loadOverviewVariant(session, options.hub, variant);
          const apiHosts = await getAPIHosts(session);
          const initial = await extractGrid(session);
          const expectedIDs = apiHosts.map((row) => row.host.id).sort();
          const actualIDs = initial.cards.map((card) => decodeURIComponent(card.href.replace(/^#\/host\//, ""))).sort();
           requirePass(`${key}: locale and grid control labels`, initial.lang === lang && initial.gridPressed === catalog.overview.grid_view &&
             initial.gridSortLabel === catalog.overview.card_sort_by && initial.gridSortValue === "system" &&
             initial.gridSortDirectionLabel === catalog.overview.card_sort_ascending,
            { actualLang: initial.lang, gridPressed: initial.gridPressed, expectedGridLabel: catalog.overview.grid_view,
              gridSortLabel: initial.gridSortLabel, gridSortValue: initial.gridSortValue, gridSortDirectionLabel: initial.gridSortDirectionLabel });
          requirePass(`${key}: filter label and system heading localized`, initial.filterLabel === catalog.overview.filter_hosts_aria_label && initial.title === catalog.overview.all_systems_title,
            { filterLabel: initial.filterLabel, heading: initial.title });
          const hostCountTemplate = catalog.overview.host_count[apiHosts.length === 1 ? "one" : "other"];
          const expectedHostCountText = hostCountTemplate.replace("{count}", String(apiHosts.length));
          requirePass(`${key}: subtitle host count matches API fixture`, initial.subtitle.includes(expectedHostCountText),
            { expectedHostCountText, subtitle: initial.subtitle, apiHostCount: apiHosts.length });
          const upCount = apiHosts.filter((host) => host.status === "up").length;
          const downCount = apiHosts.length - upCount;
          const statValue = (label) => Number(String(initial.statCards.find((card) => card.label === label)?.value || "").replace(/[^\d-]/g, ""));
          requirePass(`${key}: up/down summary counts match API fixture`,
            initial.statCards.some((card) => card.label === catalog.overview.summary_hosts_up) &&
            initial.statCards.some((card) => card.label === catalog.overview.summary_hosts_down) &&
            statValue(catalog.overview.summary_hosts_up) === upCount && statValue(catalog.overview.summary_hosts_down) === downCount,
            { expectedUp: upCount, actualUp: statValue(catalog.overview.summary_hosts_up), expectedDown: downCount, actualDown: statValue(catalog.overview.summary_hosts_down), labels: initial.statCards.map((card) => card.label) });
           requirePass(`${key}: API host count/names/links match visible cards`, !initial.missingSection && initial.cards.length === apiHosts.length &&
            JSON.stringify(actualIDs) === JSON.stringify(expectedIDs) && initial.cards.every((card) => {
              const id = decodeURIComponent(card.href.replace(/^#\/host\//, ""));
              const row = apiHosts.find((host) => host.host.id === id);
              const expectedProviderClass = `cp-badge-${row?.host.provider || ""}`;
              return row && card.href === `#/host/${encodeURIComponent(id)}` && card.name === row.host.hostname && card.providerClass === expectedProviderClass &&
                card.providerLabel === (row.host.provider === "aws" ? "AWS" : row.host.provider === "oci" ? "OCI" : catalog.host.provider_other) &&
             card.statusLabel === (row.status === "up" ? catalog.overview.card_online : catalog.overview.card_offline);
            }), { apiCount: apiHosts.length, cardCount: initial.cards.length, apiIDs: expectedIDs, cardIDs: actualIDs });
          const longNameHost = apiHosts.find((row) => row.host.id === "verify-long-name-01");
          const longNameCard = initial.cards.find((card) => card.href === "#/host/verify-long-name-01");
          requirePass(`${key}: long hostname is rendered intact from API data`, Boolean(longNameHost && longNameCard &&
            longNameHost.host.hostname.length >= 50 && longNameCard.name === longNameHost.host.hostname),
            { apiHostname: longNameHost?.host.hostname, length: longNameHost?.host.hostname.length, renderedName: longNameCard?.name });
          requirePass(`${key}: provider counts match API host distribution`, (() => {
            const expected = providerCounts(apiHosts);
            const actual = {};
            for (const card of initial.cards) {
              const provider = card.providerClass.replace(/^cp-badge-/, "");
              actual[provider] = (actual[provider] || 0) + 1;
            }
            return JSON.stringify(Object.fromEntries(Object.entries(expected).sort())) === JSON.stringify(Object.fromEntries(Object.entries(actual).sort()));
          })(), { expected: providerCounts(apiHosts), actual: initial.cards.reduce((counts, card) => {
            const provider = card.providerClass.replace(/^cp-badge-/, ""); counts[provider] = (counts[provider] || 0) + 1; return counts;
          }, {}) });
          requirePass(`${key}: no horizontal overflow`, initial.docWidth <= initial.width, { viewportWidth: initial.width, documentWidth: initial.docWidth });

          const unknownTrackFixture = apiHosts.find((row) => row.host.id === "verify-unknown-01");
          const unknownTrackStyles = unknownTrackFixture ? await inspectUnknownTrackStyles(session, unknownTrackFixture) : [];
          requirePass(`${key}: five unknown CPU/memory/disk/traffic tracks compute to 6px with width and no flex growth`,
            unknownTrackStyles.length === 5 && unknownTrackStyles.every((track) => track.exists && track.height === "6px" && track.flexGrow === "0" && track.width > 0),
            {fixture:"in-memory-only renderer clone with latest=null, null traffic byte fields, and positive limits; no API/database write",tracks:unknownTrackStyles});

          const unknown = apiHosts.find((row) => row.host.id === "verify-unknown-01");
          const unknownCard = initial.cards.find((card) => card.href === "#/host/verify-unknown-01");
           requirePass(`${key}: blank metadata/no-sample fixture renders localized unknown state`, Boolean(unknown && !unknown.latest && !unknown.host.os && !unknown.host.arch && !unknown.host.agent_version && unknownCard &&
             unknownCard.metadata.some((text) => (text.match(new RegExp(catalog.overview.unknown.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "g")) || []).length >= 2) &&
             unknownCard.snapshot.includes(catalog.host.no_samples_yet) && unknownCard.resourceValues.length === 3 && unknownCard.resourceValues.every((value) => value === "–")),
             { apiFixturePresent: Boolean(unknown), host: unknown?.host, card: unknownCard });
           const offline = apiHosts.find((row) => row.status === "down");
           const offlineCard = offline && initial.cards.find((card) => card.href === `#/host/${encodeURIComponent(offline.host.id)}`);
           requirePass(`${key}: offline API host is visibly marked and labeled`, Boolean(offline && offlineCard && offlineCard.classes.includes("cp-host-card-offline") &&
             offlineCard.statusLabel === catalog.overview.card_offline && offlineCard.snapshot.includes(catalog.overview.card_last_reported_metrics)),
             { offlineHost: offline?.host.id, card: offlineCard });
          requirePass(`${key}: visible labels/values contain no nullish placeholders`, initial.cards.every((card) => !/\b(undefined|NaN|null)\b/i.test(card.text)),
            initial.cards.filter((card) => /\b(undefined|NaN|null)\b/i.test(card.text)).map((card) => card.href));

          const metricsHost = apiHosts.find((row) => row.latest);
          const metricsCard = metricsHost && initial.cards.find((card) => card.href === `#/host/${encodeURIComponent(metricsHost.host.id)}`);
          const expectedMetricLabels = [catalog.overview.metric_cpu, catalog.overview.metric_memory, catalog.overview.metric_disk];
           requirePass(`${key}: metric labels/CPU value match localized API-backed card fields`, Boolean(metricsCard && expectedMetricLabels.every((label) => metricsCard.resourceLabels.includes(label)) &&
             metricsCard.resourceValues[0] === `${Math.max(0, Math.min(100, Number(metricsHost.latest.cpu_percent))).toFixed(1)}%`),
             { host: metricsHost?.host.id, expectedLabels: expectedMetricLabels, actualLabels: metricsCard?.resourceLabels, cpu: metricsHost?.latest?.cpu_percent, renderedValues: metricsCard?.resourceValues });

          const screenshotPath = join(realOutDir, `overview-${key}.png`);
          await session.screenshot(screenshotPath);
          report.screenshots.push(screenshotPath);
          report.matrix.push({ ...variant, apiHostCount: apiHosts.length, cardCount: initial.cards.length });
          if (width === 360) await captureMobileStateEvidence(session, lang, theme, apiHosts, realOutDir, report, requirePass);

          // Exercise the real live filter in both localizations using
          // the same API fields covered by hosttable.js's existing contract.
          for (const query of ["DEMO-AWS-WEB-01", "oci"]) {
            const filtered = await setFilterAndRead(session, query);
            const expectedFiltered = apiFilter(apiHosts, query).map((row) => row.host.id).sort();
            const actualFiltered = filtered.cards.map((card) => decodeURIComponent(card.href.replace(/^#\/host\//, ""))).sort();
            requirePass(`${key}: filter ${query} matches host API fixture`, JSON.stringify(actualFiltered) === JSON.stringify(expectedFiltered),
              { query, expected: expectedFiltered, actual: actualFiltered });
          }
          const cleared = await setFilterAndRead(session, "");
          requirePass(`${key}: clearing filter restores all fixture hosts`, cleared.cards.length === apiHosts.length,
            { expected: apiHosts.length, actual: cleared.cards.length });

           const sort = await testTableSort(session, apiHosts, catalog);
           requirePass(`${key}: System sort button toggles to descending API order`, sort.tableState.direction === "descending" &&
             JSON.stringify(sort.tableState.ids) === JSON.stringify(sort.expected), sort);
           await openView(session, catalog.overview.grid_view);
           await waitFor(session, `Boolean(document.querySelector(".cp-host-grid a[href^='#/host/']"))`, "grid view restored");
           const gridSort = await testGridSort(session, apiHosts, catalog);
           requirePass(`${key}: grid sort selector/direction reverses API order`, gridSort.selection.label === gridSort.expectedSelectLabel &&
             gridSort.selection.value === "cpu" && gridSort.ascending.selectedValue === "cpu" &&
             gridSort.ascending.directionLabel === gridSort.expectedAscendingLabel &&
             JSON.stringify(gridSort.ascending.ids) === JSON.stringify(gridSort.ascending.expected) &&
             gridSort.descending.selectedValue === "cpu" && gridSort.descending.directionLabel === gridSort.expectedDescendingLabel &&
             JSON.stringify(gridSort.descending.ids) === JSON.stringify(gridSort.descending.expected), gridSort);

          if (lang === "en" && theme === "dark" && width === 360) {
            const keyboard = await runFocusNavigation(session, options.hub, catalog, apiHosts);
            requirePass(`${key}: focused card activates the matching detail route with Enter`, keyboard.target.hash === `#/host/${encodeURIComponent(keyboard.focused.id)}` &&
              keyboard.target.heading.includes(keyboard.expectedName), keyboard);
            report.keyboardNavigation = keyboard;
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
      { consoleErrors: report.consoleErrors, javascriptErrors: report.javascriptErrors, networkFailures: report.networkFailures });
    report.passed = !failure && report.checks.every((item) => item.pass) && report.profileRemoved;
    writeFileSync(safeVerificationPath(join(realOutDir, "overview-contract-report.json")), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 });
  }

  process.stdout.write(`overview contract: ${report.passed ? "PASS" : "FAIL"} (${report.checks.filter((item) => item.pass).length}/${report.checks.length} checks)\n`);
  process.stdout.write(`report: ${join(realOutDir, "overview-contract-report.json")}\n`);
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
    process.stderr.write(`verify-overview-contract.mjs: ${error?.stack || error}\n`);
    process.exit(1);
  });
}
