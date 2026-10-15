// compare.js — multi-host history comparison. Samples retain their original
// timestamps; missing observations remain gaps rather than inferred lines.
import { getHosts, getHostMetrics } from "../core/api.js";
import { t } from "../core/i18n.js";
import { formatBitrate, formatPercent, formatLoad } from "../core/format.js";
import { el, clearChildren, emptyState, errorBanner, providerBadge, skeletonLines, statusDot } from "../ui/components.js";
import { selectField } from "../ui/select.js";
import { createTimeSeriesChart, SERIES_COLORS } from "./charts.js";

export const COMPARE_RANGE_OPTIONS = [
  { value: "1h", get label() { return t("host.range_1h"); } },
  { value: "6h", get label() { return t("host.range_6h"); } },
  { value: "24h", get label() { return t("host.range_24h"); } },
  { value: "7d", get label() { return t("host.range_7d"); } },
  { value: "30d", get label() { return t("host.range_30d"); } },
];

const MAX_HOSTS = 4;
const isMeasurement = (value) => typeof value === "number" && Number.isFinite(value) && value >= 0;

/** Normalizes the hosts query, retaining an explicit list of unavailable IDs. */
export function parseCompareHosts(value, knownIDs = null) {
  const raw = String(value || "").split(",").map((id) => id.trim()).filter(Boolean);
  const unique = [...new Set(raw)];
  if (!knownIDs) return { ids: unique.slice(0, MAX_HOSTS), unknown: [] };
  const known = new Set(knownIDs);
  return { ids: unique.filter((id) => known.has(id)).slice(0, MAX_HOSTS), unknown: unique.filter((id) => !known.has(id)) };
}

/** Produces the canonical deep-link hash for a selection. */
export function serializeCompareHosts(ids) {
  const normalized = parseCompareHosts((ids || []).join(",")).ids;
  return `#/compare?hosts=${encodeURIComponent(normalized.join(","))}`;
}

function hostHash(id) {
  let hash = 0;
  for (const char of String(id)) hash = ((hash * 31) + char.charCodeAt(0)) >>> 0;
  return hash;
}

/** A host keeps the same chart color across every comparison metric. */
export function compareColorForHost(id) {
  const palette = [SERIES_COLORS.cpu, SERIES_COLORS.mem, SERIES_COLORS.disk, SERIES_COLORS.netRx, SERIES_COLORS.netTx, SERIES_COLORS.diskRead, SERIES_COLORS.diskWrite, SERIES_COLORS.load1];
  return palette[hostHash(id) % palette.length];
}

/** Assigns distinct token colors for the current selection, independent of
 * picker order. The same map is reused for every metric chart. */
export function assignCompareColors(ids) {
  const palette = [SERIES_COLORS.cpu, SERIES_COLORS.mem, SERIES_COLORS.disk, SERIES_COLORS.netRx, SERIES_COLORS.netTx, SERIES_COLORS.diskRead, SERIES_COLORS.diskWrite, SERIES_COLORS.load1];
  const assigned = new Map(), used = new Set();
  for (const id of [...new Set(ids)].sort()) {
    let index = palette.indexOf(compareColorForHost(id));
    while (used.has(index)) index = (index + 1) % palette.length;
    used.add(index); assigned.set(id, palette[index]);
  }
  return assigned;
}

/** Gives each distinct selection/range pair one history request lifecycle. */
export function compareMetricsKey(ids, range) {
  return `${range}:${ids.join(",")}`;
}

/** Auto refreshes preserve chart instances unless their requested history changed. */
export function shouldReloadCompareMetrics(currentKey, ids, range) {
  return currentKey !== compareMetricsKey(ids, range);
}

/** The history status is visible only for an active request with no data yet. */
export function isCompareHistoryLoading(histories, metricsInFlight) {
  return histories.size === 0 && metricsInFlight;
}

/** Seeds URL hosts once; later fleet refreshes only discard departed hosts. */
export function reconcileCompareSelection({ urlSelectionSeeded, initialHosts, knownIDs, selected, unknown }) {
  if (!urlSelectionSeeded) {
    const parsed = parseCompareHosts(initialHosts, knownIDs);
    return { selected: parsed.ids, unknown: parsed.unknown, urlSelectionSeeded: true };
  }
  const known = new Set(knownIDs);
  return { selected: selected.filter((id) => known.has(id)), unknown, urlSelectionSeeded: true };
}

/** Returns IDs tied for the greatest reported value in each metric. */
export function maxHostsByColumn(rows) {
  const result = {};
  for (const column of ["cpu", "mem", "disk", "net"]) {
    const values = rows.filter((row) => isMeasurement(row[column]));
    const max = values.length ? Math.max(...values.map((row) => row[column])) : null;
    result[column] = new Set(max === null ? [] : values.filter((row) => row[column] === max).map((row) => row.id));
  }
  return result;
}

/** Guards responses from an earlier range or selection request. */
export function isCurrentCompareRequest(request, currentRequest, requestHosts, currentHosts) {
  return request === currentRequest && requestHosts.length === currentHosts.length && requestHosts.every((id, index) => id === currentHosts[index]);
}

/** Aligns exact observations onto a shared timestamp axis; never interpolates. */
export function alignMetricSeries(histories, ids, metric) {
  const timestamps = new Set();
  for (const id of ids) for (const ts of histories.get(id)?.ts || []) if (Number.isFinite(ts)) timestamps.add(ts);
  const ts = [...timestamps].sort((a, b) => a - b);
  const values = ids.map((id) => {
    const history = histories.get(id) || {};
    const source = history[metric] || [];
    const byTime = new Map((history.ts || []).map((time, index) => [time, source[index]]));
    return ts.map((time) => isMeasurement(byTime.get(time)) ? byTime.get(time) : null);
  });
  return { ts, values };
}

function combinedNetwork(history) {
  const rx = history.net_rx || [], tx = history.net_tx || [];
  return (history.ts || []).map((_time, index) => isMeasurement(rx[index]) && isMeasurement(tx[index]) ? rx[index] + tx[index] : null);
}

function comparisonRows(hosts) {
  return hosts.map((summary) => {
    const latest = summary.latest || {};
    const rx = latest.net_rx_bps, tx = latest.net_tx_bps;
    return {
      id: summary.host.id,
      summary,
      cpu: isMeasurement(latest.cpu_percent) ? latest.cpu_percent : null,
      mem: isMeasurement(latest.mem_used_percent) ? latest.mem_used_percent : null,
      disk: isMeasurement(latest.disk_used_percent) ? latest.disk_used_percent : null,
      net: isMeasurement(rx) && isMeasurement(tx) ? rx + tx : null,
    };
  });
}

function statusLabel(status) {
  if (status === "up") return t("host.detail_online");
  if (status === "down") return t("host.detail_offline");
  return t("host.detail_unknown");
}

function reading(value, formatter) {
  return isMeasurement(value) ? formatter(value) : t("compare.unknown_value");
}

/** Mounts the #/compare page. */
export function mountComparePage(container, { initialHosts = "", announce } = {}) {
  const controller = new AbortController();
  const state = { fleet: [], selected: [], unknown: [], range: "1h", histories: new Map(), errors: new Map(), charts: [], request: 0, filter: "", fleetError: false, historyKey: null, metricsInFlight: false, urlSelectionSeeded: false };
  let metricsController = null;
  controller.signal.addEventListener("abort", () => metricsController?.abort(), { once: true });

  clearChildren(container);
  const heading = el("div", { class: "cp-compare-heading" });
  heading.append(el("h1", { class: "cp-page-title", text: t("compare.title") }), el("p", { class: "cp-page-subtitle", text: t("compare.description") }));
  const picker = el("section", { class: "cp-compare-picker", attrs: { "aria-labelledby": "cp-compare-picker-title" } });
  const pickerTitle = el("h2", { class: "cp-compare-section-title", attrs: { id: "cp-compare-picker-title" }, text: t("compare.picker_title") });
  const pickerControls = el("div", { class: "cp-compare-picker-controls" });
  const search = /** @type {HTMLInputElement} */ (el("input", { class: "cp-input", attrs: { type: "search", placeholder: t("compare.search_placeholder"), "aria-label": t("compare.search_label") } }));
  const selectedNote = el("p", { class: "cp-muted-small", attrs: { role: "status" } });
  pickerControls.append(search, selectedNote);
  const list = el("div", { class: "cp-compare-host-list" });
  picker.append(pickerTitle, pickerControls, list);
  const notices = el("div", { class: "cp-compare-notices" });
  const summaryHost = el("section", { class: "cp-compare-summary", attrs: { "aria-labelledby": "cp-compare-summary-title" } });
  const chartsHost = el("section", { class: "cp-compare-charts", attrs: { "aria-labelledby": "cp-compare-charts-title" } });
  container.append(heading, picker, notices, summaryHost, chartsHost);

  const { node: rangeNode, select: rangeSelect } = selectField({ id: "cp-compare-range", label: t("host.time_range"), options: COMPARE_RANGE_OPTIONS, value: state.range });
  pickerControls.append(rangeNode);

  search.addEventListener("input", () => { state.filter = search.value; renderPicker(); });
  rangeSelect.addEventListener("change", () => { state.range = rangeSelect.value; loadMetrics(); });
  const onThemeChange = () => { if (state.histories.size) renderCharts(); };
  window.addEventListener("cp-theme-change", onThemeChange);

  function selectedSummaries() {
    const byID = new Map(state.fleet.map((item) => [item.host.id, item]));
    return state.selected.map((id) => byID.get(id)).filter(Boolean);
  }

  function replaceURL() {
    const hash = serializeCompareHosts(state.selected);
    if (globalThis.location?.hash === hash) return;
    globalThis.history?.replaceState(null, "", hash);
  }

  function choose(ids) {
    state.selected = parseCompareHosts(ids.join(","), state.fleet.map((item) => item.host.id)).ids;
    state.unknown = [];
    state.histories.clear(); state.errors.clear(); state.historyKey = null;
    replaceURL(); renderAll(); loadMetrics();
  }

  function renderPicker() {
    const active = document.activeElement;
    const focusedHostID = active instanceof HTMLInputElement && active.type === "checkbox" && list.contains(active) ? active.value : null;
    clearChildren(list);
    const term = state.filter.trim().toLocaleLowerCase();
    const candidates = state.fleet.filter((item) => !term || `${item.host.hostname || ""} ${item.host.id}`.toLocaleLowerCase().includes(term));
    selectedNote.textContent = t("compare.selected_up_to", { count: state.selected.length, max: MAX_HOSTS });
    if (!candidates.length) {
      if (term) list.append(el("p", { class: "cp-muted-small", text: t("compare.no_search_results") }));
      return;
    }
    for (const item of candidates) {
      const id = item.host.id, checked = state.selected.includes(id);
      const hostname = item.host.hostname || id;
      const hasSeparateID = id !== hostname;
      const label = el("label", { class: "cp-compare-host-option" });
      const input = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox", value: id, "aria-label": `${hostname} (${statusLabel(item.status)})` } }));
      input.checked = checked;
      input.disabled = !checked && state.selected.length >= MAX_HOSTS;
      input.addEventListener("change", () => choose(input.checked ? [...state.selected, id] : state.selected.filter((value) => value !== id)));
      const copy = el("span", { class: "cp-compare-host-copy" });
      copy.append(el("span", { class: "cp-compare-host-name", text: hostname }));
      if (hasSeparateID) copy.append(el("span", { class: "cp-compare-host-id cp-muted-small", text: id }));
      const meta = el("span", { class: "cp-compare-host-meta" });
      meta.append(statusDot(item.status), providerBadge(item.host.provider));
      label.append(input, copy, meta);
      list.append(label);
    }
    if (focusedHostID) [...list.querySelectorAll('input[type="checkbox"]')].find((input) => input.value === focusedHostID)?.focus();
  }

  function renderNotices() {
    clearChildren(notices);
    if (state.fleetError) { notices.append(errorBanner(t("compare.hosts_error"))); return; }
    if (state.fleet.length < 2) {
      notices.append(emptyState({ title: t("compare.fleet_small_title"), message: t("compare.fleet_small_message"), iconName: "server", action: { label: t("nav.add_agent"), href: "#/", iconName: "plus" } }));
      return;
    }
    if (state.unknown.length) notices.append(errorBanner(t("compare.unknown_hosts", { ids: state.unknown.join(", ") })));
    if (state.selected.length === 1) notices.append(el("p", { class: "cp-muted", text: t("compare.pick_another") }));
    if (state.selected.length === 0) notices.append(el("p", { class: "cp-muted", text: t("compare.pick_hosts") }));
    for (const [id] of state.errors) {
      const host = state.fleet.find((item) => item.host.id === id);
      notices.append(errorBanner(t("compare.history_error", { host: host?.host.hostname || id })));
    }
  }

  function renderSummary() {
    clearChildren(summaryHost);
    const hosts = selectedSummaries();
    if (hosts.length < 2) return;
    const rows = comparisonRows(hosts), maxima = maxHostsByColumn(rows);
    summaryHost.append(el("h2", { class: "cp-compare-section-title", attrs: { id: "cp-compare-summary-title" }, text: t("compare.summary_title") }));
    const wrap = el("div", { class: "cp-compare-table-wrap", attrs: { tabindex: "0", role: "region", "aria-label": t("compare.summary_title") } });
    const table = el("table", { class: "cp-table cp-compare-table" });
    const headers = [
      [t("compare.host_column"), ""],
      [t("compare.cpu_column"), "cp-compare-numeric"],
      [t("compare.memory_column"), "cp-compare-numeric"],
      [t("compare.disk_column"), "cp-compare-numeric"],
      [t("compare.network_column"), "cp-compare-numeric"],
      [t("compare.status_column"), ""],
      [t("compare.provider_column"), ""],
    ];
    table.append(el("thead", { children: [el("tr", { children: headers.map(([text, className]) => el("th", { class: className, text })) })] }));
    const body = el("tbody");
    for (const row of rows) {
      const tr = el("tr");
      tr.append(el("th", { attrs: { scope: "row" }, text: row.summary.host.hostname || row.id }));
      for (const [column, formatter] of [["cpu", formatPercent], ["mem", formatPercent], ["disk", formatPercent], ["net", formatBitrate]]) {
        const cell = el("td", { class: `cp-compare-numeric${maxima[column].has(row.id) ? " cp-compare-max" : ""}`, text: reading(row[column], formatter) });
        tr.append(cell);
      }
      tr.append(el("td", { text: statusLabel(row.summary.status) }));
      const provider = el("td"); provider.append(providerBadge(row.summary.host.provider)); tr.append(provider);
      body.append(tr);
    }
    table.append(body); wrap.append(table); summaryHost.append(wrap);
  }

  function renderCharts() {
    state.charts.forEach((chart) => chart.destroy()); state.charts = [];
    clearChildren(chartsHost);
    const hosts = selectedSummaries();
    if (hosts.length < 2) return;
    chartsHost.append(el("h2", { class: "cp-compare-section-title", attrs: { id: "cp-compare-charts-title" }, text: t("compare.charts_title") }));
    if (isCompareHistoryLoading(state.histories, state.metricsInFlight)) {
      chartsHost.append(skeletonLines(5));
      return;
    }
    const grid = el("div", { class: "cp-compare-chart-grid" }); chartsHost.append(grid);
    const colors = assignCompareColors(hosts.map((item) => item.host.id));
    const chartHosts = hosts.filter((item) => state.histories.has(item.host.id));
    const ids = chartHosts.map((item) => item.host.id);
    const specs = [
      ["cpu", t("host.chart_cpu_title"), t("compare.percent_unit"), formatPercent],
      ["mem", t("host.chart_memory_title"), t("compare.percent_unit"), formatPercent],
      ["disk", t("host.chart_disk_title"), t("compare.percent_unit"), formatPercent],
      ["network", t("host.chart_bandwidth_title"), t("compare.network_unit"), formatBitrate],
      ["load1", t("host.chart_load_title"), t("compare.load_unit"), formatLoad],
    ];
    for (const [metric, title, unit, formatter] of specs) {
      const histories = metric === "network" ? new Map([...state.histories].map(([id, history]) => [id, { ...history, network: combinedNetwork(history) }])) : state.histories;
      const aligned = alignMetricSeries(histories, ids, metric);
      const card = el("section", { class: "cp-card cp-chart-card cp-compare-chart", attrs: { "aria-label": t("compare.chart_aria", { metric: title }) } });
      card.append(el("h3", { text: title }));
      if (!aligned.ts.length || !aligned.values.some((values) => values.some(isMeasurement))) {
        card.append(el("p", { class: "cp-muted-small", text: t("host.no_data_for_range") })); grid.append(card); continue;
      }
      const chartWrap = el("div", { class: "cp-chart-wrap", attrs: { role: "img", "aria-label": t("compare.chart_aria", { metric: title }) } });
      card.append(chartWrap); grid.append(card);
      state.charts.push(createTimeSeriesChart(chartWrap, { title, series: chartHosts.map((item) => ({ label: item.host.hostname || item.host.id, color: colors.get(item.host.id), unit })), timestamps: aligned.ts, values: aligned.values, valueFormatter: formatter, yAxisFormatter: formatter, syncKey: "cp-compare-charts", lineOnly: true }));
    }
  }

  function renderFleet() { renderPicker(); renderNotices(); renderSummary(); }
  function renderAll() { renderPicker(); renderNotices(); renderSummary(); renderCharts(); }

  async function loadMetrics() {
    metricsController?.abort();
    const selected = state.selected.slice();
    if (selected.length < 2 || state.fleet.length < 2) { state.metricsInFlight = false; renderAll(); return; }
    const request = ++state.request, current = new AbortController(); metricsController = current;
    state.historyKey = compareMetricsKey(selected, state.range);
    state.metricsInFlight = true; state.histories.clear(); state.errors.clear(); renderAll();
    const results = await Promise.all(selected.map(async (id) => {
      try { return { id, history: await getHostMetrics(id, state.range, current.signal) }; }
      catch (error) { return { id, error }; }
    }));
    if (controller.signal.aborted || current.signal.aborted || !isCurrentCompareRequest(request, state.request, selected, state.selected)) return;
    for (const result of results) {
      if (result.error) state.errors.set(result.id, result.error); else state.histories.set(result.id, result.history);
    }
    state.metricsInFlight = false;
    renderAll();
  }

  async function refresh() {
    if (controller.signal.aborted) return;
    try {
      const response = await getHosts(controller.signal);
      if (controller.signal.aborted) return;
      state.fleet = response.hosts || []; state.fleetError = false;
      const selection = reconcileCompareSelection({
        urlSelectionSeeded: state.urlSelectionSeeded,
        initialHosts,
        knownIDs: state.fleet.map((item) => item.host.id),
        selected: state.selected,
        unknown: state.unknown,
      });
      state.selected = selection.selected;
      state.unknown = selection.unknown;
      state.urlSelectionSeeded = selection.urlSelectionSeeded;
      if (state.selected.length >= 2 && shouldReloadCompareMetrics(state.historyKey, state.selected, state.range)) {
        await loadMetrics();
      } else {
        if (state.selected.length < 2) {
          metricsController?.abort();
          state.metricsInFlight = false;
          state.historyKey = null;
          state.histories.clear();
          state.errors.clear();
        }
        renderFleet();
      }
    } catch (error) {
      if (controller.signal.aborted) return;
      state.fleetError = true; renderAll();
    }
  }

  return { refresh, teardown() { controller.abort(); window.removeEventListener("cp-theme-change", onThemeChange); state.charts.forEach((chart) => chart.destroy()); } };
}
