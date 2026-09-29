// overview.js — fleet overview page (#/): "All Systems" card (sortable
// filterable table with a grid view toggle, columns dropdown, mobile
// card list, empty state), a compact stat-cards row, a "Monthly
// traffic" card, and an "Object storage" card. Redesigned per
// SPEC-v0.4 §4 on top of the v0.3.x data layer (zero feature loss:
// egress split, hub-override badges, update badges, buckets free tier,
// 15s refresh + visibility pause + AbortController all preserved).
import { getHosts, getEgress, getBuckets, ApiError } from "../core/api.js";
import {
  el,
  clearChildren,
  emptyState,
  errorBanner,
  statusDot,
  providerBadge,
  progressBar,
  levelClassForBar,
  egressLevelChip,
  hubOverrideBadge,
} from "../ui/components.js";
import { icon } from "../ui/icons.js";
import { sortableHeader, nextSortState, sortRows } from "../ui/table.js";
import { createDropdown, dropdownSeparator } from "../ui/dropdown.js";
import {
  formatBytes,
  formatBitrate,
  formatPercent,
  formatDuration,
  formatRelativeTimeFromUnixSeconds,
  formatLoad,
  formatNumber,
} from "../core/format.js";
import { getItem, setItem, getJSON, setJSON } from "../core/store.js";
import { buildEgressSection, currentMonth, getStoredDirection, setStoredDirection } from "./egress.js";
import { buildBucketsSection } from "./buckets.js";
import { outdatedAgentCount, agentVersionText, hostUpdateBadgeText } from "../core/updates.js";
import {
  COLUMNS,
  columnAccessors,
  thresholdBarClass,
  filterRows,
  agentDotLevel,
  normalizeVisibleColumns,
  SORT_STORAGE_KEY,
  COLUMNS_STORAGE_KEY,
  VIEW_STORAGE_KEY,
} from "./hosttable.js";

const AGENT_INSTALL_HINT =
  "curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh | sudo bash -s -- --hub-url <HUB_URL> --token <AGENT_TOKEN>";

/**
 * mountOverviewPage renders the overview page into container and
 * returns a handle the shell can use to refresh/teardown it.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: (showLoading: boolean) => Promise<void>, teardown: () => void}}
 */
export function mountOverviewPage(container, { announce }) {
  const controller = new AbortController();
  const state = {
    month: currentMonth(),
    direction: getStoredDirection(),
    charts: [],
    hosts: [],
    filter: "",
    sort: getJSON(SORT_STORAGE_KEY, { key: "system", direction: "asc" }),
    visibleColumns: normalizeVisibleColumns(getJSON(COLUMNS_STORAGE_KEY, null)),
    view: getItem(VIEW_STORAGE_KEY, "table") === "grid" ? "grid" : "table",
  };

  clearChildren(container);
  container.append(el("h1", { class: "cp-page-title", text: "Fleet overview" }));
  container.append(el("p", { class: "cp-page-subtitle", text: "Updated every 15 seconds." }));

  const bannerHost = el("div");
  const statCardsHost = el("div", { class: "cp-stat-cards" });
  const systemsCard = el("section", { class: "cp-card cp-systems-card", attrs: { "aria-labelledby": "cp-systems-heading" } });
  const egressHost = el("div", { class: "cp-egress-host" });
  const bucketsHost = el("div", { class: "cp-buckets-host" });
  container.append(bannerHost, statCardsHost, systemsCard, egressHost, bucketsHost);

  function showBanner(message) {
    clearChildren(bannerHost);
    bannerHost.append(errorBanner(message));
  }
  function clearBanner() {
    clearChildren(bannerHost);
  }

  function renderSystemsCardBody() {
    renderSystemsCard(systemsCard, state, {
      onFilterChange: (q) => {
        state.filter = q;
        renderSystemsCardBody();
      },
      onSortChange: (key) => {
        state.sort = nextSortState(state.sort, key);
        setJSON(SORT_STORAGE_KEY, state.sort);
        renderSystemsCardBody();
      },
      onColumnsChange: (cols) => {
        state.visibleColumns = cols;
        setJSON(COLUMNS_STORAGE_KEY, cols);
        renderSystemsCardBody();
      },
      onViewChange: (v) => {
        state.view = v;
        setItem(VIEW_STORAGE_KEY, v);
        renderSystemsCardBody();
      },
    });
  }

  async function refresh() {
    try {
      const [hostsResp, egressResp, bucketsResp] = await Promise.all([
        getHosts(controller.signal),
        getEgress(state.month, controller.signal),
        getBuckets(controller.signal),
      ]);
      clearBanner();

      const nowMs = Date.now();
      state.hosts = hostsResp.hosts;
      state.nowMs = nowMs;
      renderSystemsCardBody();
      renderStatCards(statCardsHost, hostsResp, bucketsResp, nowMs);

      clearChildren(egressHost);
      egressHost.append(
        buildEgressSection({
          month: state.month,
          direction: state.direction,
          hosts: egressResp.hosts,
          onMonthChange: (m) => {
            state.month = m;
            refresh(false);
          },
          onDirectionChange: (d) => {
            state.direction = d;
            setStoredDirection(d);
            refresh(false);
          },
        }),
      );

      state.charts.forEach((c) => c.destroy());
      clearChildren(bucketsHost);
      const { node, charts } = buildBucketsSection({
        buckets: bucketsResp.buckets,
        collectors: bucketsResp.collectors,
        nowMs,
      });
      bucketsHost.append(node);
      state.charts = charts;

      announce?.("Dashboard data refreshed.");
    } catch (err) {
      if (err?.name === "AbortError") return;
      showBanner(describeError(err));
    }
  }

  function teardown() {
    controller.abort();
    state.charts.forEach((c) => c.destroy());
    state.charts = [];
  }

  return { refresh, teardown };
}

// ---------------------------------------------------------------------------
// Stat cards row
// ---------------------------------------------------------------------------

function renderStatCards(host, hostsResp, bucketsResp, nowMs) {
  const hosts = hostsResp.hosts;
  const up = hosts.filter((h) => h.status === "up");
  const down = hosts.filter((h) => h.status !== "up");
  const avgCPU = up.length > 0 ? up.reduce((sum, h) => sum + (h.latest?.cpu_percent ?? 0), 0) / up.length : 0;
  const fleetEgressBytes = hosts.reduce((sum, h) => sum + h.egress.tx_bytes, 0);
  const fleetIngressBytes = hosts.reduce((sum, h) => sum + h.egress.rx_bytes, 0);
  const outdated = outdatedAgentCount(hosts);

  clearChildren(host);
  const cards = [
    { icon: "server", label: "Hosts up", value: formatNumber(up.length), tone: "ok" },
    { icon: "wifiOff", label: "Hosts down", value: formatNumber(down.length), tone: down.length > 0 ? "critical" : "ok" },
    { icon: "cpu", label: "Avg CPU", value: formatPercent(avgCPU), tone: "neutral" },
    { icon: "arrowUp", label: "Fleet outbound (mo.)", value: formatBytes(fleetEgressBytes), tone: "neutral" },
    { icon: "arrowDown", label: "Fleet inbound (mo.)", value: formatBytes(fleetIngressBytes), tone: "neutral" },
    { icon: "database", label: "Buckets tracked", value: formatNumber(bucketsResp.buckets.length), tone: "neutral" },
  ];
  if (outdated > 0) {
    cards.push({ icon: "cloudUpload", label: "Agents outdated", value: formatNumber(outdated), tone: "warning" });
  }

  for (const c of cards) {
    const card = el("div", { class: `cp-stat-card cp-stat-card-${c.tone}` });
    const iconWrap = el("div", { class: "cp-stat-card-icon" });
    iconWrap.append(icon(c.icon));
    card.append(iconWrap);
    const textCol = el("div", { class: "cp-stat-card-text" });
    textCol.append(el("span", { class: "cp-stat-card-value", text: c.value }));
    textCol.append(el("span", { class: "cp-stat-card-label", text: c.label }));
    card.append(textCol);
    host.append(card);
  }
  void nowMs;
}

// ---------------------------------------------------------------------------
// "All Systems" card: header (title, filter, view toggle, columns) +
// table/grid/mobile-card body.
// ---------------------------------------------------------------------------

function renderSystemsCard(card, state, handlers) {
  clearChildren(card);

  const head = el("div", { class: "cp-systems-head" });
  const titleCol = el("div");
  titleCol.append(el("h2", { id: "cp-systems-heading", text: "All Systems" }));
  titleCol.append(el("p", { class: "cp-page-subtitle", text: `${state.hosts.length} host${state.hosts.length === 1 ? "" : "s"}` }));
  head.append(titleCol);

  const controls = el("div", { class: "cp-systems-controls" });

  const filterWrap = el("div", { class: "cp-input-group cp-systems-filter" });
  const filterIcon = el("span", { class: "cp-input-group-icon", attrs: { "aria-hidden": "true" } });
  filterIcon.append(icon("search"));
  const filterInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input",
      attrs: { type: "search", placeholder: "Filter hosts…", "aria-label": "Filter hosts", value: state.filter },
    })
  );
  filterInput.addEventListener("input", () => handlers.onFilterChange(filterInput.value));
  filterWrap.append(filterIcon, filterInput);
  controls.append(filterWrap);

  controls.append(buildViewToggle(state.view, handlers.onViewChange));
  controls.append(buildColumnsDropdown(state.visibleColumns, handlers.onColumnsChange));

  head.append(controls);
  card.append(head);

  const filtered = filterRows(state.hosts, state.filter);

  if (state.hosts.length === 0) {
    card.append(
      emptyState({
        title: "Add your first agent",
        message: "Install an agent on a host and point it at this hub:",
        code: AGENT_INSTALL_HINT,
        iconName: "server",
      }),
    );
    return;
  }

  if (filtered.length === 0) {
    card.append(el("p", { class: "cp-muted", text: `No hosts match "${state.filter}".` }));
    return;
  }

  const sorted = sortRows(filtered, state.sort, columnAccessors);

  if (state.view === "grid") {
    card.append(buildGridView(sorted, state));
  } else {
    card.append(buildTableView(sorted, state, handlers));
    card.append(buildMobileCardList(sorted, state));
  }
}

function buildViewToggle(current, onChange) {
  const group = el("div", { class: "cp-segmented cp-view-toggle", attrs: { role: "group", "aria-label": "View" } });
  const tableBtn = el("button", {
    class: "cp-btn cp-segmented-btn cp-btn-icon",
    attrs: { type: "button", "aria-pressed": current === "table" ? "true" : "false", "aria-label": "Table view", title: "Table view" },
  });
  tableBtn.append(icon("table"));
  const gridBtn = el("button", {
    class: "cp-btn cp-segmented-btn cp-btn-icon",
    attrs: { type: "button", "aria-pressed": current === "grid" ? "true" : "false", "aria-label": "Grid view", title: "Grid view" },
  });
  gridBtn.append(icon("layoutGrid"));
  tableBtn.addEventListener("click", () => onChange("table"));
  gridBtn.addEventListener("click", () => onChange("grid"));
  group.append(tableBtn, gridBtn);
  return group;
}

function buildColumnsDropdown(visibleColumns, onChange) {
  const trigger = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-btn cp-btn-secondary cp-btn-icon", attrs: { type: "button", "aria-label": "Columns", title: "Columns" } })
  );
  trigger.append(icon("columns3"));

  createDropdown({
    trigger,
    align: "right",
    buildMenu: () => {
      const wrap = el("div");
      wrap.append(el("div", { class: "cp-dropdown-heading", text: "Columns" }));
      for (const col of COLUMNS) {
        if (col.alwaysVisible) continue;
        const checked = visibleColumns.includes(col.key);
        const item = el("label", { class: "cp-dropdown-checkbox-item" });
        const checkbox = /** @type {HTMLInputElement} */ (
          el("input", { attrs: { type: "checkbox" } })
        );
        checkbox.checked = checked;
        checkbox.addEventListener("change", () => {
          const next = checkbox.checked
            ? Array.from(new Set([...visibleColumns, col.key]))
            : visibleColumns.filter((k) => k !== col.key);
          onChange(next);
        });
        item.append(checkbox, el("span", { text: col.label }));
        wrap.append(item);
      }
      wrap.append(dropdownSeparator());
      const resetBtn = el("button", { class: "cp-dropdown-item", attrs: { type: "button" }, text: "Show all" });
      resetBtn.addEventListener("click", () => onChange(COLUMNS.map((c) => c.key)));
      wrap.append(resetBtn);
      return wrap;
    },
  });

  return trigger;
}

// ---------------------------------------------------------------------------
// Table view
// ---------------------------------------------------------------------------

function buildTableView(rows, state, handlers) {
  const wrap = el("div", { class: "cp-table-wrap cp-systems-table-wrap" });
  const table = el("table", { class: "cp-table cp-systems-table" });
  const thead = el("thead");
  const headRow = el("tr");
  for (const col of COLUMNS) {
    if (!state.visibleColumns.includes(col.key)) continue;
    if (col.sortable) {
      headRow.append(sortableHeader({ label: col.label, sortKey: col.key, currentSort: state.sort, onSort: handlers.onSortChange }));
    } else {
      headRow.append(el("th", { text: col.label }));
    }
  }
  thead.append(headRow);
  table.append(thead);

  const tbody = el("tbody");
  for (const row of rows) {
    tbody.append(buildTableRow(row, state));
  }
  table.append(tbody);
  wrap.append(table);
  return wrap;
}

function buildTableRow(row, state) {
  const isUp = row.status === "up";
  const tr = el("tr", { class: `cp-systems-row ${isUp ? "" : "cp-systems-row-offline"}`.trim() });
  const visible = (key) => state.visibleColumns.includes(key);

  if (visible("system")) tr.append(td(systemCell(row)));
  if (visible("cpu")) tr.append(td(row.latest ? metricCell(row.latest.cpu_percent) : dashCell()));
  if (visible("memory")) tr.append(td(row.latest ? metricCell(row.latest.mem_used_percent) : dashCell()));
  if (visible("disk")) tr.append(td(row.latest ? metricCell(row.latest.disk_used_percent) : dashCell()));
  if (visible("net")) tr.append(td(row.latest ? netCell(row.latest) : dashCell()));
  if (visible("outbound")) tr.append(td(egressCell(row.egress, "out")));
  if (visible("inbound")) tr.append(td(egressCell(row.egress, "in")));
  if (visible("load")) tr.append(td(el("span", { class: "cp-tabular", text: row.latest ? formatLoad(row.latest.load1) : "–" })));
  if (visible("agent")) tr.append(td(agentCell(row)));
  if (visible("lastSeen")) tr.append(td(el("span", { text: formatRelativeTimeFromUnixSeconds(row.last_seen, state.nowMs) })));

  // Make the whole row a keyboard-accessible link by wrapping cell
  // content isn't enough for click-anywhere; add an invisible full-row
  // link as the first focus target while keeping cell-level content
  // for layout. Simpler + fully accessible: make the row itself
  // activatable via a link overlay in the System cell (already an <a>)
  // AND forward row clicks/keydowns to that link for click-anywhere.
  const link = tr.querySelector("a.cp-systems-row-link");
  tr.addEventListener("click", (ev) => {
    if (ev.target.closest("a,button,input,label")) return;
    link?.click();
  });
  return tr;
}

function td(child) {
  return el("td", { children: [child] });
}

function systemCell(row) {
  const wrap = el("div", { class: "cp-systems-system-cell" });
  const link = /** @type {HTMLAnchorElement} */ (
    el("a", {
      class: "cp-systems-row-link",
      attrs: { href: `#/host/${encodeURIComponent(row.host.id)}` },
    })
  );
  link.append(statusDot(row.status));
  const nameCol = el("span", { class: "cp-systems-system-name-col" });
  nameCol.append(el("span", { class: "cp-systems-system-name", text: row.host.hostname }));
  link.append(nameCol);
  wrap.append(link);
  wrap.append(providerBadge(row.host.provider));
  const updateInfo = hostUpdateBadgeText(row.update);
  if (updateInfo) {
    wrap.append(
      el("span", {
        class: "cp-badge cp-badge-update",
        text: "update",
        attrs: { title: updateInfo.label, "aria-label": updateInfo.label },
      }),
    );
  }
  return wrap;
}

function metricCell(percent) {
  const wrap = el("div", { class: "cp-systems-metric-cell" });
  wrap.append(el("span", { class: "cp-tabular", text: formatPercent(percent) }));
  wrap.append(
    progressBar({
      value: percent,
      max: 100,
      label: "usage",
      levelClass: thresholdBarClass(percent),
      valueText: "",
    }),
  );
  return wrap;
}

function netCell(latest) {
  const wrap = el("div", { class: "cp-systems-net-cell" });
  wrap.append(el("span", { class: "cp-tabular", text: `↓${formatBitrate(latest.net_rx_bps)}` }));
  wrap.append(el("span", { class: "cp-tabular", text: `↑${formatBitrate(latest.net_tx_bps)}` }));
  return wrap;
}

function egressCell(egress, direction) {
  const isOut = direction === "out";
  const bytes = isOut ? egress.tx_bytes : egress.rx_bytes;
  const limitBytes = isOut ? egress.limit_bytes : egress.rx_limit_bytes;
  const level = isOut ? egress.level : egress.rx_level;
  const hubOverride = (isOut ? egress.limit_source : egress.rx_limit_source) === "hub";

  const wrap = el("div", { class: "cp-systems-egress-cell" });
  const head = el("span", { class: "cp-systems-egress-head" });
  head.append(el("span", { class: "cp-tabular", text: formatBytes(bytes) }));
  if (hubOverride) head.append(hubOverrideBadge());
  wrap.append(head);
  if (limitBytes === 0) {
    wrap.append(el("span", { class: "cp-muted-small", text: "No limit" }));
  } else {
    wrap.append(
      progressBar({
        value: bytes,
        max: limitBytes,
        label: `${isOut ? "Outbound" : "Inbound"} usage`,
        levelClass: levelClassForBar(level),
        valueText: "",
      }),
    );
  }
  return wrap;
}

function agentCell(row) {
  const wrap = el("div", { class: "cp-systems-agent-cell" });
  const level = agentDotLevel(row.update);
  const dot = el("span", {
    class: `cp-dot ${level === "warning" ? "cp-dot-warning" : "cp-dot-up"}`,
    attrs: { "aria-hidden": "true" },
  });
  wrap.append(dot);
  const text = el("span", {
    class: "cp-muted-small",
    text: agentVersionText(row.host.agent_version),
  });
  if (level === "warning") {
    const info = hostUpdateBadgeText(row.update);
    if (info) {
      text.setAttribute("title", `${info.label}${row.update?.command ? ` — ${row.update.command}` : ""}`);
      text.setAttribute("tabindex", "0");
    }
  }
  wrap.append(text);
  return wrap;
}

function dashCell() {
  return el("span", { class: "cp-muted-small", text: "–" });
}

// ---------------------------------------------------------------------------
// Grid view
// ---------------------------------------------------------------------------

function buildGridView(rows, state) {
  const grid = el("div", { class: "cp-host-grid" });
  for (const row of rows) {
    grid.append(hostGridCard(row, state.nowMs));
  }
  return grid;
}

function hostGridCard(row, nowMs) {
  const isUp = row.status === "up";
  const card = /** @type {HTMLAnchorElement} */ (
    el("a", {
      class: `cp-card cp-host-card ${isUp ? "" : "cp-host-card-offline"}`.trim(),
      attrs: { href: `#/host/${encodeURIComponent(row.host.id)}` },
    })
  );

  const header = el("div", { class: "cp-host-card-header" });
  const nameRow = el("div", { class: "cp-host-card-header-name-row" });
  nameRow.append(statusDot(row.status), el("h3", { class: "cp-host-card-name", text: row.host.hostname }));
  header.append(nameRow);
  header.append(providerBadge(row.host.provider));
  const updateInfo = hostUpdateBadgeText(row.update);
  if (updateInfo) {
    header.append(
      el("span", { class: "cp-badge cp-badge-update", text: updateInfo.text, attrs: { title: updateInfo.label, "aria-label": updateInfo.label } }),
    );
  }
  card.append(header);

  card.append(
    el("p", {
      class: "cp-host-card-meta",
      text: `${row.host.os || "unknown"} / ${row.host.arch || "unknown"} · ${agentVersionText(row.host.agent_version)}`,
    }),
  );

  if (!isUp) {
    card.append(el("p", { class: "cp-host-card-offline-note", text: `Offline since ${formatRelativeTimeFromUnixSeconds(row.last_seen, nowMs)}` }));
  }

  if (row.latest) {
    card.append(gridMetricRow("CPU", row.latest.cpu_percent));
    card.append(gridMetricRow("Memory", row.latest.mem_used_percent, `${formatBytes(row.latest.mem_used)}/${formatBytes(row.latest.mem_total)}`));
    card.append(gridMetricRow("Disk", row.latest.disk_used_percent, `${formatBytes(row.latest.disk_used)}/${formatBytes(row.latest.disk_total)}`));

    const netRow = el("div", { class: "cp-host-card-net" });
    netRow.append(
      el("span", { text: `↓ ${formatBitrate(row.latest.net_rx_bps)}` }),
      el("span", { text: `↑ ${formatBitrate(row.latest.net_tx_bps)}` }),
      el("span", { text: `load ${formatLoad(row.latest.load1)}` }),
    );
    card.append(netRow);
    card.append(
      el("p", {
        class: "cp-host-card-uptime",
        text: `Uptime ${formatDuration(row.latest.uptime_seconds)} · Last seen ${formatRelativeTimeFromUnixSeconds(row.last_seen, nowMs)}`,
      }),
    );
  } else {
    card.append(el("p", { class: "cp-host-card-meta", text: "No samples yet." }));
  }

  const egressWrap = el("div", { class: "cp-egress-mini" });
  egressWrap.append(gridEgressRow("↑ Outbound", row.egress, "out"));
  egressWrap.append(gridEgressRow("↓ Inbound", row.egress, "in"));
  card.append(egressWrap);

  return card;
}

function gridMetricRow(label, percent, detail) {
  const row = el("div", { class: "cp-metric-row" });
  const head = el("div", { class: "cp-metric-head" });
  head.append(
    el("span", { class: "cp-metric-label", text: label }),
    el("span", { class: "cp-metric-value", text: detail ? `${formatPercent(percent)} · ${detail}` : formatPercent(percent) }),
  );
  row.append(head);
  row.append(progressBar({ value: percent, max: 100, label: `${label} usage`, levelClass: thresholdBarClass(percent), valueText: "" }));
  return row;
}

function gridEgressRow(label, egress, direction) {
  const isOut = direction === "out";
  const bytes = isOut ? egress.tx_bytes : egress.rx_bytes;
  const limitBytes = isOut ? egress.limit_bytes : egress.rx_limit_bytes;
  const level = isOut ? egress.level : egress.rx_level;
  const hubOverride = (isOut ? egress.limit_source : egress.rx_limit_source) === "hub";

  const wrap = el("div", { class: "cp-egress-direction" });
  const head = el("div", { class: "cp-metric-head" });
  const labelGroup = el("span", { class: "cp-egress-direction-label-group" });
  labelGroup.append(el("span", { class: "cp-metric-label", text: label }));
  if (hubOverride) labelGroup.append(hubOverrideBadge());
  head.append(labelGroup, egressLevelChip(level));
  wrap.append(head);
  if (limitBytes === 0) {
    wrap.append(el("p", { class: "cp-egress-unlimited", text: `No limit · ${formatBytes(bytes)} used` }));
    return wrap;
  }
  wrap.append(progressBar({ value: bytes, max: limitBytes, label: `${label} usage`, levelClass: levelClassForBar(level), valueText: `${formatBytes(bytes)} / ${formatBytes(limitBytes)}` }));
  return wrap;
}

// ---------------------------------------------------------------------------
// Mobile card list (< md, shown alongside the table via CSS visibility)
// ---------------------------------------------------------------------------

function buildMobileCardList(rows, state) {
  const list = el("div", { class: "cp-systems-mobile-list" });
  for (const row of rows) {
    list.append(mobileCard(row, state.nowMs));
  }
  return list;
}

function mobileCard(row, nowMs) {
  const isUp = row.status === "up";
  const card = /** @type {HTMLAnchorElement} */ (
    el("a", {
      class: `cp-systems-mobile-card ${isUp ? "" : "cp-host-card-offline"}`.trim(),
      attrs: { href: `#/host/${encodeURIComponent(row.host.id)}` },
    })
  );
  const head = el("div", { class: "cp-host-card-header" });
  const nameRow = el("div", { class: "cp-host-card-header-name-row" });
  nameRow.append(statusDot(row.status), el("h3", { class: "cp-host-card-name", text: row.host.hostname }));
  head.append(nameRow, providerBadge(row.host.provider));
  card.append(head);
  if (row.latest) {
    const metrics = el("div", { class: "cp-systems-mobile-metrics" });
    metrics.append(
      el("span", { text: `CPU ${formatPercent(row.latest.cpu_percent)}` }),
      el("span", { text: `Mem ${formatPercent(row.latest.mem_used_percent)}` }),
      el("span", { text: `Disk ${formatPercent(row.latest.disk_used_percent)}` }),
    );
    card.append(metrics);
  }
  card.append(el("p", { class: "cp-host-card-uptime", text: `Last seen ${formatRelativeTimeFromUnixSeconds(row.last_seen, nowMs)}` }));
  return card;
}

function describeError(err) {
  if (err instanceof ApiError) return `API error: ${err.message}`;
  return "Unable to reach the hub API. Showing last known data.";
}
