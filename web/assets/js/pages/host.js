// host.js — host detail page (#/host/<id>): header card with badge row
// (status/hostname/platform/uptime/kernel/CPU/agent version/provider),
// time-range select + layout toggle, synced uPlot area charts with
// tooltips (destroy+recreate on theme change), disks table, monthly
// traffic card, and agent update card. Redesigned per SPEC-v0.4 §4 on
// top of the v0.3.x data layer (zero feature loss).
import { getHost, getHostMetrics, getHostInventory, ApiError } from "../core/api.js";
import { el, clearChildren, emptyState, errorBanner, egressDirectionRow, agentUpdatePanel, statusDot } from "../ui/components.js";
import { selectField } from "../ui/select.js";
import { icon } from "../ui/icons.js";
import { formatBytes, formatBitrate, formatDuration, formatRelativeTimeFromUnixSeconds, formatLoad } from "../core/format.js";
import { createTimeSeriesChart, SERIES_COLORS } from "./charts.js";
import { runHostDetailRefresh } from "../core/refresh.js";
import { getItem, setItem } from "../core/store.js";
import {
  formatPublishedPort,
  formatListeningAddress,
  sortPorts,
  sortContainers,
  processLabel,
  dockerStatusInfo,
  containerHealthLabel,
  containerComposeLabel,
} from "../core/ports.js";

const RANGE_OPTIONS = [
  { value: "1h", label: "1 hour" },
  { value: "6h", label: "6 hours" },
  { value: "24h", label: "24 hours" },
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
];

const LAYOUT_STORAGE_KEY = "cp_host_layout";

/**
 * mountHostDetailPage renders the host detail page into container.
 * @param {HTMLElement} container
 * @param {string} hostID
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @param {(cmd: string, announceText: string) => Promise<void>} ctx.copyToClipboard
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountHostDetailPage(container, hostID, { announce, copyToClipboard }) {
  const controller = new AbortController();
  const state = {
    range: "1h",
    charts: [],
    notFound: false,
    layout: getItem(LAYOUT_STORAGE_KEY, "2") === "1" ? "1" : "2",
    lastSeries: null,
  };

  clearChildren(container);
  const backLink = el("a", { class: "cp-back-link", attrs: { href: "#/" }, text: "← Back to overview" });
  container.append(backLink);

  const bannerHost = el("div");
  const headerHost = el("div", { class: "cp-card cp-host-detail-header" });
  container.append(bannerHost, headerHost);

  const chartsHost = el("div", { class: `cp-charts-grid cp-charts-grid-cols-${state.layout}` });
  const disksHost = el("div", { class: "cp-disks-host" });
  const egressHost = el("div", { class: "cp-egress-host" });
  const inventoryHost = el("div", { class: "cp-inventory-host" });
  const agentUpdateHost = el("div", { class: "cp-agent-update-host" });
  container.append(chartsHost, disksHost, egressHost, inventoryHost, agentUpdateHost);

  const onThemeChange = () => {
    if (state.lastSeries) {
      state.charts.forEach((c) => c.destroy());
      state.charts = renderHostCharts(chartsHost, state.lastSeries);
    }
  };
  window.addEventListener("cp-theme-change", onThemeChange);

  function showBanner(message) {
    clearChildren(bannerHost);
    bannerHost.append(errorBanner(message));
  }
  function clearBanner() {
    clearChildren(bannerHost);
  }

  async function loadSummary() {
    try {
      const summary = await getHost(hostID, controller.signal);
      clearBanner();
      renderHostDetailHeader(headerHost, summary, Date.now(), { onRangeChange: handleRangeChange, onLayoutChange: handleLayoutChange, range: state.range, layout: state.layout });
      renderHostEgressCard(egressHost, hostID, summary.egress);
      renderAgentUpdatePanel(agentUpdateHost, summary.update, announce, copyToClipboard);
      if (summary.latest?.disks) {
        renderDisksTable(disksHost, summary.latest.disks);
      } else {
        clearChildren(disksHost);
      }
      loadInventory();
      return true;
    } catch (err) {
      if (err?.name === "AbortError") return false;
      if (err instanceof ApiError && err.status === 404) {
        state.notFound = true;
        clearChildren(container);
        container.append(
          emptyState({ title: "Host not found", message: `No host with id "${hostID}" is known to this hub.`, iconName: "circleX" }),
        );
        container.append(el("a", { class: "cp-back-link", attrs: { href: "#/" }, text: "← Back to overview" }));
        return false;
      }
      showBanner(describeError(err));
      return true;
    }
  }

  async function loadInventory() {
    try {
      const inventory = await getHostInventory(hostID, controller.signal);
      renderServicesCard(inventoryHost, inventory);
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (err instanceof ApiError && err.status === 404) {
        clearChildren(inventoryHost);
        return;
      }
      // Non-404 inventory errors don't block the rest of the page —
      // just leave the card in its last-known state.
    }
  }

  async function loadMetrics() {
    try {
      const series = await getHostMetrics(hostID, state.range, controller.signal);
      state.lastSeries = series;
      state.charts.forEach((c) => c.destroy());
      state.charts = renderHostCharts(chartsHost, series);
    } catch (err) {
      if (err?.name === "AbortError") return;
      showBanner(describeError(err));
    }
  }

  function handleRangeChange(newRange) {
    state.range = newRange;
    loadMetrics();
  }

  function handleLayoutChange(newLayout) {
    state.layout = newLayout;
    setItem(LAYOUT_STORAGE_KEY, newLayout);
    chartsHost.className = `cp-charts-grid cp-charts-grid-cols-${newLayout}`;
  }

  async function refresh() {
    await runHostDetailRefresh({
      isActive: () => !state.notFound,
      loadSummary,
      loadMetrics,
      rescheduleOnly: false,
      schedule: () => {},
    });
  }

  function teardown() {
    controller.abort();
    window.removeEventListener("cp-theme-change", onThemeChange);
    state.charts.forEach((c) => c.destroy());
    state.charts = [];
  }

  return { refresh, teardown };
}

function renderHostDetailHeader(host, summary, nowMs, { onRangeChange, onLayoutChange, range, layout }) {
  clearChildren(host);
  const h = summary.host;
  const isUp = summary.status === "up";

  const top = el("div", { class: "cp-host-detail-top" });
  const titleGroup = el("div", { class: "cp-host-detail-title-group" });
  titleGroup.append(el("h1", { class: "cp-page-title", text: h.hostname }));
  top.append(titleGroup);

  const rightControls = el("div", { class: "cp-host-detail-controls" });
  const { node: rangeNode, select: rangeSelect } = selectField({
    id: "cp-host-range",
    ariaLabel: "Time range",
    options: RANGE_OPTIONS,
    value: range,
  });
  rangeSelect.addEventListener("change", () => onRangeChange(rangeSelect.value));
  rightControls.append(rangeNode);
  rightControls.append(buildLayoutToggle(layout, onLayoutChange));
  top.append(rightControls);
  host.append(top);

  const badgeRow = el("div", { class: "cp-host-detail-badges" });
  const badges = [
    statusBadge(isUp, summary.status),
    textBadge(h.hostname),
    textBadge(`${h.platform || "unknown"} ${h.platform_version || ""}`.trim()),
    textBadge(`Uptime ${summary.latest ? formatDuration(summary.latest.uptime_seconds) : "unknown"}`),
    textBadge(`Kernel ${h.kernel_version || "unknown"}`),
    textBadge(`${h.cpu_model || "unknown"} (${h.cpu_cores || 0} cores)`),
    textBadge(`Agent ${h.agent_version || "unknown"}`),
    textBadge(h.provider ? h.provider.toUpperCase() : "OTHER"),
  ];
  badges.forEach((b, i) => {
    if (i > 0) badgeRow.append(el("span", { class: "cp-host-detail-badge-divider", attrs: { "aria-hidden": "true" } }));
    badgeRow.append(b);
  });
  host.append(badgeRow);

  if (!isUp) {
    host.append(
      el("p", {
        class: "cp-host-card-offline-note",
        text: `Offline since ${formatRelativeTimeFromUnixSeconds(summary.last_seen, nowMs)}`,
      }),
    );
  }
}

function statusBadge(isUp, status) {
  const wrap = el("span", { class: "cp-host-detail-badge cp-host-detail-badge-status" });
  wrap.append(statusDot(status));
  wrap.append(el("span", { text: isUp ? "Up" : "Down" }));
  return wrap;
}

function textBadge(text) {
  return el("span", { class: "cp-host-detail-badge", text });
}

function buildLayoutToggle(current, onChange) {
  const group = el("div", { class: "cp-segmented cp-layout-toggle", attrs: { role: "group", "aria-label": "Chart layout" } });
  const oneBtn = el("button", {
    class: "cp-btn cp-segmented-btn cp-btn-icon",
    attrs: { type: "button", "aria-pressed": current === "1" ? "true" : "false", "aria-label": "One column", title: "One column" },
  });
  oneBtn.append(icon("rows3"));
  const twoBtn = el("button", {
    class: "cp-btn cp-segmented-btn cp-btn-icon",
    attrs: { type: "button", "aria-pressed": current === "2" ? "true" : "false", "aria-label": "Two columns", title: "Two columns" },
  });
  twoBtn.append(icon("columns3"));
  oneBtn.addEventListener("click", () => {
    oneBtn.setAttribute("aria-pressed", "true");
    twoBtn.setAttribute("aria-pressed", "false");
    onChange("1");
  });
  twoBtn.addEventListener("click", () => {
    oneBtn.setAttribute("aria-pressed", "false");
    twoBtn.setAttribute("aria-pressed", "true");
    onChange("2");
  });
  group.append(oneBtn, twoBtn);
  return group;
}

function renderHostEgressCard(host, hostID, egress) {
  clearChildren(host);
  const card = el("div", { class: "cp-card" });
  const headRow = el("div", { class: "cp-metric-head" });
  headRow.append(el("h2", { text: "Monthly traffic" }));
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
      attrs: { href: `#/settings/limits?host=${encodeURIComponent(hostID)}` },
      text: "Edit limits →",
    }),
  );

  host.append(card);
}

function renderAgentUpdatePanel(host, update, announce, copyToClipboard) {
  clearChildren(host);
  const built = agentUpdatePanel(update);
  if (!built) return;
  if (built.copyBtn) {
    built.copyBtn.addEventListener("click", async () => {
      await copyToClipboard(built.command, undefined);
      announce?.("Agent update command copied to clipboard.");
    });
  }
  host.append(built.node);
}

/**
 * renderServicesCard builds the host detail "Services & ports" card
 * (SPEC-v0.5 §D): a Docker containers table (or an empty/permission/
 * error state with the exact fix command) followed by a listening
 * ports table.
 * @param {HTMLElement} host
 * @param {Object} inventory a models.Inventory JSON object
 */
function renderServicesCard(host, inventory) {
  clearChildren(host);
  const card = el("div", { class: "cp-card cp-services-card" });
  card.append(el("h2", { text: "Services & ports" }));

  card.append(renderDockerSection(inventory.docker));
  card.append(renderPortsSection(inventory.ports));

  host.append(card);
}

function renderDockerSection(docker) {
  const section = el("div", { class: "cp-services-section" });
  section.append(el("h3", { class: "cp-services-section-title", text: "Docker containers" }));

  if (docker.status !== "ok") {
    const info = dockerStatusInfo(docker.status);
    const box = el("div", { class: "cp-services-status-box" });
    box.append(icon(docker.status === "permission_denied" ? "triangleAlert" : "box"));
    const textCol = el("div");
    textCol.append(el("p", { class: "cp-services-status-title", text: info.title }));
    textCol.append(el("p", { class: "cp-muted-small", text: docker.error || info.message }));
    if (info.fixCommand) {
      const pre = el("pre", { class: "cp-code-block cp-code-block-wrap" });
      pre.append(el("code", { text: info.fixCommand }));
      textCol.append(pre);
    }
    box.append(textCol);
    section.append(box);
    return section;
  }

  if (!docker.containers || docker.containers.length === 0) {
    section.append(el("p", { class: "cp-muted-small", text: "No containers reported." }));
    return section;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(
    el("tr", { children: ["Name", "Compose", "Image", "State", "Health", "Ports", "Status"].map((t) => el("th", { text: t })) }),
  );
  table.append(thead);
  const tbody = el("tbody");
  for (const c of sortContainers(docker.containers)) {
    tbody.append(containerRow(c));
  }
  table.append(tbody);
  tableWrap.append(table);
  section.append(tableWrap);
  return section;
}

function containerRow(c) {
  const isRunning = c.state === "running";
  const health = containerHealthLabel(c.health);
  const compose = containerComposeLabel(c);

  const portsCell = el("td");
  const portsWrap = el("div", { class: "cp-services-port-chips" });
  for (const p of c.ports || []) {
    portsWrap.append(el("span", { class: "cp-chip cp-chip-other cp-services-port-chip", text: formatPublishedPort(p) }));
  }
  if ((c.ports || []).length === 0) portsWrap.append(el("span", { class: "cp-muted-small", text: "—" }));
  portsCell.append(portsWrap);

  return el("tr", {
    children: [
      el("td", { text: c.name }),
      el("td", { text: compose || "—" }),
      el("td", { text: c.image }),
      el("td", { children: [el("span", { class: `cp-chip ${isRunning ? "cp-chip-ok" : "cp-chip-other"}`, text: c.state })] }),
      el("td", { text: health || "—" }),
      portsCell,
      el("td", { text: c.status }),
    ],
  });
}

function renderPortsSection(ports) {
  const section = el("div", { class: "cp-services-section" });
  section.append(el("h3", { class: "cp-services-section-title", text: "Listening ports" }));

  if (!ports || ports.length === 0) {
    section.append(el("p", { class: "cp-muted-small", text: "No listening ports reported." }));
    return section;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(el("tr", { children: ["Proto", "Address", "Process", "Container"].map((t) => el("th", { text: t })) }));
  table.append(thead);
  const tbody = el("tbody");
  for (const p of sortPorts(ports)) {
    tbody.append(
      el("tr", {
        children: [
          el("td", { text: p.proto.toUpperCase() }),
          el("td", { children: [el("span", { class: "cp-tabular", text: formatListeningAddress(p) })] }),
          el("td", { text: processLabel(p) }),
          el("td", { text: p.container_id ? p.container_id.slice(0, 12) : "—" }),
        ],
      }),
    );
  }
  table.append(tbody);
  tableWrap.append(table);
  section.append(tableWrap);
  return section;
}

function renderDisksTable(host, disks) {
  clearChildren(host);
  if (!disks || disks.length === 0) return;
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { text: "Disks" }));
  const tableWrap = el("div", { class: "cp-table-wrap" });
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
  tableWrap.append(table);
  card.append(tableWrap);
  host.append(card);
}

/**
 * renderHostCharts builds the full metric chart grid for a host's
 * Series and returns the created ChartHandle instances so the caller
 * can destroy them on navigation/refresh/theme change. Charts share a
 * synced cursor (uPlot's `cursor.sync`) so hovering one highlights the
 * same timestamp across all of them.
 */
function renderHostCharts(host, series) {
  clearChildren(host);
  const charts = [];
  const syncKey = "cp-host-charts";
  const percentFormatter = (v) => `${v.toFixed(1)}%`;
  const bitrateFormatter = (v) => formatBitrate(v, 1);
  const loadFormatter = (v) => formatLoad(v);
  const specs = [
    {
      title: "CPU Usage",
      description: "Average system-wide CPU utilization",
      series: [{ label: "CPU", color: SERIES_COLORS.cpu }],
      values: [series.cpu],
      summary: `CPU usage over time, latest ${lastOrZero(series.cpu).toFixed(1)}%`,
      valueFormatter: percentFormatter,
      yAxisFormatter: percentFormatter,
    },
    {
      title: "Memory Usage",
      description: "True available memory usage (total minus available, not naive free)",
      series: [{ label: "Memory", color: SERIES_COLORS.mem }],
      values: [series.mem],
      summary: `Memory usage over time, latest ${lastOrZero(series.mem).toFixed(1)}%`,
      valueFormatter: percentFormatter,
      yAxisFormatter: percentFormatter,
    },
    {
      title: "Disk Usage",
      description: "Aggregate disk usage across all mounted filesystems",
      series: [{ label: "Disk", color: SERIES_COLORS.disk }],
      values: [series.disk],
      summary: `Disk usage over time, latest ${lastOrZero(series.disk).toFixed(1)}%`,
      valueFormatter: percentFormatter,
      yAxisFormatter: percentFormatter,
    },
    {
      title: "Bandwidth",
      description: "Network receive/transmit throughput",
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
      title: "Disk I/O",
      description: "Disk read/write throughput",
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
      title: "Load average",
      description: "System load average (1 minute)",
      series: [{ label: "Load1", color: SERIES_COLORS.load1 }],
      values: [series.load1],
      summary: `Load average (1 minute) over time, latest ${formatLoad(lastOrZero(series.load1))}`,
      valueFormatter: loadFormatter,
      yAxisFormatter: loadFormatter,
    },
  ];

  for (const spec of specs) {
    const card = el("div", { class: "cp-card cp-chart-card" });
    const titleRow = el("div", { class: "cp-chart-card-head" });
    titleRow.append(el("h2", { text: spec.title }));
    titleRow.append(el("p", { class: "cp-muted-small", text: spec.description }));
    card.append(titleRow);
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
      syncKey,
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
