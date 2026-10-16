// overview.js — fleet overview page (#/): "All Systems" card (sortable
// filterable table with a grid view toggle, columns dropdown, mobile
// card list, empty state), a compact stat-cards row, a "Monthly
// traffic" card, and an "Object storage" card. Redesigned per
// SPEC-v0.4 §4 on top of the v0.3.x data layer (zero feature loss:
// egress split, hub-override badges, update badges, buckets free tier,
// 15s refresh + visibility pause + AbortController all preserved).
import { getHosts, getEgress, getBuckets, getStorageAccounts, getBilling, ApiError } from "../core/api.js";
import { formatDisplayAmount, formatUSD } from "../core/currency.js";
import { spendSummary } from "../core/billing-view.js";
import { loadDisplayCurrency } from "../core/display-currency.js";
import {
  el,
  clearChildren,
  errorBanner,
  statusDot,
  providerBadge,
  progressBar,
  levelClassForBar,
  hubOverrideBadge,
  skeletonCards,
  skeletonTable,
} from "../ui/components.js";
import { icon } from "../ui/icons.js";
import { sortableHeader, nextSortState, sortRows } from "../ui/table.js";
import { createDropdown, dropdownSeparator } from "../ui/dropdown.js";
import {
  formatBytes,
  formatBitrate,
  formatPercent,
  formatRelativeTimeFromUnixSeconds,
  formatLoad,
  formatNumber,
} from "../core/format.js";
import { getItem, setItem, getJSON, setJSON } from "../core/store.js";
import { buildEgressSection, currentMonth, getStoredDirection, setStoredDirection } from "./egress.js";
import { buildBucketsSection } from "./buckets.js";
import { buildStorageAccountsSection } from "./storage-overview.js";
import { outdatedAgentCount, agentVersionText, hostUpdateBadgeText } from "../core/updates.js";
import { applyFacets, facetCounts, facetsActive, normalizeFacets } from "../core/hostfilter.js";
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
import { t } from "../core/i18n.js";
import { localizeApiError } from "../core/api.js";
import { hostGridCard } from "./hostgrid.js";
import { selectField } from "../ui/select.js";

// These cloud-backed summaries are collected by the hub on a much slower
// cadence than host telemetry. Reusing a successful snapshot for one minute
// avoids presenting a 15-second UI refresh as freshly collected cloud data.
export const CLOUD_SUMMARY_REFRESH_MS = 60000;

/** Pure freshness check kept separate so cloud request cadence is testable
 * without mounting the dashboard DOM. */
export function isCloudSummaryFresh(snapshot, snapshotAtMs, month, nowMs = Date.now()) {
  return Boolean(snapshot && snapshot.month === month && nowMs - snapshotAtMs < CLOUD_SUMMARY_REFRESH_MS);
}

/**
 * mountOverviewPage renders the overview page into container and
 * returns a handle the shell can use to refresh/teardown it.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @param {(opener: HTMLElement, options: {signal: AbortSignal}) => Promise<void>|void} [ctx.onAddAgent] opens the existing enrollment dialog on explicit request
 * @returns {{refresh: (showLoading: boolean) => Promise<void>, teardown: () => void}}
 */
export function mountOverviewPage(container, { announce, onAddAgent }) {
  const controller = new AbortController();
  let resizeFrame = null;
  const syncTableOverflowOnResize = () => {
    if (resizeFrame !== null) return;
    resizeFrame = globalThis.requestAnimationFrame(() => {
      resizeFrame = null;
      const wrap = container.querySelector(".cp-systems-table-wrap");
      if (wrap) syncTableOverflow(wrap);
    });
  };
  globalThis.addEventListener?.("resize", syncTableOverflowOnResize);
  const storedView = getItem(VIEW_STORAGE_KEY);
  const state = {
    month: currentMonth(),
    direction: getStoredDirection(),
    charts: [],
    hosts: [],
    hostsLoaded: false,
    hostsError: "",
    filter: "",
    // Facet chips are page-session state: a stored filter that silently
    // hides hosts on the next visit would be more confusing than helpful.
    facets: normalizeFacets(null),
    sort: getJSON(SORT_STORAGE_KEY, { key: "system", direction: "asc" }),
    visibleColumns: normalizeVisibleColumns(getJSON(COLUMNS_STORAGE_KEY, null)),
    view: storedView === "grid" ? "grid" : "table",
    hasStoredView: storedView === "grid" || storedView === "table",
    hasResolvedDefaultView: false,
    cloudSnapshot: null,
    cloudSnapshotAtMs: 0,
    lastUpdatedMs: 0,
  };

  clearChildren(container);
  const pageHeader = el("div", { class: "cp-overview-page-header" });
  const titleLine = el("div", { class: "cp-overview-title-line" });
  titleLine.append(el("h1", { class: "cp-page-title", text: t("overview.page_title") }));
  const liveStatus = el("span", { class: "cp-overview-live-status" });
  liveStatus.append(el("span", { class: "cp-overview-live-dot", attrs: { "aria-hidden": "true" } }), el("span", { text: t("overview.page_subtitle") }));
  titleLine.append(liveStatus);
  pageHeader.append(titleLine, el("p", { class: "cp-page-subtitle", text: t("overview.page_description") }));
  container.append(pageHeader);

  // Keep an inactive banner out of the shell's shared page gap. Otherwise
  // this empty node creates an extra gap only between the header and KPIs.
  const bannerHost = el("div");
  bannerHost.hidden = true;
  const statCardsHost = el("div", { class: "cp-stat-cards" });
  const spendHost = el("div", { class: "cp-spend-host" });
  spendHost.hidden = true;
  const systemsCard = el("section", { class: "cp-card cp-systems-card", attrs: { "aria-labelledby": "cp-systems-heading" } });
  const egressHost = el("div", { class: "cp-egress-host" });
  const bucketsHost = el("div", { class: "cp-buckets-host" });
  const storageAccountsHost = el("div", { class: "cp-storage-accounts-host" });
  container.append(bannerHost, spendHost, statCardsHost, systemsCard, egressHost, bucketsHost, storageAccountsHost);

  function showBanner(message, retry = false) {
    bannerHost.hidden = false;
    clearChildren(bannerHost);
    const banner = errorBanner(message);
    if (retry) {
      const button = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: t("common.retry") });
      button.addEventListener("click", () => refresh());
      banner.append(button);
    }
    bannerHost.append(banner);
  }
  function clearBanner() {
    clearChildren(bannerHost);
    bannerHost.hidden = true;
  }

  function renderSystemsCardBody() {
    // Rebuilding the header must not interrupt typing or a timed refresh's
    // focused search field. Keep the caret as well as the current query.
    const active = document.activeElement;
    const focusedFilter = systemsCard.contains(active) && active?.matches(".cp-systems-filter .cp-input");
    const selection = focusedFilter ? [active.selectionStart, active.selectionEnd] : null;
    const focusedSort = systemsCard.contains(active) && active?.matches(".cp-host-grid-sort select, .cp-host-grid-sort button");
    const focusedCardHref = systemsCard.contains(active) && active?.matches("a.cp-host-grid-card") ? active.getAttribute("href") : null;
    const focusedOnboardingID = systemsCard.contains(active) && active?.matches(".cp-onboarding button, .cp-overview-load-state button") ? active.id : null;
    const focusedFacet = systemsCard.contains(active) && active?.matches("[data-facet]") ? active.getAttribute("data-facet") : null;
    renderSystemsCard(systemsCard, state, {
      onAddAgent: onAddAgent ? (opener) => {
        if (!controller.signal.aborted) return onAddAgent(opener, { signal: controller.signal });
      } : undefined,
      onRetry: refresh,
      onFilterChange: (q) => {
        state.filter = q;
        renderSystemsCardBody();
      },
      onFacetsChange: (facets) => {
        state.facets = normalizeFacets(facets);
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
        state.hasStoredView = true;
        setItem(VIEW_STORAGE_KEY, v);
        renderSystemsCardBody();
      },
    });
    if (focusedFilter) {
      const input = systemsCard.querySelector(".cp-systems-filter .cp-input");
      input?.focus({ preventScroll: true });
      if (selection && selection.every((value) => value !== null)) input?.setSelectionRange(...selection);
    } else if (focusedSort) {
      systemsCard.querySelector(active.tagName === "SELECT" ? ".cp-host-grid-sort select" : ".cp-host-grid-sort button")?.focus({ preventScroll: true });
    } else if (focusedCardHref) {
      // Preserve a keyboard user's place across normal 15-second refreshes.
      Array.from(systemsCard.querySelectorAll("a.cp-host-grid-card")).find((link) => link.getAttribute("href") === focusedCardHref)?.focus({ preventScroll: true });
    } else if (focusedOnboardingID) {
      systemsCard.querySelector(`#${focusedOnboardingID}`)?.focus({ preventScroll: true });
    } else if (focusedFacet) {
      Array.from(systemsCard.querySelectorAll("[data-facet]")).find((chip) => chip.getAttribute("data-facet") === (focusedFacet === "clear" ? "status:all" : focusedFacet))?.focus({ preventScroll: true });
    }
  }
  renderSystemsCardBody();

  async function refresh() {
    if (controller.signal.aborted) return;
    if (!state.hostsLoaded) { state.hostsError = ""; renderSystemsCardBody(); }
    try {
      const cloudFresh = isCloudSummaryFresh(state.cloudSnapshot, state.cloudSnapshotAtMs, state.month);
      const cloudPromise = cloudFresh
        ? Promise.resolve(state.cloudSnapshot)
        : Promise.all([
          getEgress(state.month, controller.signal),
          getBuckets(controller.signal),
          getStorageAccounts(controller.signal).catch((err) => {
            if (err?.name === "AbortError") throw err;
            return { accounts: [] };
          }),
          // Spend is supplementary: a billing failure must not blank the
          // fleet view, so it degrades to "no figures" instead.
          getBilling(controller.signal).catch((err) => {
            if (err?.name === "AbortError") throw err;
            return null;
          }),
        ]).then(([egress, buckets, storageAccounts, billing]) => ({
          month: state.month,
          fetchedAtMs: Date.now(),
          egress,
          buckets,
          storageAccounts,
          billing,
        }));
      const [hostsResp, displayCurrency, cloudSnapshot] = await Promise.all([
        getHosts(controller.signal),
        loadDisplayCurrency(controller.signal),
        cloudPromise,
      ]);
      if (controller.signal.aborted) return;
      if (!cloudFresh) {
        state.cloudSnapshot = cloudSnapshot;
        state.cloudSnapshotAtMs = cloudSnapshot.fetchedAtMs;
      }
      state.displayCurrency = displayCurrency;
      clearBanner();

      const nowMs = Date.now();
      state.hosts = hostsResp.hosts;
      state.hostsLoaded = true;
      state.hostsError = "";
      state.nowMs = nowMs;
      state.lastUpdatedMs = nowMs;
      if (!state.hasStoredView && !state.hasResolvedDefaultView) {
        state.view = state.hosts.length <= 12 ? "grid" : "table";
        state.hasResolvedDefaultView = true;
      }
      renderSystemsCardBody();
      // No hosts means no measured fleet CPU/rates. The confirmed host count
      // lives beside the onboarding guide; don't present placeholder metrics.
      renderSpend(spendHost, cloudSnapshot.billing);
      statCardsHost.hidden = state.hosts.length === 0;
      if (state.hosts.length === 0) clearChildren(statCardsHost);
      else renderStatCards(statCardsHost, hostsResp, cloudSnapshot.buckets, nowMs);

      const noFleetOrBuckets = state.hosts.length === 0 && (cloudSnapshot.buckets.buckets || []).length === 0;
      clearChildren(egressHost);
      clearChildren(bucketsHost);
      clearChildren(storageAccountsHost);
      egressHost.hidden = noFleetOrBuckets;
      bucketsHost.hidden = noFleetOrBuckets;
      storageAccountsHost.hidden = noFleetOrBuckets;
      if (!noFleetOrBuckets) egressHost.append(
        buildEgressSection({
          month: state.month,
          direction: state.direction,
          hosts: cloudSnapshot.egress.hosts,
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
      if (!noFleetOrBuckets) {
        const { node, charts } = buildBucketsSection({
          buckets: cloudSnapshot.buckets.buckets,
          collectors: cloudSnapshot.buckets.collectors,
          nowMs,
        });
        bucketsHost.append(node);
        state.charts = charts;
        storageAccountsHost.append(buildStorageAccountsSection({ accounts: cloudSnapshot.storageAccounts.accounts || [], nowMs }));
      } else state.charts = [];

      announce?.(t("overview.dashboard_refreshed"));
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (controller.signal.aborted) return;
      state.hostsError = describeError(err);
      showBanner(state.hostsLoaded ? t("overview.refresh_failed_keep_data") : state.hostsError, state.hostsLoaded);
      renderSystemsCardBody();
    }
  }

  function teardown() {
    globalThis.removeEventListener?.("resize", syncTableOverflowOnResize);
    if (resizeFrame !== null) globalThis.cancelAnimationFrame?.(resizeFrame);
    controller.abort();
    state.charts.forEach((c) => c.destroy());
    state.charts = [];
  }

  return { refresh, teardown };
}

// ---------------------------------------------------------------------------
// This month's cloud spend
// ---------------------------------------------------------------------------

const PROVIDER_LABELS = { aws: "AWS", oci: "OCI" };

/**
 * renderSpend shows this month's provider-reported spend, one figure per
 * billing currency, linking to the Costs page. Hidden when billing is off
 * on the hub (billing null or no snapshots at all); a hub where no
 * provider is connected yet gets a one-line pointer to set one up.
 */
export function renderSpend(host, billing) {
  clearChildren(host);
  const snapshots = billing?.snapshots || [];
  host.hidden = snapshots.length === 0;
  if (host.hidden) return;
  const { totals, attention, stale } = spendSummary(snapshots);

  const section = el("section", { class: "cp-card cp-spend-card", attrs: { "aria-labelledby": "cp-spend-heading" } });
  const head = el("div", { class: "cp-spend-head" });
  head.append(el("h2", { class: "cp-card-title", attrs: { id: "cp-spend-heading" }, text: t("overview.spend_title") }));
  head.append(el("a", { class: "cp-back-link", attrs: { href: "#/costs" }, text: t("overview.spend_view_costs") }));
  section.append(head);

  if (totals.length === 0 && attention.length === 0) {
    const empty = el("p", { class: "cp-muted-small" });
    empty.append(el("span", { text: `${t("overview.spend_not_connected")} ` }));
    empty.append(el("a", { attrs: { href: "#/settings/billing" }, text: t("overview.spend_connect") }));
    section.append(empty);
    host.append(section);
    return;
  }

  const list = el("dl", { class: "cp-spend-totals" });
  for (const row of totals) {
    const item = el("div", { class: "cp-spend-total" });
    item.append(el("dt", { class: "cp-spend-providers", text: row.providers.map((p) => PROVIDER_LABELS[p] || p.toUpperCase()).join(" + ") }));
    item.append(el("dd", { class: "cp-spend-value", text: formatUSD(row.mtd, row.currency, { taxNote: false }) }));
    item.append(el("dd", { class: "cp-muted-small", text: t("overview.spend_forecast", { amount: formatUSD(row.forecast, row.currency, { taxNote: false }) }) }));
    list.append(item);
  }
  if (totals.length > 0) section.append(list);

  const notes = [];
  if (attention.length > 0) {
    notes.push(t("overview.spend_attention", { providers: attention.map((p) => PROVIDER_LABELS[p] || p.toUpperCase()).join(", ") }));
  }
  if (stale) notes.push(t("costs.stale_data"));
  notes.push(t("costs.excl_tax"));
  section.append(el("p", { class: attention.length > 0 ? "cp-spend-note cp-spend-note-attention" : "cp-spend-note", text: notes.join(" · ") }));
  host.append(section);
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

  const cards = [
    { icon: "server", label: t("overview.summary_hosts_up"), value: formatNumber(up.length), tone: "ok" },
    { icon: "wifiOff", label: t("overview.summary_hosts_down"), value: formatNumber(down.length), tone: down.length > 0 ? "critical" : "neutral" },
    { icon: "cpu", label: t("overview.summary_avg_cpu"), value: formatPercent(avgCPU), tone: "neutral" },
    { icon: "arrowUp", label: t("overview.summary_fleet_outbound"), value: formatBytes(fleetEgressBytes), tone: "neutral" },
    { icon: "arrowDown", label: t("overview.summary_fleet_inbound"), value: formatBytes(fleetIngressBytes), tone: "neutral" },
    { icon: "database", label: t("overview.summary_buckets_tracked"), value: formatNumber(bucketsResp.buckets.length), tone: "neutral" },
  ];
  if (outdated > 0) {
    cards.push({ icon: "cloudUpload", label: t("overview.summary_agents_outdated"), value: formatNumber(outdated), tone: "warning" });
  }

  clearChildren(host);
  host.className = `cp-stat-cards cp-stat-cards-${cards.length}${cards.length % 2 ? " cp-stat-cards-odd" : ""}`;
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
  titleCol.append(el("h2", { class: "cp-card-title", attrs: { id: "cp-systems-heading" }, text: t("overview.all_systems_title") }));
  const subtitle = state.hostsLoaded ? el("p", { class: "cp-page-subtitle", text: t("overview.host_count", { count: state.hosts.length }) }) : null;
  if (subtitle) titleCol.append(subtitle);
  head.append(titleCol);

  if (!state.hostsLoaded || (state.hostsError && state.hosts.length === 0)) {
    card.append(head);
    const notice = el("div", { class: "cp-overview-load-state" });
    if (!state.hostsError) {
      notice.append(skeletonCards(4), skeletonTable(4, 4), el("p", { class: "sr-only", attrs: { role: "status" }, text: t("overview.onboarding_loading") }));
      card.append(notice);
      return;
    }
    notice.append(icon("triangleAlert"));
    const noticeText = t("overview.onboarding_load_error");
    notice.append(el("p", { class: "cp-muted", text: noticeText }));
    {
      const retry = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button", id: "cp-overview-retry" }, text: t("overview.onboarding_retry") });
      retry.addEventListener("click", () => handlers.onRetry?.());
      notice.append(retry);
    }
    card.append(notice);
    return;
  }

  if (state.hosts.length === 0) {
    card.append(head, buildFirstAgentGuide(handlers.onAddAgent), buildOptionalNextSteps());
    return;
  }

  const controls = el("div", { class: "cp-systems-controls" });

  const filterWrap = el("div", { class: "cp-input-group cp-systems-filter" });
  const filterIcon = el("span", { class: "cp-input-group-icon", attrs: { "aria-hidden": "true" } });
  filterIcon.append(icon("search"));
  const filterInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input",
      attrs: { type: "search", placeholder: t("overview.filter_hosts_placeholder"), "aria-label": t("overview.filter_hosts_aria_label"), value: state.filter },
    })
  );
  filterInput.addEventListener("input", () => handlers.onFilterChange(filterInput.value));
  filterWrap.append(filterIcon, filterInput);
  controls.append(filterWrap);

  controls.append(buildViewToggle(state.view, handlers.onViewChange));
  controls.append(buildColumnsDropdown(state.visibleColumns, handlers.onColumnsChange));
  if (state.view === "grid") controls.append(buildGridSort(state.sort, handlers.onSortChange));

  head.append(controls);
  card.append(head);

  const textMatched = filterRows(state.hosts, state.filter);
  card.append(buildFacetBar(textMatched, state.facets, handlers.onFacetsChange, state.hosts));
  const filtered = applyFacets(textMatched, state.facets);
  if (filtered.length !== state.hosts.length) {
    subtitle.textContent = t("overview.hosts_shown", { shown: filtered.length, total: state.hosts.length });
  }

  if (filtered.length === 0) {
    const message = textMatched.length === 0
      ? t("overview.no_hosts_match_filter", { filter: state.filter })
      : t("overview.no_hosts_match_facets");
    card.append(el("p", { class: "cp-muted", attrs: { role: "status" }, text: message }));
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

function buildOptionalNextSteps() {
  const section = el("section", { class: "cp-optional-next-steps", attrs: { "aria-labelledby": "cp-optional-next-steps" } });
  section.append(el("h3", { attrs: { id: "cp-optional-next-steps" }, text: t("overview.optional_next_steps") }));
  const list = el("ul", { attrs: { role: "list" } });
  const steps = [
    ["database", t("overview.optional_storage"), "#/settings/storage"],
    ["gauge", t("overview.optional_limits"), "#/settings/traffic"],
    ["bell", t("overview.optional_notifications"), "#/settings/notifications"],
  ];
  for (const [iconName, label, href] of steps) {
    const row = el("li");
    row.append(icon(iconName), el("a", { attrs: href.startsWith("http") ? { href, target: "_blank", rel: "noopener noreferrer" } : { href }, text: label }));
    list.append(row);
  }
  section.append(list);
  return section;
}

function buildFirstAgentGuide(onAddAgent) {
  const guide = el("div", { class: "cp-onboarding" });
  const intro = el("div", { class: "cp-onboarding-intro" });
  const mark = el("span", { class: "cp-onboarding-mark", attrs: { "aria-hidden": "true" } });
  mark.append(icon("server"));
  intro.append(mark, el("h3", { class: "cp-onboarding-title", text: t("overview.onboarding_title") }), el("p", { class: "cp-onboarding-description", text: t("overview.onboarding_description") }));
  const actions = el("div", { class: "cp-onboarding-actions" });
  const feedback = el("p", { class: "cp-muted-small", attrs: { role: "alert" } });
  const add = el(onAddAgent ? "button" : "a", {
    class: "cp-btn cp-btn-primary",
    attrs: onAddAgent ? { type: "button", id: "cp-onboarding-add-agent" } : { href: "#/settings/agents" },
    children: [icon("plus"), el("span", { text: t("nav.add_agent") })],
  });
  let opening = false;
  if (onAddAgent) add.addEventListener("click", async () => {
    if (opening) return;
    opening = true;
    // Keep the native button focusable while the command request is pending.
    // Disabling a focused button makes Firefox drop its keyboard focus.
    add.setAttribute("aria-disabled", "true");
    feedback.textContent = "";
    try { await onAddAgent(add); }
    catch { if (add.isConnected) feedback.textContent = t("overview.onboarding_open_error"); }
    finally { opening = false; add.removeAttribute("aria-disabled"); }
  });
  actions.append(add, el("a", { class: "cp-back-link", attrs: { href: "https://github.com/leejeonghun001/cloud-pulse/blob/main/docs/install.md", target: "_blank", rel: "noopener noreferrer" }, text: t("overview.onboarding_install_guide") }));
  intro.append(actions, feedback);
  const steps = el("ol", { class: "cp-onboarding-steps", attrs: { role: "list" } });
  for (const step of [1, 2, 3]) {
    const item = el("li", { class: "cp-onboarding-step" });
    const copy = el("div");
    copy.append(el("h4", { text: t(`overview.onboarding_step_${step}_title`) }), el("p", { text: t(`overview.onboarding_step_${step}_description`) }));
    item.append(el("span", { class: "cp-onboarding-step-number", attrs: { "aria-hidden": "true" }, text: String(step) }), copy);
    steps.append(item);
  }
  guide.append(intro, steps);
  return guide;
}

function buildViewToggle(current, onChange) {
  const group = el("div", { class: "cp-segmented cp-view-toggle", attrs: { role: "group", "aria-label": t("overview.view_toggle_aria_label") } });
  const tableBtn = el("button", {
    class: "cp-btn cp-segmented-btn cp-btn-icon",
    attrs: { type: "button", "aria-pressed": current === "table" ? "true" : "false", "aria-label": t("overview.table_view"), title: t("overview.table_view") },
  });
  tableBtn.append(icon("table"));
  const gridBtn = el("button", {
    class: "cp-btn cp-segmented-btn cp-btn-icon",
    attrs: { type: "button", "aria-pressed": current === "grid" ? "true" : "false", "aria-label": t("overview.grid_view"), title: t("overview.grid_view") },
  });
  gridBtn.append(icon("layoutGrid"));
  tableBtn.addEventListener("click", () => onChange("table"));
  gridBtn.addEventListener("click", () => onChange("grid"));
  group.append(tableBtn, gridBtn);
  return group;
}

function buildGridSort(sort, onChange) {
  const wrap = el("div", { class: "cp-host-grid-sort" });
  const { node, select } = selectField({
    id: "cp-host-grid-sort", ariaLabel: t("overview.card_sort_by"),
    options: COLUMNS.filter((column) => column.sortable).map((column) => ({ value: column.key, label: column.label })),
    value: sort.key,
  });
  select.addEventListener("change", () => onChange(select.value));
  const directionLabel = sort.direction === "asc" ? t("overview.card_sort_ascending") : t("overview.card_sort_descending");
  const reverse = el("button", {
    class: "cp-btn cp-btn-icon",
    attrs: { type: "button", "aria-label": directionLabel, title: directionLabel },
  });
  reverse.append(icon(sort.direction === "asc" ? "arrowUp" : "arrowDown"));
  reverse.addEventListener("click", () => onChange(sort.key));
  wrap.append(node, reverse);
  return wrap;
}

function buildColumnsDropdown(visibleColumns, onChange) {
  const trigger = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-btn cp-btn-secondary cp-btn-icon", attrs: { type: "button", "aria-label": t("overview.columns"), title: t("overview.columns") } })
  );
  trigger.append(icon("columns3"));

  createDropdown({
    trigger,
    align: "right",
    buildMenu: () => {
      const wrap = el("div");
      wrap.append(el("div", { class: "cp-dropdown-heading", text: t("overview.columns") }));
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
      const resetBtn = el("button", { class: "cp-dropdown-item", attrs: { type: "button" }, text: t("overview.show_all_columns") });
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
  const wrap = el("div", {
    class: "cp-table-wrap cp-systems-table-wrap",
    attrs: { tabindex: "0", role: "region", "aria-label": t("overview.systems_table_scroll_label") },
  });
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
  // No ResizeObserver: the table is rebuilt on every refresh. The page-scoped
  // resize listener re-syncs the current wrapper without adding rebuild leaks.
  const sync = () => syncTableOverflow(wrap);
  wrap.addEventListener("scroll", sync, { passive: true });
  wrap.addEventListener("pointerenter", sync);
  wrap.addEventListener("focus", sync);
  globalThis.requestAnimationFrame?.(sync);
  return wrap;
}

// syncTableOverflow marks the scroll region while columns remain hidden to
// its right, which drives the CSS edge fade.
export function syncTableOverflow(wrap) {
  const hiddenRight = wrap.scrollWidth - wrap.clientWidth - wrap.scrollLeft > 1;
  if (hiddenRight) wrap.setAttribute("data-overflow-right", "true");
  else wrap.removeAttribute("data-overflow-right");
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
  if (visible("containers")) tr.append(td(containersCell(row)));
  if (visible("cost")) tr.append(td(costCell(row.cost, state.displayCurrency)));
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
        text: t("overview.update_badge"),
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
      label: t("overview.usage_label"),
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
    wrap.append(el("span", { class: "cp-muted-small", text: t("overview.no_limit") }));
  } else {
    wrap.append(
      progressBar({
        value: bytes,
        max: limitBytes,
        label: isOut ? t("host.egress_outbound_bar_label") : t("host.egress_inbound_bar_label"),
        levelClass: levelClassForBar(level),
        valueText: "",
      }),
    );
  }
  return wrap;
}

function containersCell(row) {
  const wrap = el("div", { class: "cp-systems-containers-cell" });
  if (row.containers_running === undefined || row.containers_running === null) {
    wrap.append(el("span", { class: "cp-muted-small", text: "–" }));
    return wrap;
  }
  wrap.append(icon("box"));
  wrap.append(el("span", { class: "cp-tabular", text: formatNumber(row.containers_running) }));
  return wrap;
}

/**
 * costCell renders a host's combined cloud-billing + network-estimate
 * MTD cost (SPEC-v0.6 §1's optional "Est. cost" column) in the hub's
 * display currency (SPEC-v0.6 §3: USD, or KRW via the manual rate).
 * The "excl. tax" note lives in the column header tooltip, not each cell.
 * Dashes when the host has no cost data yet.
 * @param {Object|null|undefined} cost models.HostCost
 * @param {Object|null|undefined} displayCurrency models.DisplayCurrencySettings
 */
function costCell(cost, displayCurrency) {
  if (!cost) return el("span", { class: "cp-muted-small", text: "–" });
  return el("span", {
    class: "cp-tabular",
    text: formatDisplayAmount(Number(cost.total_mtd || 0), displayCurrency, { taxNote: false }),
    attrs: { title: t("overview.cost_cell_title") },
  });
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
      text.setAttribute("title", row.update?.command ? t("overview.agent_update_title_with_command", { label: info.label, command: row.update.command }) : info.label);
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
      el("span", { text: t("overview.mobile_metric_cpu", { value: formatPercent(row.latest.cpu_percent) }) }),
      el("span", { text: t("overview.mobile_metric_mem", { value: formatPercent(row.latest.mem_used_percent) }) }),
      el("span", { text: t("overview.mobile_metric_disk", { value: formatPercent(row.latest.disk_used_percent) }) }),
    );
    card.append(metrics);
  }
  card.append(el("p", { class: "cp-host-card-uptime", text: t("overview.last_seen", { relative: formatRelativeTimeFromUnixSeconds(row.last_seen, nowMs) }) }));
  return card;
}

function describeError(err) {
  if (err instanceof ApiError) return t("overview.api_error_prefix", { message: localizeApiError(err) });
  return t("overview.hub_unreachable_showing_last_known");
}

// Status chips are single-choice; provider chips toggle independently.
// Counts come from facetCounts so each chip shows what clicking it leaves.
export function buildFacetBar(rows, facets, onChange, fleet = rows) {
  const counts = facetCounts(rows, facets, fleet);
  const bar = el("div", { class: "cp-facet-bar", attrs: { role: "group", "aria-label": t("overview.facet_group_label") } });
  const chip = (key, label, count, pressed, onClick, title) => {
    const button = el("button", {
      class: `cp-facet-chip${pressed ? " cp-facet-chip-active" : ""}`,
      attrs: { type: "button", "aria-pressed": pressed ? "true" : "false", "data-facet": key, ...(title ? { title } : {}) },
    });
    button.append(el("span", { text: label }), el("span", { class: "cp-facet-count", text: String(count) }));
    button.addEventListener("click", onClick);
    return button;
  };
  const statusGroup = el("div", { class: "cp-facet-group", attrs: { role: "group", "aria-label": t("overview.facet_status_label") } });
  const statusLabels = {
    all: t("overview.facet_all"),
    online: t("overview.card_online"),
    offline: t("overview.card_offline"),
    attention: t("overview.facet_attention"),
  };
  for (const key of Object.keys(statusLabels)) {
    statusGroup.append(chip(`status:${key}`, statusLabels[key], counts.status[key], facets.status === key,
      () => onChange({ ...facets, status: key }), key === "attention" ? t("overview.facet_attention_title") : undefined));
  }
  bar.append(statusGroup);
  if (counts.providers.length > 1 || facets.providers.length > 0) {
    const providerGroup = el("div", { class: "cp-facet-group", attrs: { role: "group", "aria-label": t("overview.facet_provider_label") } });
    const providerLabels = { aws: "AWS", oci: "OCI", other: t("host.provider_other") };
    for (const { key, count } of counts.providers) {
      const pressed = facets.providers.includes(key);
      providerGroup.append(chip(`provider:${key}`, providerLabels[key], count, pressed, () => onChange({
        ...facets,
        providers: pressed ? facets.providers.filter((p) => p !== key) : [...facets.providers, key],
      })));
    }
    bar.append(providerGroup);
  }
  if (facetsActive(facets)) {
    const clear = el("button", { class: "cp-facet-clear", attrs: { type: "button", "data-facet": "clear" }, text: t("overview.facet_clear") });
    clear.addEventListener("click", () => onChange(normalizeFacets(null)));
    bar.append(clear);
  }
  return bar;
}
