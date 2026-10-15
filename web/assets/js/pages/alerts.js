// alerts.js — #/alerts page (SPEC-v0.5 §D): an "Active alerts" card
// (currently-firing events) plus a filterable/paginated lifecycle timeline
// of every alert_events row, each showing metric, value vs threshold,
// duration, and per-channel delivery results (✓/✗ with an error
// tooltip).
import { getActiveAlerts, getAlertEvents, ApiError, localizeApiError } from "../core/api.js";
import { el, clearChildren, emptyState, errorBanner } from "../ui/components.js";
import { selectField } from "../ui/select.js";
import { icon } from "../ui/icons.js";
import { formatDuration } from "../core/format.js";
import { eventSeverity, metricLabel } from "../core/alerts.js";
import { t } from "../core/i18n.js";
import { buildAlertTimeline, eventCondition, eventHost, eventDate, eventTone } from "./alerttimeline.js";

const PAGE_SIZE = 50;

const STATE_OPTIONS = [
  { value: "", get label() { return t("alerts_page.all_states"); } },
  { value: "firing", get label() { return t("alerts_page.firing"); } },
  { value: "resolved", get label() { return t("alerts_page.resolved"); } },
];

/**
 * mountAlertsPage renders the alerts page into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: (options?: {reason?: string}) => Promise<void>, teardown: () => void}}
 */
export function mountAlertsPage(container, { announce }) {
  const controller = new AbortController();
  const state = {
    stateFilter: "",
    hostFilter: "",
    events: [],
    hasMore: false,
    nextBefore: 0,
    loading: true,
    historyError: "",
    historyLoaded: false,
  };

  clearChildren(container);
  const root = el("div", { class: "cp-alerts-page" });
  const pageHead = el("div", { class: "cp-alerts-page-head" });
  pageHead.append(el("h1", { class: "cp-page-title", text: t("alerts_page.title") }), el("p", { class: "cp-page-subtitle", text: t("alerts_page.subtitle") }));
  root.append(pageHead);

  const bannerHost = el("div");
  const activeHost = el("div");
  const historyHost = el("div");
  root.append(bannerHost, activeHost, historyHost);
  container.append(root);
  renderHistoryCard(historyHost, state, { onFilterChange, onLoadMore, onRefresh: refresh });
  activeHost.append(el("p", { class: "cp-muted", attrs: { role: "status" }, text: t("alerts_page.activity_loading_active") }));
  let historyController = null;
  let historyRequest = 0;
  let activeLoaded = false;
  function readingPosition() {
    if (typeof window === "undefined" || !window.scrollY) return null;
    const anchor = Array.from(historyHost.querySelectorAll("[data-event-id]")).find((node) => {
      const rect = node.getBoundingClientRect();
      return rect.bottom > 0 && rect.top < window.innerHeight;
    });
    return anchor ? { eventID: anchor.getAttribute("data-event-id"), top: anchor.getBoundingClientRect().top, left: window.scrollX } : null;
  }
  function restoreReadingPosition(position) {
    if (!position) return;
    const anchor = Array.from(historyHost.querySelectorAll("[data-event-id]")).find((node) => node.getAttribute("data-event-id") === position.eventID);
    if (!anchor) return;
    const shift = anchor.getBoundingClientRect().top - position.top;
    if (shift) window.scrollTo({ left: position.left, top: window.scrollY + shift, behavior: "instant" });
  }
  const renderHistory = () => {
    const active = document.activeElement;
    const focusID = historyHost.contains(active) ? active?.id : null;
    const position = readingPosition();
    renderHistoryCard(historyHost, state, { onFilterChange, onLoadMore, onRefresh: refresh });
    if (focusID) Array.from(historyHost.querySelectorAll("[id]")).find((node) => node.id === focusID)?.focus({ preventScroll: true });
    restoreReadingPosition(position);
  };

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
      if (controller.signal.aborted) return false;
      const position = readingPosition();
      renderActiveCard(activeHost, resp.events || []);
      activeLoaded = true;
      clearBanner();
      restoreReadingPosition(position);
      return true;
    } catch (err) {
      if (err?.name === "AbortError") return false;
      const position = readingPosition();
      showBanner(describeError(err));
      if (!activeLoaded) {
        clearChildren(activeHost);
        activeHost.append(el("p", { class: "cp-muted", text: t("alerts_page.activity_active_error") }));
      }
      restoreReadingPosition(position);
      return false;
    }
  }

  async function loadHistory(reset, keepPrevious = false) {
    if (!reset && state.loading) return false;
    historyController?.abort();
    const request = ++historyRequest;
    const current = new AbortController();
    historyController = current;
    const abort = () => current.abort();
    controller.signal.addEventListener("abort", abort, { once: true });
    if (reset) {
      if (!keepPrevious) {
        state.events = [];
        state.nextBefore = 0;
        state.hasMore = false;
      }
    }
    state.loading = true;
    state.historyError = "";
    renderHistory();
    try {
      const resp = await getAlertEvents(
        { state: state.stateFilter || undefined, host: state.hostFilter || undefined, limit: PAGE_SIZE, before: reset ? undefined : state.nextBefore || undefined },
        current.signal,
      );
      if (controller.signal.aborted || request !== historyRequest) return false;
      const page = resp.events || [];
      const existing = new Set(state.events.map((event) => event.id));
      state.events = reset ? page : state.events.concat(page.filter((event) => !existing.has(event.id)));
      state.historyLoaded = true;
      state.hasMore = page.length === PAGE_SIZE;
      state.nextBefore = page.length > 0 ? page[page.length - 1].started_at : state.nextBefore;
      return true;
    } catch (err) {
      if (err?.name === "AbortError" || request !== historyRequest) return false;
      state.historyError = describeError(err);
      return false;
    } finally {
      controller.signal.removeEventListener("abort", abort);
      if (request === historyRequest && !controller.signal.aborted) { state.loading = false; renderHistory(); }
    }
  }

  function onFilterChange(next) {
    state.stateFilter = next.stateFilter;
    state.hostFilter = next.hostFilter;
    state.historyLoaded = false;
    loadHistory(true);
  }

  function onLoadMore() {
    loadHistory(false);
  }

  async function refresh({ reason = "manual" } = {}) {
    if (controller.signal.aborted) return;
    // Scheduled refreshes update active alerts, not the reader's history
    // window or an in-flight Load more/filter request. Explicit Refresh
    // still requests the newest page with the current state filter.
    const keepHistory = reason === "scheduled" && (state.historyLoaded || (state.loading && historyRequest > 0));
    const [activeOK, historyOK] = await Promise.all([loadActive(), keepHistory ? false : loadHistory(true, true)]);
    if (!controller.signal.aborted) {
      if (activeOK && historyOK) announce?.(t("alerts_page.alerts_updated"));
      else if (activeOK && keepHistory) announce?.(t("alerts_page.activity_active_updated"));
    }
  }

  function teardown() {
    controller.abort();
    historyController?.abort();
  }

  return { refresh, teardown };
}

export function renderActiveCard(host, events) {
  clearChildren(host);
  const card = el("section", { class: "cp-card cp-alerts-active-card", attrs: { "aria-labelledby": "cp-alerts-active-heading" } });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h2", { class: "cp-card-title", attrs: { id: "cp-alerts-active-heading" }, text: t("alerts_page.active_alerts") }));
  if (events.length > 0) {
    const tone = activeAlertsTone(events);
    head.append(el("span", { class: `cp-chip cp-chip-${tone} cp-alerts-active-summary-${tone}`, text: t("nav.alerts_firing_count", { count: events.length }) }));
  }
  card.append(head);

  if (events.length === 0) {
    card.append(emptyState({ title: t("alerts_page.all_clear"), message: t("alerts_page.no_alerts_firing"), iconName: "circleCheck" }));
    host.append(card);
    return;
  }

  const list = el("ul", { class: "cp-alerts-active-list", attrs: { role: "list" } });
  const groups = new Map();
  for (const ev of events) {
    const key = ev.host_id || "";
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(ev);
  }
  for (const hostEvents of groups.values()) {
    if (hostEvents.length > 1) {
      const group = el("li", { class: "cp-alerts-active-host-group" });
      const first = hostEvents[0];
      const hostLink = eventHost(first, "cp-alerts-active-host-group-name");
      group.append(el("div", { class: "cp-alerts-active-host-group-head", children: [hostLink, el("span", { class: "cp-chip cp-chip-other", attrs: { "aria-label": t("alerts_page.active_alert_count", { count: hostEvents.length }) }, text: String(hostEvents.length) })] }));
      const groupList = el("ul", { class: "cp-alerts-active-list", attrs: { role: "list" } });
      for (const ev of hostEvents) groupList.append(activeAlertRow(ev, true));
      group.append(groupList); list.append(group);
    } else list.append(activeAlertRow(hostEvents[0]));
  }
  card.append(list);
  host.append(card);
}

/** Returns the highest active severity for aggregate alert-count tone. */
export function activeAlertsTone(events) {
  return events.some((event) => eventSeverity(event) === "critical") ? "critical" : "warning";
}

function activeAlertRow(ev, grouped = false) {
  const tone = eventTone(ev);
  const item = el("li", { class: `cp-alerts-active-item cp-alerts-active-item-${tone}` });
    const link = el("div", { class: "cp-alerts-active-link" });
    link.append(
      icon("circleAlert"),
      el("span", { class: "cp-alerts-active-name", text: ev.rule_name || metricLabel(ev.metric) }),
    );
    if (!grouped) link.append(eventHost(ev, "cp-alerts-active-host"));
    const detail = el("div", { class: "cp-alerts-active-detail" });
    detail.append(
      el("span", { text: eventCondition(ev) }),
      el("span", { text: eventDate(ev.started_at) && ev.started_at <= Date.now() / 1000 ? t("alerts_page.firing_for", { duration: formatDuration(Math.floor(Date.now() / 1000) - ev.started_at) }) : t("alerts_page.activity_duration_unknown") }),
    );
    item.append(link, detail);
  return item;
}

export function renderHistoryCard(host, state, { onFilterChange, onLoadMore, onRefresh }) {
  clearChildren(host);
  const card = el("section", { class: "cp-card cp-activity-card", attrs: { "aria-labelledby": "cp-activity-heading", "aria-busy": state.loading ? "true" : "false" } });
  const head = el("div", { class: "cp-activity-header" });
  const title = el("div", { class: "cp-activity-title-group" });
  title.append(el("h2", { class: "cp-card-title", attrs: { id: "cp-activity-heading" }, text: t("alerts_page.activity_title") }), el("p", { class: "cp-muted-small", text: t("alerts_page.activity_description") }));
  head.append(title);
  const filterRow = el("div", { class: "cp-settings-row cp-alerts-filter-row" });
  const { node: stateNode, select: stateSelect } = selectField({
    id: "cp-alerts-state-filter",
    label: t("alerts_page.filter_by_state"),
    options: STATE_OPTIONS,
    value: state.stateFilter,
  });
  stateSelect.addEventListener("change", () => onFilterChange({ stateFilter: stateSelect.value, hostFilter: state.hostFilter }));
  filterRow.append(stateNode);
  const controls = el("div", { class: "cp-activity-controls" });
  const refreshBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button", id: "cp-activity-refresh" }, text: t("alerts_page.activity_refresh") });
  refreshBtn.append(icon("refreshCw"));
  refreshBtn.addEventListener("click", () => onRefresh?.());
  controls.append(filterRow, refreshBtn);
  head.append(controls);
  card.append(head);
  card.append(el("p", { class: "cp-muted-small", text: t("alerts_page.activity_refresh_hint") }));
  if (state.loading) card.append(el("p", { class: "cp-activity-notice", attrs: { role: "status" }, text: t("alerts_page.activity_loading") }));
  if (state.historyError) card.append(errorBanner(state.historyError));
  if (state.events.length === 0) {
    if (!state.loading && !state.historyError) card.append(emptyState({ title: state.stateFilter ? t("alerts_page.activity_no_match") : t("alerts_page.no_events_yet"), message: state.stateFilter ? t("alerts_page.activity_try_all") : t("alerts_page.history_appears_here"), iconName: "activity" }));
    if (state.historyError) {
      const retry = el("button", { class: "cp-btn", attrs: { type: "button", id: "cp-activity-retry" }, text: t("alerts_page.activity_retry") });
      retry.addEventListener("click", () => onFilterChange({ stateFilter: state.stateFilter, hostFilter: state.hostFilter }));
      card.append(retry);
    }
    host.append(card);
    return;
  }
  card.append(buildAlertTimeline(state.events));
  const footer = el("div", { class: "cp-activity-footer" });
  footer.append(el("p", { class: "cp-muted-small", text: t("alerts_page.activity_showing", { count: state.events.length }) }));
  if (state.hasMore) {
    const moreBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button", id: "cp-activity-load-more", ...(state.loading ? { disabled: "" } : {}) }, text: t("alerts_page.load_more") });
    moreBtn.addEventListener("click", onLoadMore);
    footer.append(moreBtn);
  }
  card.append(footer);
  host.append(card);
}

function describeError(err) {
  if (err instanceof ApiError) return t("overview.api_error_prefix", { message: localizeApiError(err) });
  return t("alerts_page.hub_unreachable");
}
