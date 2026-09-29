// main.js — bootstrap and hash router for the cloud-pulse dashboard.
// Routes: "#/" overview, "#/host/<id>" host detail. Handles the 15s
// auto-refresh loop (paused while the tab is hidden), request
// cancellation via AbortController on navigation, and the 401 token
// dialog wiring.
import { apiFetch, registerTokenDialog, getHosts, getHost, getHostMetrics, getEgress, getBuckets, getVersion, ApiError } from "./api.js";
import {
  el,
  clearChildren,
  hostCard,
  emptyState,
  errorBanner,
  summaryStrip,
  progressBar,
  levelClassForBar,
  egressDirectionRow,
  updateBanner,
  agentUpdatePanel,
} from "./components.js";
import { formatBytes, formatBitrate, formatDuration, formatRelativeTimeFromUnixSeconds, formatLoad } from "./format.js";
import { buildEgressSection, currentMonth, getStoredDirection, setStoredDirection } from "./egress.js";
import { buildBucketsSection } from "./buckets.js";
import { createTimeSeriesChart, SERIES_COLORS } from "./charts.js";
import { runHostDetailRefresh } from "./refresh.js";
import { buildSettingsPage, copyToClipboard } from "./settings.js";
import {
  shouldShowUpdateBanner,
  getDismissedUpdateTag,
  setDismissedUpdateTag,
  outdatedAgentCount,
  VERSION_REFRESH_INTERVAL_MS,
} from "./updates.js";

const REFRESH_INTERVAL_MS = 15000;
const AGENT_INSTALL_HINT =
  "curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh | sudo bash -s -- --hub-url <HUB_URL> --token <AGENT_TOKEN>";

const RANGE_OPTIONS = ["1h", "6h", "24h", "7d", "30d"];

/** @type {HTMLElement} */
let mainEl;
/** @type {HTMLElement} */
let liveRegionEl;
/** @type {HTMLElement} */
let refreshIndicatorEl;
/** @type {HTMLElement} */
let refreshBannerHost;
/** @type {HTMLElement} */
let updateBannerHost;

/** updateBannerTimer holds the pending setInterval id for the 30-min
 * GET /api/v1/version poll driving the global update banner. Runs for
 * the lifetime of the app (not torn down on route changes), matching
 * D-U6's "all pages" banner scope. */
let updateBannerTimer = null;
/** latestVersionInfo caches the last successful GET /api/v1/version
 * response so a page navigation can re-render the banner without an
 * extra fetch. */
let latestVersionInfo = null;

/** Router state: tracks the active page's teardown so navigation cleans up timers/charts/fetches. */
let activePage = null;

/** refreshTimer holds the pending setTimeout id for the auto-refresh chain. */
let refreshTimer = null;

function scheduleRefresh(fn) {
  clearRefreshTimer();
  if (document.hidden) return;
  refreshTimer = setTimeout(() => {
    fn();
  }, REFRESH_INTERVAL_MS);
}

function clearRefreshTimer() {
  if (refreshTimer !== null) {
    clearTimeout(refreshTimer);
    refreshTimer = null;
  }
}

function announce(text) {
  liveRegionEl.textContent = text;
}

function setRefreshIndicator(text) {
  refreshIndicatorEl.textContent = text;
}

function showBanner(message) {
  clearChildren(refreshBannerHost);
  refreshBannerHost.append(errorBanner(message));
}

function clearBanner() {
  clearChildren(refreshBannerHost);
}

/**
 * teardownActivePage stops the active page's refresh loop, aborts
 * in-flight fetches, and destroys any charts before switching routes.
 */
function teardownActivePage() {
  clearRefreshTimer();
  if (activePage) {
    activePage.controller?.abort();
    activePage.charts?.forEach((c) => c.destroy());
    activePage = null;
  }
}

// ---------------------------------------------------------------------------
// Overview page
// ---------------------------------------------------------------------------

async function renderOverview() {
  teardownActivePage();
  const controller = new AbortController();
  const page = { controller, charts: [], month: currentMonth(), direction: getStoredDirection() };
  activePage = page;

  clearChildren(mainEl);
  const heading = el("h1", { class: "cp-page-title", text: "Fleet overview" });
  mainEl.append(heading);

  const summaryHost = el("div", { class: "cp-summary-host" });
  const refreshRow = el("div", { class: "cp-refresh-row" });
  const refreshBtn = el("button", {
    class: "cp-btn cp-btn-secondary",
    attrs: { type: "button" },
    text: "Refresh now",
  });
  refreshRow.append(
    el("span", { class: "cp-refresh-label", text: "Auto-refreshing every 15s" }),
    refreshBtn,
  );
  mainEl.append(summaryHost, refreshRow);

  const hostsHeading = el("h2", { text: "Hosts" });
  const hostsGrid = el("div", { class: "cp-host-grid" });
  mainEl.append(hostsHeading, hostsGrid);

  const egressHost = el("div", { class: "cp-egress-host" });
  mainEl.append(egressHost);

  const bucketsHost = el("div", { class: "cp-buckets-host" });
  mainEl.append(bucketsHost);

  async function load(showLoading) {
    if (page !== activePage) return;
    try {
      const [hostsResp, egressResp, bucketsResp] = await Promise.all([
        getHosts(controller.signal),
        getEgress(page.month, controller.signal),
        getBuckets(controller.signal),
      ]);
      if (page !== activePage) return;
      clearBanner();

      const nowMs = Date.now();
      renderHostsGrid(hostsGrid, hostsResp.hosts, nowMs);
      renderSummary(summaryHost, hostsResp, bucketsResp, nowMs);

      clearChildren(egressHost);
      egressHost.append(
        buildEgressSection({
          month: page.month,
          direction: page.direction,
          hosts: egressResp.hosts,
          onMonthChange: (m) => {
            page.month = m;
            load(false);
          },
          onDirectionChange: (d) => {
            page.direction = d;
            setStoredDirection(d);
            load(false);
          },
        }),
      );

      page.charts.forEach((c) => c.destroy());
      clearChildren(bucketsHost);
      const { node, charts } = buildBucketsSection({
        buckets: bucketsResp.buckets,
        collectors: bucketsResp.collectors,
        nowMs,
      });
      bucketsHost.append(node);
      page.charts = charts;

      setRefreshIndicator(`Last refreshed ${new Date(nowMs).toLocaleTimeString()}`);
      announce("Dashboard data refreshed.");
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (page !== activePage) return;
      showBanner(describeError(err));
    } finally {
      if (page === activePage) scheduleRefresh(() => load(false));
    }
  }

  refreshBtn.addEventListener("click", () => {
    clearRefreshTimer();
    load(true);
  });

  await load(true);
}

function renderHostsGrid(grid, hosts, nowMs) {
  clearChildren(grid);
  if (hosts.length === 0) {
    grid.append(
      emptyState({
        title: "No hosts reporting yet",
        message: "Install an agent on a host and point it at this hub:",
        code: AGENT_INSTALL_HINT,
      }),
    );
    return;
  }
  for (const summary of hosts) {
    grid.append(hostCard(summary, nowMs));
  }
}

function renderSummary(host, hostsResp, bucketsResp, nowMs) {
  const hosts = hostsResp.hosts;
  const up = hosts.filter((h) => h.status === "up");
  const down = hosts.filter((h) => h.status !== "up");
  const avgCPU = up.length > 0 ? up.reduce((sum, h) => sum + (h.latest?.cpu_percent ?? 0), 0) / up.length : 0;
  const fleetEgressBytes = hosts.reduce((sum, h) => sum + h.egress.tx_bytes, 0);
  const fleetIngressBytes = hosts.reduce((sum, h) => sum + h.egress.rx_bytes, 0);

  clearChildren(host);
  host.append(
    summaryStrip({
      hostsUp: up.length,
      hostsDown: down.length,
      avgCPU,
      fleetEgressBytes,
      fleetIngressBytes,
      bucketsTracked: bucketsResp.buckets.length,
      lastRefreshMs: nowMs,
      outdatedAgents: outdatedAgentCount(hosts),
    }),
  );
}

// ---------------------------------------------------------------------------
// Host detail page
// ---------------------------------------------------------------------------

async function renderHostDetail(hostID) {
  teardownActivePage();
  const controller = new AbortController();
  const page = { controller, charts: [], range: "1h" };
  activePage = page;

  clearChildren(mainEl);

  const backLink = el("a", { class: "cp-back-link", attrs: { href: "#/" }, text: "← Back to overview" });
  mainEl.append(backLink);

  const headerHost = el("div", { class: "cp-host-detail-header" });
  mainEl.append(headerHost);

  const rangeRow = el("div", { class: "cp-range-row", attrs: { role: "group", "aria-label": "Time range" } });
  const rangeButtons = {};
  for (const r of RANGE_OPTIONS) {
    const btn = el("button", {
      class: "cp-btn cp-btn-range",
      attrs: { type: "button", "aria-pressed": r === page.range ? "true" : "false" },
      text: r,
    });
    btn.addEventListener("click", () => {
      page.range = r;
      for (const [key, b] of Object.entries(rangeButtons)) {
        b.setAttribute("aria-pressed", key === r ? "true" : "false");
      }
      loadMetrics();
    });
    rangeButtons[r] = btn;
    rangeRow.append(btn);
  }
  mainEl.append(rangeRow);

  const chartsHost = el("div", { class: "cp-charts-grid" });
  mainEl.append(chartsHost);

  const disksHost = el("div", { class: "cp-disks-host" });
  mainEl.append(disksHost);

  const egressHost = el("div", { class: "cp-egress-host" });
  mainEl.append(egressHost);

  const agentUpdateHost = el("div", { class: "cp-agent-update-host" });
  mainEl.append(agentUpdateHost);

  async function loadSummary() {
    if (page !== activePage) return false;
    try {
      const summary = await getHost(hostID, controller.signal);
      if (page !== activePage) return false;
      clearBanner();
      renderHostDetailHeader(headerHost, summary, Date.now());
      renderHostEgressCard(egressHost, hostID, summary.egress);
      renderAgentUpdatePanel(agentUpdateHost, summary.update);
      if (summary.latest?.disks) {
        renderDisksTable(disksHost, summary.latest.disks);
      }
      setRefreshIndicator(`Last refreshed ${new Date().toLocaleTimeString()}`);
      return true;
    } catch (err) {
      if (err?.name === "AbortError") return false;
      if (err instanceof ApiError && err.status === 404) {
        clearChildren(mainEl);
        mainEl.append(
          emptyState({ title: "Host not found", message: `No host with id "${hostID}" is known to this hub.` }),
        );
        mainEl.append(el("a", { class: "cp-back-link", attrs: { href: "#/" }, text: "← Back to overview" }));
        clearRefreshTimer();
        return false;
      }
      if (page !== activePage) return false;
      showBanner(describeError(err));
      return true;
    }
  }

  async function loadMetrics() {
    if (page !== activePage) return;
    try {
      const series = await getHostMetrics(hostID, page.range, controller.signal);
      if (page !== activePage) return;
      page.charts.forEach((c) => c.destroy());
      page.charts = renderHostCharts(chartsHost, series);
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (page !== activePage) return;
      showBanner(describeError(err));
    }
  }

  async function loadAll(rescheduleOnly) {
    await runHostDetailRefresh({
      isActive: () => page === activePage,
      loadSummary,
      loadMetrics,
      rescheduleOnly,
      schedule: () => scheduleRefresh(() => loadAll(false)),
    });
  }

  await loadAll(false);
}

function renderHostDetailHeader(host, summary, nowMs) {
  clearChildren(host);
  const h = summary.host;
  const top = el("div", { class: "cp-host-detail-top" });
  top.append(
    el("h1", { class: "cp-page-title", text: h.hostname }),
    el("span", { class: `cp-chip ${summary.status === "up" ? "cp-chip-ok" : "cp-chip-exceeded"}`, text: summary.status }),
  );
  host.append(top);

  const meta = el("dl", { class: "cp-host-detail-meta" });
  const entries = [
    ["Platform", `${h.platform || "unknown"} ${h.platform_version || ""}`.trim()],
    ["Kernel", h.kernel_version || "unknown"],
    ["CPU", `${h.cpu_model || "unknown"} (${h.cpu_cores || 0} cores)`],
    ["Boot time", h.boot_time ? new Date(h.boot_time * 1000).toLocaleString() : "unknown"],
    ["Uptime", summary.latest ? formatDuration(summary.latest.uptime_seconds) : "unknown"],
    ["Agent version", h.agent_version || "unknown"],
    ["Provider", h.provider],
    ["Egress limit", h.egress_limit_bytes === 0 ? "Unlimited" : formatBytes(h.egress_limit_bytes)],
    ["Last seen", formatRelativeTimeFromUnixSeconds(summary.last_seen, nowMs)],
  ];
  // Each label/value pair is wrapped in its own grid item so the 3-column
  // grid lays out whole pairs, not interleaved individual dt/dd nodes
  // (a flat dl with 2*N children under a 3-col grid would place dt/dd
  // pairs across column boundaries once N isn't a multiple of 3).
  for (const [label, value] of entries) {
    const pair = el("div", { class: "cp-host-detail-meta-pair" });
    pair.append(el("dt", { text: label }), el("dd", { text: value }));
    meta.append(pair);
  }
  host.append(meta);

  if (summary.status !== "up") {
    host.append(
      el("p", {
        class: "cp-host-card-offline-note",
        text: `Offline since ${formatRelativeTimeFromUnixSeconds(summary.last_seen, nowMs)}`,
      }),
    );
  }
}

function renderHostEgressCard(host, hostID, egress) {
  clearChildren(host);
  const card = el("div", { class: "cp-card" });
  const headRow = el("div", { class: "cp-metric-head" });
  headRow.append(el("h2", { text: "Monthly egress" }));
  card.append(headRow);

  card.append(
    egressDirectionRow({
      label: "↑ Outbound",
      bytes: egress.tx_bytes,
      limitBytes: egress.limit_bytes,
      level: egress.level,
      projectedBytes: egress.projected_tx_bytes,
      hubOverride: egress.limit_source === "hub",
      barLabel: "Monthly outbound usage",
    }),
  );
  card.append(
    egressDirectionRow({
      label: "↓ Inbound",
      bytes: egress.rx_bytes,
      limitBytes: egress.rx_limit_bytes,
      level: egress.rx_level,
      projectedBytes: egress.projected_rx_bytes,
      hubOverride: egress.rx_limit_source === "hub",
      barLabel: "Monthly inbound usage",
    }),
  );

  card.append(
    el("a", {
      class: "cp-back-link",
      attrs: { href: `#/settings?host=${encodeURIComponent(hostID)}` },
      text: "Edit limits →",
    }),
  );

  host.append(card);
}

/**
 * renderAgentUpdatePanel renders (or clears) the host-detail "Agent
 * update" panel from a host summary's `update` field. Clears the host
 * element entirely when there's no update info to show.
 */
function renderAgentUpdatePanel(host, update) {
  clearChildren(host);
  const built = agentUpdatePanel(update);
  if (!built) return;
  if (built.copyBtn) {
    built.copyBtn.addEventListener("click", async () => {
      await copyToClipboard(built.command, undefined);
      announce("Agent update command copied to clipboard.");
    });
  }
  host.append(built.node);
}

function renderDisksTable(host, disks) {
  clearChildren(host);
  if (!disks || disks.length === 0) return;
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { text: "Disks" }));
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(
    el("tr", {
      children: ["Mountpoint", "Device", "FS", "Used", "Total", "Used %"].map((t) => el("th", { text: t })),
    }),
  );
  table.append(thead);
  const tbody = el("tbody");
  for (const d of disks) {
    tbody.append(
      el("tr", {
        children: [
          el("td", { text: d.mountpoint }),
          el("td", { text: d.device }),
          el("td", { text: d.fstype }),
          el("td", { text: formatBytes(d.used_bytes) }),
          el("td", { text: formatBytes(d.total_bytes) }),
          el("td", { text: `${d.used_percent.toFixed(1)}%` }),
        ],
      }),
    );
  }
  table.append(tbody);
  card.append(table);
  host.append(card);
}

/**
 * renderHostCharts builds the full metric chart grid for a host's
 * Series and returns the created ChartHandle instances so the caller
 * can destroy them on navigation/refresh.
 */
function renderHostCharts(host, series) {
  clearChildren(host);
  const charts = [];
  const percentFormatter = (v) => `${v.toFixed(1)}%`;
  const bitrateFormatter = (v) => formatBitrate(v, 1);
  const loadFormatter = (v) => formatLoad(v);
  const specs = [
    {
      title: "CPU %",
      series: [{ label: "CPU", color: SERIES_COLORS.cpu }],
      values: [series.cpu],
      summary: `CPU usage over time, latest ${lastOrZero(series.cpu).toFixed(1)}%`,
      valueFormatter: percentFormatter,
      yAxisFormatter: percentFormatter,
    },
    {
      title: "Memory %",
      series: [{ label: "Memory", color: SERIES_COLORS.mem }],
      values: [series.mem],
      summary: `Memory usage over time, latest ${lastOrZero(series.mem).toFixed(1)}%`,
      valueFormatter: percentFormatter,
      yAxisFormatter: percentFormatter,
    },
    {
      title: "Disk %",
      series: [{ label: "Disk", color: SERIES_COLORS.disk }],
      values: [series.disk],
      summary: `Disk usage over time, latest ${lastOrZero(series.disk).toFixed(1)}%`,
      valueFormatter: percentFormatter,
      yAxisFormatter: percentFormatter,
    },
    {
      title: "Network (bytes/s)",
      series: [
        { label: "Rx", color: SERIES_COLORS.netRx },
        { label: "Tx", color: SERIES_COLORS.netTx },
      ],
      values: [series.net_rx, series.net_tx],
      summary: `Network throughput over time, latest rx ${formatBytes(lastOrZero(series.net_rx))}/s, tx ${formatBytes(lastOrZero(series.net_tx))}/s`,
      valueFormatter: bitrateFormatter,
      yAxisFormatter: bitrateFormatter,
    },
    {
      title: "Disk IO (bytes/s)",
      series: [
        { label: "Read", color: SERIES_COLORS.diskRead },
        { label: "Write", color: SERIES_COLORS.diskWrite },
      ],
      values: [series.disk_read, series.disk_write],
      summary: `Disk IO over time, latest read ${formatBytes(lastOrZero(series.disk_read))}/s, write ${formatBytes(lastOrZero(series.disk_write))}/s`,
      valueFormatter: bitrateFormatter,
      yAxisFormatter: bitrateFormatter,
    },
    {
      title: "Load (1m)",
      series: [{ label: "Load1", color: SERIES_COLORS.load1 }],
      values: [series.load1],
      summary: `Load average (1 minute) over time, latest ${formatLoad(lastOrZero(series.load1))}`,
      valueFormatter: loadFormatter,
      yAxisFormatter: loadFormatter,
    },
  ];

  for (const spec of specs) {
    const card = el("div", { class: "cp-card cp-chart-card" });
    card.append(el("h2", { text: spec.title }));
    if (series.ts.length === 0) {
      card.append(el("p", { class: "cp-muted-small", text: "No data for this range yet." }));
      host.append(card);
      continue;
    }
    const chartWrap = el("div", {
      class: "cp-chart-wrap",
      attrs: { role: "img", "aria-label": spec.summary },
    });
    card.append(chartWrap);
    card.append(el("p", { class: "cp-sr-only-summary", text: spec.summary }));
    host.append(card);

    const chart = createTimeSeriesChart(chartWrap, {
      title: spec.title,
      series: spec.series,
      timestamps: series.ts,
      values: spec.values,
      valueFormatter: spec.valueFormatter,
      yAxisFormatter: spec.yAxisFormatter,
    });
    charts.push(chart);
  }
  return charts;
}

function lastOrZero(arr) {
  if (!arr || arr.length === 0) return 0;
  return arr[arr.length - 1];
}

function describeError(err) {
  if (err instanceof ApiError) return `API error: ${err.message}`;
  return "Unable to reach the hub API. Showing last known data.";
}

// ---------------------------------------------------------------------------
// Settings page
// ---------------------------------------------------------------------------

async function renderSettings(focusHostID) {
  teardownActivePage();
  const controller = new AbortController();
  const page = { controller, charts: [] };
  activePage = page;

  clearChildren(mainEl);
  clearRefreshTimer();
  setRefreshIndicator("");

  try {
    const node = await buildSettingsPage({ signal: controller.signal, announce, focusHostID });
    if (page !== activePage) return;
    clearChildren(mainEl);
    mainEl.append(node);
    if (focusHostID) {
      const row = document.getElementById(`cp-limits-row-${focusHostID}`);
      row?.scrollIntoView({ block: "center" });
    }
  } catch (err) {
    if (err?.name === "AbortError") return;
    if (page !== activePage) return;
    clearChildren(mainEl);
    mainEl.append(el("h1", { class: "cp-page-title", text: "Settings" }));
    showBanner(describeError(err));
  }
}

// ---------------------------------------------------------------------------
// Global update banner (all pages; polls GET /api/v1/version every 30 min)
// ---------------------------------------------------------------------------

/**
 * renderUpdateBanner re-renders the global update banner from the
 * cached latestVersionInfo and the persisted dismissed-tag, showing or
 * clearing it as appropriate. Safe to call anytime (e.g. after a route
 * change) without re-fetching.
 */
function renderUpdateBanner() {
  clearChildren(updateBannerHost);
  if (!shouldShowUpdateBanner(latestVersionInfo, getDismissedUpdateTag())) return;

  const { node, copyBtn, dismissBtn, command } = updateBanner({ versionInfo: latestVersionInfo });
  copyBtn.addEventListener("click", async () => {
    await copyToClipboard(command);
    announce("Update command copied to clipboard.");
  });
  dismissBtn.addEventListener("click", () => {
    setDismissedUpdateTag(latestVersionInfo.latest_version || "");
    renderUpdateBanner();
  });
  updateBannerHost.append(node);
}

/**
 * pollVersion fetches GET /api/v1/version, caches the result, and
 * re-renders the update banner. Errors are swallowed (the banner simply
 * doesn't update this cycle) since this must never disrupt the rest of
 * the dashboard.
 */
async function pollVersion() {
  try {
    latestVersionInfo = await getVersion();
    renderUpdateBanner();
  } catch {
    // Network/auth error fetching version info: leave any previously
    // shown banner as-is rather than surfacing a second error banner.
  }
}

function startUpdateBannerPolling() {
  pollVersion();
  updateBannerTimer = setInterval(pollVersion, VERSION_REFRESH_INTERVAL_MS);
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

function route() {
  const hash = location.hash || "#/";
  const hostMatch = hash.match(/^#\/host\/([^/?]+)$/);
  if (hostMatch) {
    renderHostDetail(decodeURIComponent(hostMatch[1]));
    return;
  }
  const settingsMatch = hash.match(/^#\/settings(?:\?(.*))?$/);
  if (settingsMatch) {
    const params = new URLSearchParams(settingsMatch[1] || "");
    renderSettings(params.get("host") || undefined);
    return;
  }
  renderOverview();
}

function handleVisibilityChange() {
  if (document.hidden) {
    clearRefreshTimer();
  } else if (activePage) {
    // Trigger an immediate refresh on return rather than waiting out a
    // stale interval.
    route();
  }
}

function bootstrap() {
  mainEl = document.getElementById("cp-main");
  liveRegionEl = document.getElementById("cp-live-region");
  refreshIndicatorEl = document.getElementById("cp-refresh-indicator");
  refreshBannerHost = document.getElementById("cp-banner-host");
  updateBannerHost = document.getElementById("cp-update-banner-host");

  const tokenDialog = document.getElementById("cp-token-dialog");
  registerTokenDialog({
    dialog: tokenDialog,
    form: document.getElementById("cp-token-form"),
    input: document.getElementById("cp-token-input"),
    errorEl: document.getElementById("cp-token-error"),
  });
  document.getElementById("cp-token-cancel").addEventListener("click", () => {
    tokenDialog.close("cancel");
  });

  window.addEventListener("hashchange", route);
  document.addEventListener("visibilitychange", handleVisibilityChange);

  startUpdateBannerPolling();
  route();
}

bootstrap();

// Exported for potential future test hooks; not used by index.html directly.
export { apiFetch };
