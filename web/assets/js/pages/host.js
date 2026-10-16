// host.js — host detail page (#/host/<id>): header card with badge row
// (status/hostname/platform/uptime/kernel/CPU/agent version/provider),
// time-range select + layout toggle, synced uPlot area charts with
// tooltips (destroy+recreate on theme change), disks table, monthly
// traffic card, and agent update card. Redesigned per SPEC-v0.4 §4 on
// top of the v0.3.x data layer (zero feature loss).
import { getHost, getHostMetrics, getHostInventory, getAlertEvents, ApiError } from "../core/api.js";
import { formatDisplayAmount } from "../core/currency.js";
import { loadDisplayCurrency } from "../core/display-currency.js";
import { el, clearChildren, emptyState, errorBanner, agentUpdatePanel } from "../ui/components.js";
import { selectField } from "../ui/select.js";
import { icon } from "../ui/icons.js";
import { formatBytes, formatBitrate, formatRelativeTimeFromUnixSeconds, formatLoad } from "../core/format.js";
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
import { t } from "../core/i18n.js";
import { localizeApiError } from "../core/api.js";
import { buildDetailIdentity, buildDetailResources, detailTrafficDirection, detailReading, isMeasurement } from "./hostdetail.js";
import { buildAlertTimeline } from "./alerttimeline.js";

const RANGE_OPTIONS = [
  { value: "1h", get label() { return t("host.range_1h"); } },
  { value: "6h", get label() { return t("host.range_6h"); } },
  { value: "24h", get label() { return t("host.range_24h"); } },
  { value: "7d", get label() { return t("host.range_7d"); } },
  { value: "30d", get label() { return t("host.range_30d"); } },
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
    loadedRange: null,
    summaryLoaded: false,
    activityEvents: [],
    activityLoaded: false,
    activityError: "",
    activityRequest: 0,
  };
  let metricsController = null;
  let metricsRequest = 0;
  controller.signal.addEventListener("abort", () => metricsController?.abort(), { once: true });

  clearChildren(container);
  const breadcrumb = el("nav", { class: "cp-detail-breadcrumb", attrs: { "aria-label": t("host.breadcrumb_label") } });
  breadcrumb.append(el("a", { attrs: { href: "#/" }, text: t("nav.overview") }));
  const breadcrumbCurrent = el("span", { class: "cp-detail-breadcrumb-current", attrs: { "aria-current": "page" } });
  breadcrumb.append(el("span", { class: "cp-detail-breadcrumb-separator", attrs: { "aria-hidden": "true" }, text: "/" }), breadcrumbCurrent);
  container.append(breadcrumb);

  const bannerHost = el("div");
  const headerHost = el("div", { class: "cp-card cp-host-detail-header" });
  container.append(bannerHost, headerHost);
  headerHost.append(el("p", { class: "cp-muted", attrs: { role: "status" }, text: t("host.detail_loading") }));

  const chartsHost = el("div", { class: `cp-charts-grid cp-charts-grid-cols-${state.layout}` });
  const disksHost = el("div", { class: "cp-disks-host" });
  const egressHost = el("div", { class: "cp-egress-host" });
  const inventoryHost = el("div", { class: "cp-inventory-host" });
  const agentUpdateHost = el("div", { class: "cp-agent-update-host cp-detail-agent-target", attrs: { id: "cp-detail-agent-update" } });
  const activityHost = el("div", { class: "cp-detail-activity-host" });
  const costHost = el("div", { class: "cp-host-cost-host" });
  const historySection = el("section", { class: "cp-detail-history", attrs: { "aria-labelledby": "cp-detail-history-heading" } });
  const chartNotice = el("p", { class: "cp-muted-small cp-detail-history-notice", attrs: { role: "status" } });
  const chartRange = el("p", { class: "cp-muted-small cp-detail-history-range", attrs: { id: "cp-detail-loaded-range" } });
  historySection.append(el("h2", { class: "cp-detail-section-title", attrs: { id: "cp-detail-history-heading" }, text: t("host.detail_history") }), chartRange, chartNotice, chartsHost);
  const inventoryNotice = el("div", { class: "cp-detail-inventory-notice" });
  container.append(agentUpdateHost, activityHost, historySection, egressHost, disksHost, inventoryNotice, inventoryHost, costHost);
  chartsHost.append(el("p", { class: "cp-muted", attrs: { role: "status" }, text: t("host.detail_loading_charts") }));

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
      const focusedControl = headerHost.contains(document.activeElement) ? document.activeElement.id : null;
      renderHostDetailHeader(headerHost, summary, Date.now(), { onRangeChange: handleRangeChange, onLayoutChange: handleLayoutChange, range: state.range, layout: state.layout });
      breadcrumbCurrent.textContent = summary.host.hostname || summary.host.id;
      state.summaryLoaded = true;
      if (focusedControl) Array.from(headerHost.querySelectorAll("[id]")).find((node) => node.id === focusedControl)?.focus({ preventScroll: true });
      renderHostEgressCard(egressHost, hostID, summary.egress);
      renderAgentUpdatePanel(agentUpdateHost, summary.update, announce, copyToClipboard);
      renderHostCostCard(costHost, summary.cost, summary.cost ? await loadDisplayCurrency(controller.signal) : null);
      renderDisksTable(disksHost, summary.latest?.disks);
      loadInventory();
      loadActivity();
      return true;
    } catch (err) {
      if (err?.name === "AbortError") return false;
      if (err instanceof ApiError && err.status === 404) {
        state.notFound = true;
        clearChildren(container);
        container.append(
          emptyState({ title: t("host.not_found_title"), message: t("host.not_found_message", { hostID }), iconName: "circleX" }),
        );
        container.append(el("a", { class: "cp-back-link", attrs: { href: "#/" }, text: t("host.back_to_overview") }));
        return false;
      }
      showBanner(describeError(err));
      if (!state.summaryLoaded) {
        clearChildren(headerHost);
        headerHost.append(el("p", { class: "cp-muted", text: t("host.detail_unavailable") }));
      }
      return true;
    }
  }

  async function loadActivity() {
    const request = ++state.activityRequest;
    // Retain the current card during background refreshes: rebuilding it would
    // collapse the timeline and repeatedly announce a loading status.
    state.activityError = "";
    if (!state.activityLoaded) renderHostActivity(activityHost, { loading: true, events: state.activityEvents, error: "" });
    try {
      const response = await getAlertEvents({ host: hostID, limit: 10 }, controller.signal);
      if (controller.signal.aborted || request !== state.activityRequest) return;
      state.activityEvents = response.events || [];
      state.activityLoaded = true;
      state.activityError = "";
      renderHostActivity(activityHost, { loading: false, events: state.activityEvents, error: "" });
    } catch (err) {
      if (err?.name === "AbortError" || controller.signal.aborted || request !== state.activityRequest) return;
      state.activityError = describeError(err);
      // Keep last-known events visible beneath the refresh error.
      renderHostActivity(activityHost, { loading: false, events: state.activityEvents, error: state.activityError });
    }
  }

  async function loadInventory() {
    try {
      const inventory = await getHostInventory(hostID, controller.signal);
      clearChildren(inventoryNotice);
      renderServicesCard(inventoryHost, inventory);
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (err instanceof ApiError && err.status === 404) {
        clearChildren(inventoryHost);
        clearChildren(inventoryNotice);
        renderInventoryState(inventoryHost, t("host.detail_inventory_pending"));
        return;
      }
      // Preserve last-known inventory, but disclose a failed refresh.
      clearChildren(inventoryNotice);
      inventoryNotice.append(errorBanner(t("host.detail_inventory_error")));
      if (!inventoryHost.firstChild) renderInventoryState(inventoryHost, t("host.detail_inventory_unavailable"));
    }
  }

  async function loadMetrics() {
    if (controller.signal.aborted || state.notFound) return;
    metricsController?.abort();
    const request = ++metricsRequest;
    const current = new AbortController();
    metricsController = current;
    const requestedRange = state.range;
    const rangeLabel = (range) => RANGE_OPTIONS.find((option) => option.value === range)?.label || range;
    const ranges = { requested: rangeLabel(requestedRange), loaded: rangeLabel(state.loadedRange) };
    chartsHost.setAttribute("aria-busy", "true");
    chartNotice.textContent = state.lastSeries && state.loadedRange !== requestedRange
      ? t("host.detail_history_loading_retained", ranges) : t("host.detail_loading_charts");
    try {
      const series = await getHostMetrics(hostID, requestedRange, current.signal);
      // A -> B -> A may share a range string, but never a request generation.
      if (controller.signal.aborted || current.signal.aborted || request !== metricsRequest || requestedRange !== state.range) return;
      state.lastSeries = series;
      state.loadedRange = requestedRange;
      state.charts.forEach((c) => c.destroy());
      state.charts = renderHostCharts(chartsHost, series);
      chartRange.textContent = t("host.detail_history_loaded_range", { range: rangeLabel(state.loadedRange) });
      chartNotice.textContent = "";
    } catch (err) {
      if (err?.name === "AbortError" || controller.signal.aborted || current.signal.aborted || request !== metricsRequest || requestedRange !== state.range) return;
      showBanner(describeError(err));
      chartNotice.textContent = state.lastSeries ? t("host.detail_history_error_retained", ranges) : t("host.detail_history_error");
      if (!state.lastSeries) clearChildren(chartsHost);
    } finally {
      if (request === metricsRequest && !controller.signal.aborted) chartsHost.setAttribute("aria-busy", "false");
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

export function renderHostDetailHeader(host, summary, nowMs, { onRangeChange, onLayoutChange, range, layout }) {
  clearChildren(host);
  const { top, meta } = buildDetailIdentity(summary, nowMs);

  const rightControls = el("div", { class: "cp-host-detail-controls" });
  const { node: rangeNode, select: rangeSelect } = selectField({
    id: "cp-host-range",
    label: t("host.time_range"),
    options: RANGE_OPTIONS,
    value: range,
  });
  rangeSelect.addEventListener("change", () => onRangeChange(rangeSelect.value));
  rightControls.append(rangeNode);
  rightControls.append(el("a", { class: "cp-btn cp-btn-secondary", attrs: { href: `#/compare?hosts=${encodeURIComponent(summary.host.id)}` }, text: t("compare.open_from_host") }));
  rightControls.append(buildLayoutToggle(layout, onLayoutChange));
  top.append(rightControls);
  host.append(top);
  host.append(meta, buildDetailResources(summary, nowMs));
}

function buildLayoutToggle(current, onChange) {
  const group = el("div", { class: "cp-segmented cp-layout-toggle", attrs: { role: "group", "aria-label": t("host.chart_layout") } });
  const oneBtn = el("button", {
    class: "cp-btn cp-segmented-btn cp-btn-icon",
    attrs: { id: "cp-detail-layout-one", type: "button", "aria-pressed": current === "1" ? "true" : "false", "aria-label": t("host.one_column"), title: t("host.one_column") },
  });
  oneBtn.append(icon("rows3"));
  const twoBtn = el("button", {
    class: "cp-btn cp-segmented-btn cp-btn-icon",
    attrs: { id: "cp-detail-layout-two", type: "button", "aria-pressed": current === "2" ? "true" : "false", "aria-label": t("host.two_columns"), title: t("host.two_columns") },
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

export function renderHostEgressCard(host, hostID, egress) {
  clearChildren(host);
  const card = el("section", { class: "cp-card cp-detail-traffic-card", attrs: { "aria-labelledby": "cp-detail-traffic-heading" } });
  const headRow = el("div", { class: "cp-detail-section-head" });
  headRow.append(el("h2", { class: "cp-card-title", attrs: { id: "cp-detail-traffic-heading" }, text: t("host.monthly_traffic") }));
  headRow.append(
    el("a", {
      class: "cp-back-link",
      attrs: { href: `#/settings/traffic?host=${encodeURIComponent(hostID)}` },
      text: t("host.edit_limits"),
    }),
  );
  const grid = el("div", { class: "cp-detail-traffic-grid" });
  grid.append(detailTrafficDirection(egress, "out"), detailTrafficDirection(egress, "in"));
  card.append(headRow, grid);
  host.append(card);
}

function renderAgentUpdatePanel(host, update, announce, copyToClipboard) {
  clearChildren(host);
  const built = agentUpdatePanel(update);
  if (!built) return;
  built.node.classList.add("cp-detail-agent-panel");
  if (built.copyBtn) {
    built.copyBtn.className = "cp-btn cp-btn-secondary cp-btn-icon cp-detail-agent-copy";
    built.copyBtn.setAttribute("aria-label", t("common.copy"));
    built.copyBtn.setAttribute("title", t("common.copy"));
    clearChildren(built.copyBtn);
    built.copyBtn.append(icon("copy"));
    built.copyBtn.addEventListener("click", async () => {
      await copyToClipboard(built.command, undefined);
      announce?.(t("host.agent_update_command_copied"));
    });
  }
  host.append(built.node);
}

export function renderHostActivity(host, state) {
  clearChildren(host);
  const card = el("section", { class: "cp-card cp-detail-activity-card", attrs: { "aria-labelledby": "cp-detail-activity-heading", "aria-busy": state.loading ? "true" : "false" } });
  const head = el("div", { class: "cp-detail-section-head" });
  head.append(el("div", { class: "cp-activity-title-group", children: [el("h2", { class: "cp-card-title", attrs: { id: "cp-detail-activity-heading" }, text: t("alerts_page.activity_title") }), el("p", { class: "cp-muted-small", text: t("host.activity_description") })] }));
  head.append(el("a", { class: "cp-back-link", attrs: { href: "#/alerts" }, text: t("nav.view_all") }));
  card.append(head);
  if (state.loading) card.append(el("p", { class: "cp-activity-notice", attrs: { role: "status" }, text: t("alerts_page.activity_loading") }));
  if (state.error) card.append(errorBanner(state.error));
  if (!state.loading && !state.error && state.events.length === 0) card.append(emptyState({ title: t("host.activity_empty_title"), message: t("host.activity_empty_message"), iconName: "activity" }));
  if (state.events.length > 0) card.append(buildAlertTimeline(state.events, Math.floor(Date.now() / 1000), { hideHost: true }));
  host.append(card);
}

/**
 * renderHostCostCard builds the host detail "Cost" card (SPEC-v0.6 §1):
 * the matched cloud-billing MTD/forecast plus the estimated network
 * egress cost, rendered in the hub's display currency (SPEC-v0.6 §3).
 * Renders nothing when the summary has no `cost` field (billing disabled).
 * @param {HTMLElement} host
 * @param {Object|null|undefined} cost a models.HostCost JSON object
 * @param {Object|null|undefined} displayCurrency models.DisplayCurrencySettings
 */
function renderHostCostCard(host, cost, displayCurrency) {
  clearChildren(host);
  if (!cost) return;
  const money = (v) => formatDisplayAmount(Number(v || 0), displayCurrency, { taxNote: false });

  const card = el("div", { class: "cp-card cp-host-cost-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("host.cost_title") }));

  if (cost.provider) {
    if (cost.matched) {
      card.append(el("p", { class: "cp-tabular", text: t("host.cloud_billing_matched", { provider: cost.provider.toUpperCase(), mtd: money(cost.cloud_mtd), forecast: money(cost.cloud_forecast) }) }));
    } else {
      card.append(el("p", { class: "cp-muted-small", text: t("host.cloud_billing_unmatched", { provider: cost.provider.toUpperCase() }) }));
    }
  } else {
    card.append(el("p", { class: "cp-muted-small", text: t("host.no_cloud_billing_match") }));
  }

  if (cost.network_estimate?.plan_id) {
    card.append(el("p", { class: "cp-tabular", text: t("host.network_estimate_line", { plan: cost.network_estimate.plan_name, mtd: money(cost.network_estimate.mtd), projected: money(cost.network_estimate.projected) }) }));
  } else {
    card.append(el("p", { class: "cp-muted-small", text: t("host.no_pricing_plan_assigned") }));
  }

  card.append(el("p", { class: "cp-muted-small cp-tabular", text: t("host.cost_total_line", { mtd: money(cost.total_mtd), forecast: money(cost.total_forecast) }) }));
  host.append(card);
}

/**
 * renderServicesCard builds the host detail "Services & ports" card
 * (SPEC-v0.5 §D): a Docker containers table (or an empty/permission/
 * error state with the exact fix command) followed by a listening
 * ports table.
 * @param {HTMLElement} host
 * @param {Object} inventory a models.Inventory JSON object
 */
export function renderServicesCard(host, inventory) {
  clearChildren(host);
  const card = el("section", { class: "cp-card cp-services-card cp-detail-services", attrs: { "aria-labelledby": "cp-detail-services-heading" } });
  const head = el("div", { class: "cp-detail-section-head" });
  head.append(el("h2", { class: "cp-card-title", attrs: { id: "cp-detail-services-heading" }, text: t("host.services_and_ports") }));
  if (isMeasurement(inventory.collected_at) && inventory.collected_at > 0) head.append(el("span", { class: "cp-muted-small", text: t("host.detail_inventory_age", { relative: formatRelativeTimeFromUnixSeconds(inventory.collected_at) }) }));
  card.append(head);
  if (inventory.docker?.status === "ok" && !(inventory.docker.containers || []).length && !(inventory.ports || []).length) {
    card.append(compactInventoryEmpty(t("host.no_services_or_ports_reported")));
    host.append(card);
    return;
  }
  card.append(renderDockerSection(inventory.docker || { status: "unknown" }));
  card.append(renderPortsSection(inventory.ports));

  host.append(card);
}

export function renderInventoryState(host, message) {
  clearChildren(host);
  const card = el("section", { class: "cp-card cp-detail-services", attrs: { "aria-labelledby": "cp-detail-services-heading" } });
  card.append(el("h2", { class: "cp-card-title", attrs: { id: "cp-detail-services-heading" }, text: t("host.services_and_ports") }), compactInventoryEmpty(message));
  host.append(card);
}

function renderDockerSection(docker) {
  const section = el("div", { class: "cp-services-section" });
  section.append(el("h3", { class: "cp-services-section-title", text: t("host.docker_containers") }));

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
    section.append(compactInventoryEmpty(t("host.no_containers_reported")));
    return section;
  }

  const tableWrap = el("div", { class: "cp-table-wrap", attrs: { tabindex: "0", role: "region", "aria-label": t("host.docker_containers") } });
  const table = el("table", { class: "cp-table cp-detail-container-table" });
  const thead = el("thead");
  thead.append(
    el("tr", { children: [t("host.col_name"), t("host.col_compose"), t("host.col_image"), t("host.col_state"), t("host.col_health"), t("host.col_ports"), t("host.col_status")].map((tx) => el("th", { text: tx })) }),
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
    portsWrap.append(el("span", { class: "cp-chip cp-chip-other cp-services-port-chip cp-tabular", text: formatPublishedPort(p) }));
  }
  if ((c.ports || []).length === 0) portsWrap.append(el("span", { class: "cp-muted-small", text: "—" }));
  portsCell.append(portsWrap);

  return el("tr", {
    children: [
      el("td", { text: c.name }),
      el("td", { text: compose || "—" }),
      el("td", { text: c.image }),
      el("td", { class: "cp-detail-short-cell", children: [el("span", { class: `cp-chip ${isRunning ? "cp-chip-ok" : "cp-chip-other"}`, text: c.state })] }),
      el("td", { class: "cp-detail-short-cell", text: health || "—" }),
      portsCell,
      el("td", { text: c.status }),
    ],
  });
}

function renderPortsSection(ports) {
  const section = el("div", { class: "cp-services-section" });
  section.append(el("h3", { class: "cp-services-section-title", text: t("host.listening_ports") }));

  if (!ports || ports.length === 0) {
    section.append(compactInventoryEmpty(t("host.no_listening_ports_reported")));
    return section;
  }

  const tableWrap = el("div", { class: "cp-table-wrap", attrs: { tabindex: "0", role: "region", "aria-label": t("host.listening_ports") } });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(el("tr", { children: [t("host.col_proto"), t("host.col_address"), t("host.col_process"), t("host.col_container")].map((tx) => el("th", { text: tx })) }));
  table.append(thead);
  const tbody = el("tbody");
  for (const p of sortPorts(ports)) {
    tbody.append(
      el("tr", {
        children: [
          el("td", { class: "cp-detail-short-cell", text: p.proto.toUpperCase() }),
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

export function renderDisksTable(host, disks) {
  clearChildren(host);
  const card = el("section", { class: "cp-card cp-detail-disks", attrs: { "aria-labelledby": "cp-detail-disks-heading" } });
  card.append(el("h2", { class: "cp-card-title", attrs: { id: "cp-detail-disks-heading" }, text: t("host.disks_title") }));
  if (!disks || disks.length === 0) {
    card.append(compactInventoryEmpty(t("host.no_disks_reported")));
    host.append(card);
    return;
  }
  const tableWrap = el("div", { class: "cp-table-wrap", attrs: { tabindex: "0", role: "region", "aria-label": t("host.disks_title") } });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(
    el("tr", {
      children: [t("host.col_mountpoint"), t("host.col_device"), t("host.col_fs"), t("host.col_used"), t("host.col_total"), t("host.col_used_pct")].map((tx) => el("th", { text: tx })),
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
          el("td", { class: "cp-tabular", text: detailReading(d.used_bytes, formatBytes) }),
          el("td", { class: "cp-tabular", text: detailReading(d.total_bytes, formatBytes) }),
          el("td", { class: "cp-tabular", text: detailReading(d.used_percent, (value) => `${value.toFixed(1)}%`) }),
        ],
      }),
    );
  }
  table.append(tbody);
  tableWrap.append(table);
  card.append(tableWrap);
  host.append(card);
}

function compactInventoryEmpty(message) {
  const empty = el("div", { class: "cp-detail-compact-empty" });
  empty.append(icon("circleAlert"), el("p", { class: "cp-muted-small", text: message }));
  return empty;
}

/**
 * renderHostCharts builds the full metric chart grid for a host's
 * Series and returns the created ChartHandle instances so the caller
 * can destroy them on navigation/refresh/theme change. Charts share a
 * synced cursor (uPlot's `cursor.sync`) so hovering one highlights the
 * same timestamp across all of them.
 */
export function renderHostCharts(host, series) {
  clearChildren(host);
  const charts = [];
  const syncKey = "cp-host-charts";
  const percentFormatter = (v) => `${v.toFixed(1)}%`;
  const bitrateFormatter = (v) => formatBitrate(v, 1);
  const loadFormatter = (v) => formatLoad(v);
  const specs = [
    {
      title: t("host.chart_cpu_title"),
      description: t("host.chart_cpu_description"),
      series: [{ label: t("host.chart_series_cpu"), color: SERIES_COLORS.cpu }],
      values: [series.cpu],
      summary: t("host.chart_cpu_summary", { value: latestReading(series.cpu, (value) => value.toFixed(1)) }),
      valueFormatter: percentFormatter,
      yAxisFormatter: percentFormatter,
    },
    {
      title: t("host.chart_memory_title"),
      description: t("host.chart_memory_description"),
      series: [{ label: t("host.chart_series_memory"), color: SERIES_COLORS.mem }],
      values: [series.mem],
      summary: t("host.chart_memory_summary", { value: latestReading(series.mem, (value) => value.toFixed(1)) }),
      valueFormatter: percentFormatter,
      yAxisFormatter: percentFormatter,
    },
    {
      title: t("host.chart_disk_title"),
      description: t("host.chart_disk_description"),
      series: [{ label: t("host.chart_series_disk"), color: SERIES_COLORS.disk }],
      values: [series.disk],
      summary: t("host.chart_disk_summary", { value: latestReading(series.disk, (value) => value.toFixed(1)) }),
      valueFormatter: percentFormatter,
      yAxisFormatter: percentFormatter,
    },
    {
      title: t("host.chart_bandwidth_title"),
      description: t("host.chart_bandwidth_description"),
      series: [
        { label: t("host.chart_series_rx"), color: SERIES_COLORS.netRx },
        { label: t("host.chart_series_tx"), color: SERIES_COLORS.netTx },
      ],
      values: [series.net_rx, series.net_tx],
      summary: t("host.chart_bandwidth_summary", { rx: latestReading(series.net_rx, formatBytes), tx: latestReading(series.net_tx, formatBytes) }),
      valueFormatter: bitrateFormatter,
      yAxisFormatter: bitrateFormatter,
    },
    {
      title: t("host.chart_disk_io_title"),
      description: t("host.chart_disk_io_description"),
      series: [
        { label: t("host.chart_series_read"), color: SERIES_COLORS.diskRead },
        { label: t("host.chart_series_write"), color: SERIES_COLORS.diskWrite },
      ],
      values: [series.disk_read, series.disk_write],
      summary: t("host.chart_disk_io_summary", { read: latestReading(series.disk_read, formatBytes), write: latestReading(series.disk_write, formatBytes) }),
      valueFormatter: bitrateFormatter,
      yAxisFormatter: bitrateFormatter,
    },
    {
      title: t("host.chart_load_title"),
      description: t("host.chart_load_description"),
      series: [{ label: t("host.chart_series_load1"), color: SERIES_COLORS.load1 }],
      values: [series.load1],
      summary: t("host.chart_load_summary", { value: latestReading(series.load1, formatLoad) }),
      valueFormatter: loadFormatter,
      yAxisFormatter: loadFormatter,
    },
  ];

  for (const [index, spec] of specs.entries()) {
    const headingID = `cp-detail-chart-${index}`;
    const card = el("section", { class: "cp-card cp-chart-card", attrs: { "aria-labelledby": headingID } });
    const titleRow = el("div", { class: "cp-chart-card-head" });
    titleRow.append(el("h2", { attrs: { id: headingID }, text: spec.title }));
    titleRow.append(el("p", { class: "cp-muted-small", text: spec.description }));
    card.append(titleRow);
    if (series.ts.length === 0 || !spec.values.some((values) => values?.some(isMeasurement))) {
      card.append(el("div", { class: "cp-detail-chart-empty", children: [icon("activity"), el("p", { text: t("host.no_data_for_range") })] }));
      host.append(card);
      continue;
    }
    const chartWrap = el("div", {
      class: "cp-chart-wrap",
      attrs: { role: "img", "aria-label": spec.summary },
    });
    card.append(chartWrap);
    card.append(el("p", { class: "cp-detail-chart-summary", text: spec.summary }));
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

function latestReading(arr, formatter) {
  // A missing latest point is unknown, not a measured zero or an older point.
  return detailReading(arr?.[arr.length - 1], formatter);
}

function describeError(err) {
  if (err instanceof ApiError) return t("overview.api_error_prefix", { message: localizeApiError(err) });
  return t("overview.hub_unreachable_showing_last_known");
}
