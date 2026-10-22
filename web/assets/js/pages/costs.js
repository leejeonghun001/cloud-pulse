// pages/costs.js — #/costs page (SPEC-v0.6 §1/§3): cloud provider
// billing cards (MTD/forecast/status, with the last-successful-fetch
// freshness note per SPEC §1 개선 a) plus a per-host cost table
// combining cloud billing and estimated network egress cost.
import { el, clearChildren, emptyState, errorBanner, errorState, providerBadge, skeletonCards } from "../ui/components.js";
import { icon } from "../ui/icons.js";
import { showToast } from "../ui/toast.js";
import { formatDuration } from "../core/format.js";
import { getBilling, refreshBilling, ApiError, RateLimitError, localizeApiError } from "../core/api.js";
import { formatUSD } from "../core/currency.js";
import { freshnessViewModel, hostCostDisplayAmounts, providerStatusHint, accountTitle, spendSummary } from "../core/billing-view.js";
import { disclosure } from "../ui/disclosure.js";
import { t } from "../core/i18n.js";

/** localUtcOffsetMinutes returns the browser's current UTC offset in
 * minutes, matching billing-view.js's formatAbsoluteLocal sign
 * convention (positive east of UTC) — i.e. the negation of
 * Date.prototype.getTimezoneOffset(), which uses the opposite sign. */
function localUtcOffsetMinutes() {
  return -new Date().getTimezoneOffset();
}

/** PROVIDER_STATUS_LABELS maps a models.CloudBillingStatus to a short
 * display label shown on a provider card's status badge. Getters so
 * each label re-resolves against the current language on every read. */
const PROVIDER_STATUS_LABELS = {
  get ok() { return t("costs.status_ok"); },
  get not_installed() { return t("costs.status_not_installed"); },
  get not_configured() { return t("costs.status_not_configured"); },
  get auth_failed() { return t("costs.status_auth_failed"); },
  get permission_denied() { return t("costs.status_permission_denied"); },
  get error() { return t("costs.status_error"); },
};

/** PROVIDER_NAMES maps a models.CloudBillingProvider to its display
 * name (product/API names, never translated). */
const PROVIDER_NAMES = { aws: "AWS", oci: "OCI" };

/**
 * mountCostsPage renders the Costs page into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountCostsPage(container, { announce }) {
  const controller = new AbortController();
  let generation = 0;

  /** @type {HTMLElement|null} */
  let content = null;

  // The first load shows a skeleton; later (auto-)refreshes fetch first
  // and swap the content in place, so the page neither flickers nor
  // loses scroll/focus every poll, and a failed background refresh
  // keeps the last good figures on screen.
  async function refresh() {
    const current = ++generation;
    const firstLoad = !content || !content.isConnected;
    if (firstLoad) {
      clearChildren(container);
      content = buildShell();
    }
    const target = content;

    let view;
    try {
      view = await getBilling(controller.signal);
    } catch (err) {
      if (err?.name === "AbortError" || controller.signal.aborted || current !== generation) return;
      if (!firstLoad) return;
      clearChildren(target);
      target.setAttribute("aria-busy", "false");
      target.append(errorState({
        title: t("costs.page_title"),
        message: t("costs.load_failed", { reason: describeError(err) }),
        onRetry: refresh,
      }));
      return;
    }
    if (controller.signal.aborted || current !== generation) return;
    const active = document.activeElement;
    const focusedID = active && target.contains?.(active) ? active.id : "";
    clearChildren(target);
    target.setAttribute("aria-busy", "false");

    target.append(
      providerCardsSection({
        snapshots: view.snapshots || [],
        intervalSeconds: view.interval_seconds || 0,
        signal: controller.signal,
        announce,
        onRefreshed: refresh,
      }),
    );
    target.append(hostCostTable(view.hosts || [], view.display_currency, view.snapshots || []));
    if (focusedID) document.getElementById(focusedID)?.focus();
  }

  /** buildShell renders the page header and a skeleton content area. */
  function buildShell() {
    const root = el("div", { class: "cp-costs-page" });
    const header = el("header", { class: "cp-costs-header" });
    header.append(el("h1", { class: "cp-page-title", text: t("costs.page_title") }));
    header.append(
      el("p", {
        class: "cp-page-subtitle",
        text: t("costs.page_subtitle"),
      }),
    );
    root.append(header);
    const content = el("div", { class: "cp-costs-content", attrs: { "aria-busy": "true" } });
    content.append(skeletonCards(2), el("span", { class: "sr-only", attrs: { role: "status" }, text: t("costs.loading") }));
    root.append(content);
    container.append(root);
    return content;
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

/**
 * providerCardsSection builds the "Now provider" cards row plus the
 * "Refresh now" button. Shown even with zero snapshots (billing
 * disabled) as an explanatory empty state.
 */
function providerCardsSection({ snapshots, intervalSeconds, signal, announce, onRefreshed }) {
  const section = el("section", { class: "cp-section cp-costs-providers", attrs: { "aria-labelledby": "cp-costs-providers-heading" } });
  const head = el("div", { class: "cp-costs-section-head" });
  const title = el("div");
  title.append(el("h2", { attrs: { id: "cp-costs-providers-heading" }, text: t("costs.cloud_billing_title") }));
  title.append(el("p", { class: "cp-muted-small", text: t("costs.provider_currency_note") }));
  head.append(title);

  const refreshBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" } });
  refreshBtn.append(icon("refreshCw"), el("span", { text: t("costs.refresh_now") }));
  refreshBtn.addEventListener("click", async () => {
    refreshBtn.disabled = true;
    try {
      const result = await refreshBilling(signal);
      const agents = result?.agent_refresh_requested || [];
      if (agents.length > 0) {
        // Agent-collected providers answer on the agent's next report;
        // the page's own 15s refresh picks the new figures up.
        const message = t("costs.agent_refresh_requested", { hosts: agents.join(", ") });
        showToast({ message });
        announce?.(message);
      } else {
        announce?.(t("costs.billing_refreshed"));
      }
      await onRefreshed();
      document.getElementById("cp-costs-refresh")?.focus();
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (err instanceof RateLimitError) {
        showToast({
          message: t("costs.refresh_throttled", { duration: formatDuration(err.retryAfterSeconds) }),
          variant: "error",
        });
        return;
      }
      showToast({ message: t("costs.refresh_failed", { reason: describeError(err) }), variant: "error" });
    } finally {
      refreshBtn.disabled = false;
    }
  });
  refreshBtn.setAttribute("id", "cp-costs-refresh");
  head.append(refreshBtn);
  section.append(head);

  if (snapshots.length === 0) {
    section.append(
      emptyState({
        title: t("costs.billing_not_enabled_title"),
        message: t("costs.billing_not_enabled_message"),
        iconName: "cloud",
      }),
    );
    section.append(el("a", { class: "cp-btn cp-btn-secondary", text: t("costs.billing_settings"), attrs: { href: "#/settings/billing" } }));
    return section;
  }

  // One collapsible group per provider, its line summing the provider's
  // accounts per currency, so a closed group still says what it costs.
  const setupRows = el("div", { class: "cp-costs-provider-grid" });
  const groups = new Map();
  for (const snap of snapshots) {
    if (isProviderUnconfigured(snap)) {
      // "Not set up" only when no agent reports this provider either.
      if (!snapshots.some((other) => other.provider === snap.provider && other !== snap)) setupRows.append(providerSetupRow(snap));
      continue;
    }
    if (!groups.has(snap.provider)) groups.set(snap.provider, []);
    groups.get(snap.provider).push(snap);
  }
  for (const [provider, accounts] of groups) {
    const grid = el("div", { class: "cp-costs-provider-grid" });
    for (const snap of accounts) grid.append(providerCard(snap, intervalSeconds));
    section.append(disclosure({
      id: `costs-provider-${provider}`,
      open: true,
      summary: providerGroupSummary(provider, accounts),
      children: [grid],
      className: "cp-costs-provider-group",
    }).node);
  }
  if (setupRows.children.length > 0) section.append(setupRows);
  return section;
}

/** providerGroupSummary: "OCI · 2 accounts · 31.00 SGD this month
 * (forecast 47.26 SGD)", per currency, flagging accounts that fail. */
function providerGroupSummary(provider, accounts) {
  const name = PROVIDER_NAMES[provider] || provider.toUpperCase();
  const { totals, attention } = spendSummary(accounts);
  const money = totals.map((row) => t("costs.group_amounts", {
    mtd: formatUSD(row.mtd, row.currency, { taxNote: false }),
    forecast: formatUSD(row.forecast, row.currency, { taxNote: false }),
  })).join(" · ");
  const parts = [name, t("costs.group_accounts", { count: accounts.length })];
  if (money) parts.push(money);
  if (attention.length > 0) parts.push(t("costs.group_attention", { count: attention.length }));
  return parts.join(" · ");
}

/** Only the hub's own, never-set-up account is "not set up"; an agent's
 * account in that state gets a full card with its fix. */
function isProviderUnconfigured(snap) {
  return !snap.account_id && !snap.source_host && (snap.status === "not_installed" || snap.status === "not_configured");
}

/** A missing optional billing integration is setup guidance, not an error card. */
function providerSetupRow(snap) {
  const provider = PROVIDER_NAMES[snap.provider] || snap.provider.toUpperCase();
  const row = el("div", { class: "cp-costs-provider-setup" });
  row.append(el("span", { class: "cp-muted-small", text: t("costs.provider_not_setup", { provider }) }));
  row.append(el("a", { class: "cp-back-link", attrs: { href: "#/settings/billing" }, text: t("costs.how_to_enable") }));
  return row;
}

/**
 * providerCard builds one provider's status card: MTD/forecast, status
 * badge, and — for a quiet-skip status with a prior success, or a
 * stale successful one — the freshness note per SPEC §1 개선 a.
 * @param {Object} snap a models.CloudCostSnapshot JSON object
 * @param {number} intervalSeconds the current billing poll interval
 * @returns {HTMLElement}
 */
function providerCard(snap, intervalSeconds) {
  const card = el("div", { class: "cp-card cp-costs-provider-card" });
  const head = el("div", { class: "cp-costs-section-head" });
  head.append(el("h3", { class: "cp-card-title", text: accountTitle(snap) }));
  head.append(statusBadge(snap.status, snap.stale));
  card.append(head);

  const notes = el("div", { class: "cp-costs-provider-notes-body" });
  if (snap.status === "ok" || snap.last_success_at > 0) {
    if (snap.status !== "ok") {
      card.append(el("p", { class: "cp-muted-small", text: snap.status_detail || t("costs.not_connected") }));
      card.append(el("p", { class: "cp-costs-last-known cp-muted-small", text: t("costs.last_known_note") }));
    }
    const metrics = el("dl", { class: "cp-costs-provider-metrics" });
    metrics.append(costLine(t("costs.mtd_actual"), snap.mtd_cost, snap.currency));
    metrics.append(costLine(t("costs.month_end_forecast"), snap.forecast_cost, snap.currency));
    card.append(metrics);
    if (snap.forecast_method === "linear") {
      notes.append(el("p", { class: "cp-muted-small", text: t("costs.forecast_linear_note") }));
    }
    if (snap.account_level) {
      notes.append(el("p", { class: "cp-muted-small", text: t("costs.account_level_note") }));
    }
  } else {
    card.append(el("p", { class: "cp-muted-small", text: snap.status_detail || t("costs.not_connected") }));
    card.append(el("p", { class: "cp-muted-small", text: t("costs.amounts_unavailable") }));
  }

  const hint = providerStatusHint(snap);
  if (hint) card.append(el("p", { class: "cp-costs-status-hint", text: t(hint.key, hint.params) }));

  if (snap.source_host) {
    const key = snap.provider === "aws" ? "costs.collected_by_agent_aws" : "costs.collected_by_agent";
    notes.append(el("p", { class: "cp-muted-small", text: t(key, { host: snap.source_host }) }));
  } else if (snap.provider === "oci" && snap.status !== "ok") {
    notes.append(el("a", { class: "cp-back-link", attrs: { href: "#/settings/billing" }, text: t("costs.oci_agent_link") }));
  }
  notes.append(el("p", { class: "cp-muted-small", text: t("costs.excl_tax") }));
  // Freshness stays in view; the method/source/tax notes are one click
  // away so the card reads as its two numbers first.
  const footer = el("div", { class: "cp-costs-provider-notes" });
  footer.append(freshnessNote(snap, intervalSeconds));
  footer.append(disclosure({ summary: t("costs.card_details"), children: [notes], className: "cp-costs-card-details" }).node);
  card.append(footer);
  return card;
}

/**
 * statusBadge builds the small status chip for a provider card,
 * appending a "stale" badge alongside it when applicable.
 */
function statusBadge(status, stale) {
  const wrap = el("span", { class: "cp-costs-status-group" });
  const cls = status === "ok" ? "cp-chip-ok" : status === "auth_failed" || status === "permission_denied" || status === "error" ? "cp-chip-critical" : "cp-chip-other";
  wrap.append(el("span", { class: `cp-chip ${cls}`, text: PROVIDER_STATUS_LABELS[status] || status }));
  if (stale) {
    wrap.append(el("span", { class: "cp-chip cp-chip-warning", text: t("costs.stale_data"), attrs: { title: t("costs.stale_data_title") } }));
  }
  return wrap;
}

/**
 * freshnessNote builds the "last success N ago" line (SPEC §1 개선 a):
 * shows the absolute last-success time (local, YYYY-MM-DD HH:mm) and
 * elapsed duration, or "No successful fetch yet." if LastSuccessAt is
 * 0. Delegates the actual decision/formatting to the pure
 * core/billing-view.js helper so it's covered by node tests with fixed
 * clocks/offsets.
 */
function freshnessNote(snap, intervalSeconds) {
  const vm = freshnessViewModel({
    status: snap.status,
    lastSuccessAt: snap.last_success_at || 0,
    nowUnixSeconds: Math.floor(Date.now() / 1000),
    intervalSeconds,
    utcOffsetMinutes: localUtcOffsetMinutes(),
  });
  return el("p", { class: "cp-muted-small", text: vm.text });
}

/** costLine builds a labeled monetary value row for a provider card. */
function costLine(label, amount, currency) {
  const row = el("div", { class: "cp-costs-provider-metric" });
  row.append(el("dt", { class: "cp-muted-small", text: label }));
  row.append(el("dd", { class: "cp-costs-provider-value", text: hasAmount(amount) ? formatUSD(amount, currency, { taxNote: false }) : t("costs.unavailable") }));
  return row;
}

/**
 * hostCostTable builds the per-host cost table: cloud billing (if
 * matched), estimated network cost, and the combined total, converted
 * to the hub's display currency. The "(excl. tax)" note is shown once
 * in the section footer rather than repeated on every cell (SPEC-v0.6
 * §2).
 * @param {Array} hosts models.HostCost[] from GET /api/v1/billing
 * @param {Object} displayCurrency models.DisplayCurrencySettings
 * @returns {HTMLElement}
 */
function hostCostTable(hosts, displayCurrency, snapshots) {
  const section = el("section", { class: "cp-section cp-costs-hosts", attrs: { "aria-labelledby": "cp-costs-hosts-heading" } });
  section.append(el("h2", { attrs: { id: "cp-costs-hosts-heading" }, text: t("costs.per_host_costs_title") }));
  section.append(el("p", { class: "cp-muted-small", text: t("costs.host_costs_note") }));
  if (displayCurrency?.currency === "KRW") {
    section.append(el("p", { class: "cp-muted-small", text: t("costs.manual_conversion_note") }));
  }

  if (hosts.length === 0) {
    section.append(el("p", { class: "cp-muted", text: t("costs.no_hosts_reporting_yet") }));
    return section;
  }

  const table = el("table", { class: "cp-table cp-costs-table", attrs: { role: "table", "aria-labelledby": "cp-costs-hosts-heading" } });
  const thead = el("thead", { attrs: { role: "rowgroup" } });
  const headRow = el("tr", { attrs: { role: "row" } });
  for (const label of [t("costs.col_host"), t("costs.col_cloud_charges"), t("costs.col_network_estimate"), t("costs.col_cost_projection")]) {
    headRow.append(el("th", { text: label, attrs: { scope: "col", role: "columnheader" } }));
  }
  thead.append(headRow);
  table.append(thead);

  const tbody = el("tbody", { attrs: { role: "rowgroup" } });
  for (const h of hosts) {
    tbody.append(hostCostRow(h, displayCurrency, snapshots.find((s) => s.provider === h.provider)));
  }
  table.append(tbody);
  const tableWrap = el("div", { class: "cp-table-wrap cp-costs-table-wrap" });
  tableWrap.append(table);
  section.append(tableWrap);
  section.append(el("p", { class: "cp-muted-small", text: t("costs.all_amounts_excl_tax") }));
  return section;
}

function hostCostRow(h, displayCurrency, snapshot) {
  const row = el("tr", { attrs: { role: "row" } });
  const host = el("th", { class: "cp-costs-host-cell", attrs: { scope: "row", role: "rowheader" } });
  host.append(el("a", { class: "cp-table-link", text: h.hostname, attrs: { href: `#/host/${encodeURIComponent(h.host_id)}` } }));
  if (h.provider) host.append(providerBadge(h.provider));
  row.append(host);

  const amounts = hostCostDisplayAmounts(h, displayCurrency);

  const cloudCell = costCell(t("costs.col_cloud_charges"));
  if (!h.provider) {
    cloudCell.append(el("span", { class: "cp-muted-small", text: t("costs.not_connected") }));
  } else if (h.matched || snapshot?.account_level) {
    if (!h.matched) cloudCell.append(el("span", { class: "cp-muted-small cp-costs-scope-note", text: t("costs.account_total_only") }));
    if (snapshot?.status !== "ok" && !snapshot?.last_success_at && !h.matched) {
      cloudCell.append(el("span", { class: "cp-muted-small", text: t("costs.amounts_unavailable") }));
    } else {
      cloudCell.append(amountPair(h.cloud_mtd, amounts.cloudMTD, h.cloud_forecast, amounts.cloudForecast));
    }
    if (snapshot?.stale) cloudCell.append(el("p", { class: "cp-muted-small cp-costs-source-note", text: t("costs.stale_data") }));
    if (snapshot && snapshot.status !== "ok") cloudCell.append(el("p", { class: "cp-muted-small cp-costs-source-note", text: t("costs.last_known_note") }));
  } else {
    cloudCell.append(el("span", { class: "cp-muted-small", text: t("costs.resource_unavailable") }));
  }
  row.append(cloudCell);
  const network = costCell(t("costs.col_network_estimate"));
  if (amounts.networkMTD !== null) network.append(amountPair(h.network_estimate.mtd, amounts.networkMTD, h.network_estimate.projected, amounts.networkForecast));
  else network.append(el("span", { class: "cp-muted-small", text: t("costs.no_plan") }));
  row.append(network);
  const total = costCell(t("costs.col_cost_projection"), "cp-costs-total-cell");
  total.append(amountPair(h.total_mtd, amounts.totalMTD, h.total_forecast, amounts.totalForecast));
  if (!h.matched && (snapshot?.account_level || h.cloud_mtd !== 0 && hasAmount(h.cloud_mtd))) {
    total.append(el("span", { class: "cp-muted-small cp-costs-scope-note", text: t("costs.account_projection_note") }));
  } else if (!h.matched && !h.network_estimate?.plan_id) {
    total.append(el("span", { class: "cp-muted-small cp-costs-scope-note", text: t("costs.incomplete_total_note") }));
  }
  if (snapshot?.stale) total.append(el("p", { class: "cp-muted-small cp-costs-source-note", text: t("costs.stale_data") }));
  row.append(total);
  return row;
}

function hasAmount(amount) {
  return amount !== null && amount !== undefined && Number.isFinite(Number(amount));
}

function costCell(label, extraClass = "") {
  const cell = el("td", { class: extraClass, attrs: { role: "cell" } });
  cell.append(el("span", { class: "cp-costs-mobile-label", text: label, attrs: { "aria-hidden": "true" } }));
  return cell;
}

/** Keep the existing formatter's amount, USD original, manual rate and date
 * exactly intact; only separate its primary value from its supporting text. */
function amountPair(mtd, mtdText, forecast, forecastText) {
  const pair = el("dl", { class: "cp-costs-amount-pair" });
  for (const [label, raw, formatted] of [[t("costs.mtd"), mtd, mtdText], [t("costs.forecast"), forecast, forecastText]]) {
    const item = el("div", { class: "cp-costs-amount-item" });
    item.append(el("dt", { text: label }));
    const value = el("dd");
    const text = hasAmount(raw) ? formatted : t("costs.unavailable");
    const detailAt = text.indexOf(" (");
    value.append(el("span", { class: "cp-costs-amount", text: detailAt < 0 ? text : text.slice(0, detailAt) }));
    if (detailAt >= 0) value.append(el("span", { class: "cp-costs-conversion", text: text.slice(detailAt) }));
    item.append(value);
    pair.append(item);
  }
  return pair;
}

function describeError(err) {
  if (err instanceof ApiError) return localizeApiError(err);
  return String(err?.message || err);
}
