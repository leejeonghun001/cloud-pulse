// alerts.js — #/alerts page (SPEC-v0.5 §D): an "Active alerts" card
// (currently-firing events) plus a filterable/paginated history table
// of every alert_events row, each showing metric, value vs threshold,
// duration, and per-channel delivery results (✓/✗ with an error
// tooltip).
import { getActiveAlerts, getAlertEvents, ApiError } from "../core/api.js";
import { el, clearChildren, emptyState, errorBanner, statusDot } from "../ui/components.js";
import { selectField } from "../ui/select.js";
import { icon } from "../ui/icons.js";
import { formatRelativeTimeFromUnixSeconds, formatDuration } from "../core/format.js";
import { metricLabel, formatThreshold } from "../core/alerts.js";

const PAGE_SIZE = 50;

const STATE_OPTIONS = [
  { value: "", label: "All states" },
  { value: "firing", label: "Firing" },
  { value: "resolved", label: "Resolved" },
];

/**
 * mountAlertsPage renders the alerts page into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountAlertsPage(container, { announce }) {
  const controller = new AbortController();
  const state = {
    stateFilter: "",
    hostFilter: "",
    events: [],
    hasMore: false,
    nextBefore: 0,
  };

  clearChildren(container);
  const root = el("div", { class: "cp-alerts-page" });
  root.append(el("h1", { class: "cp-page-title", text: "Alerts" }));
  root.append(el("p", { class: "cp-page-subtitle", text: "Currently firing alerts and delivery history." }));

  const bannerHost = el("div");
  const activeHost = el("div");
  const historyHost = el("div");
  root.append(bannerHost, activeHost, historyHost);
  container.append(root);

  function showBanner(message) {
    clearChildren(bannerHost);
    bannerHost.append(errorBanner(message));
  }
  function clearBanner() {
    clearChildren(bannerHost);
  }

  async function loadActive() {
    try {
      const resp = await getActiveAlerts(controller.signal);
      renderActiveCard(activeHost, resp.events || []);
    } catch (err) {
      if (err?.name === "AbortError") return;
      showBanner(describeError(err));
    }
  }

  async function loadHistory(reset) {
    if (reset) {
      state.events = [];
      state.nextBefore = 0;
    }
    try {
      const resp = await getAlertEvents(
        { state: state.stateFilter || undefined, host: state.hostFilter || undefined, limit: PAGE_SIZE, before: state.nextBefore || undefined },
        controller.signal,
      );
      const page = resp.events || [];
      state.events = reset ? page : state.events.concat(page);
      state.hasMore = page.length === PAGE_SIZE;
      state.nextBefore = page.length > 0 ? page[page.length - 1].started_at : state.nextBefore;
      clearBanner();
      renderHistoryCard(historyHost, state, { onFilterChange, onLoadMore });
    } catch (err) {
      if (err?.name === "AbortError") return;
      showBanner(describeError(err));
    }
  }

  function onFilterChange(next) {
    state.stateFilter = next.stateFilter;
    state.hostFilter = next.hostFilter;
    loadHistory(true);
  }

  function onLoadMore() {
    loadHistory(false);
  }

  async function refresh() {
    await Promise.all([loadActive(), loadHistory(state.events.length === 0)]);
    announce?.("Alerts updated.");
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

function renderActiveCard(host, events) {
  clearChildren(host);
  const card = el("div", { class: "cp-card cp-alerts-active-card" });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h2", { text: "Active alerts" }));
  if (events.length > 0) {
    head.append(el("span", { class: "cp-chip cp-chip-critical", text: `${events.length} firing` }));
  }
  card.append(head);

  if (events.length === 0) {
    card.append(emptyState({ title: "All clear", message: "No alerts are currently firing.", iconName: "circleCheck" }));
    host.append(card);
    return;
  }

  const list = el("ul", { class: "cp-alerts-active-list", attrs: { role: "list" } });
  for (const ev of events) {
    const item = el("li", { class: "cp-alerts-active-item" });
    const link = el("a", { class: "cp-alerts-active-link", attrs: { href: `#/host/${encodeURIComponent(ev.host_id)}` } });
    link.append(
      statusDot("down"),
      el("span", { class: "cp-alerts-active-name", text: ev.rule_name || metricLabel(ev.metric) }),
      el("span", { class: "cp-alerts-active-host", text: ev.hostname || ev.host_id }),
    );
    item.append(link);
    item.append(
      el("span", {
        class: "cp-muted-small",
        text: `${metricLabel(ev.metric)}: ${ev.value.toFixed(1)} vs ${formatThreshold(ev.metric, ev.threshold) || ev.threshold}`,
      }),
    );
    item.append(
      el("span", {
        class: "cp-muted-small",
        text: `Firing for ${formatDuration(Math.max(0, Math.floor(Date.now() / 1000) - ev.started_at))}`,
      }),
    );
    list.append(item);
  }
  card.append(list);
  host.append(card);
}

function renderHistoryCard(host, state, { onFilterChange, onLoadMore }) {
  clearChildren(host);
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { text: "History" }));

  const filterRow = el("div", { class: "cp-settings-row cp-alerts-filter-row" });
  const { node: stateNode, select: stateSelect } = selectField({
    id: "cp-alerts-state-filter",
    ariaLabel: "Filter by state",
    options: STATE_OPTIONS,
    value: state.stateFilter,
  });
  stateSelect.addEventListener("change", () => onFilterChange({ stateFilter: stateSelect.value, hostFilter: state.hostFilter }));
  filterRow.append(stateNode);
  card.append(filterRow);

  if (state.events.length === 0) {
    card.append(emptyState({ title: "No events yet", message: "Alert history will appear here once a rule fires." }));
    host.append(card);
    return;
  }

  const tableWrap = el("div", { class: "cp-table-wrap" });
  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  thead.append(
    el("tr", {
      children: ["State", "Rule", "Host", "Metric", "Value", "Started", "Duration", "Delivery"].map((t) => el("th", { text: t })),
    }),
  );
  table.append(thead);

  const tbody = el("tbody");
  for (const ev of state.events) {
    tbody.append(historyRow(ev));
  }
  table.append(tbody);
  tableWrap.append(table);
  card.append(tableWrap);

  if (state.hasMore) {
    const moreBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Load more" });
    moreBtn.addEventListener("click", onLoadMore);
    card.append(moreBtn);
  }

  host.append(card);
}

function historyRow(ev) {
  const isFiring = ev.state === "firing";
  const endedAt = isFiring ? Math.floor(Date.now() / 1000) : ev.resolved_at;
  const durationSec = Math.max(0, endedAt - ev.started_at);

  return el("tr", {
    children: [
      el("td", {
        children: [
          el("span", {
            class: `cp-chip ${isFiring ? "cp-chip-critical" : "cp-chip-ok"}`,
            text: isFiring ? "Firing" : "Resolved",
          }),
        ],
      }),
      el("td", { text: ev.rule_name || "" }),
      el("td", {
        children: [el("a", { attrs: { href: `#/host/${encodeURIComponent(ev.host_id)}` }, text: ev.hostname || ev.host_id })],
      }),
      el("td", { text: metricLabel(ev.metric) }),
      el("td", { text: `${ev.value.toFixed(1)} / ${formatThreshold(ev.metric, ev.threshold) || ev.threshold}` }),
      el("td", { text: formatRelativeTimeFromUnixSeconds(ev.started_at) }),
      el("td", { text: formatDuration(durationSec) }),
      el("td", { children: [deliveryChips(ev.deliveries)] }),
    ],
  });
}

function deliveryChips(deliveries) {
  const wrap = el("span", { class: "cp-alerts-delivery-chips" });
  if (!deliveries || deliveries.length === 0) {
    wrap.append(el("span", { class: "cp-muted-small", text: "—" }));
    return wrap;
  }
  for (const d of deliveries) {
    const chip = el("span", {
      class: `cp-alerts-delivery-chip ${d.ok ? "cp-alerts-delivery-ok" : "cp-alerts-delivery-fail"}`,
      attrs: { title: d.ok ? `${d.channel_name}: delivered` : `${d.channel_name}: ${d.error || "delivery failed"}` },
    });
    chip.append(icon(d.ok ? "check" : "x"), el("span", { text: d.channel_name }));
    wrap.append(chip);
  }
  return wrap;
}

function describeError(err) {
  if (err instanceof ApiError) return `API error: ${err.message}`;
  return "Unable to reach the hub API.";
}
