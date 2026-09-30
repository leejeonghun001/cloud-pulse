// pages/costs.js — #/costs page (SPEC-v0.6 §1/§3): cloud provider
// billing cards (MTD/forecast/status, with the last-successful-fetch
// freshness note per SPEC §1 개선 a) plus a per-host cost table
// combining cloud billing and estimated network egress cost.
import { el, clearChildren, emptyState, errorBanner, providerBadge } from "../ui/components.js";
import { icon } from "../ui/icons.js";
import { showToast } from "../ui/toast.js";
import { formatDuration } from "../core/format.js";
import { getBilling, refreshBilling, ApiError, RateLimitError } from "../core/api.js";
import { formatUSD } from "../core/currency.js";
import { freshnessViewModel, lastKnownCostViewModel, hostCostDisplayAmounts } from "../core/billing-view.js";

/** localUtcOffsetMinutes returns the browser's current UTC offset in
 * minutes, matching billing-view.js's formatAbsoluteLocal sign
 * convention (positive east of UTC) — i.e. the negation of
 * Date.prototype.getTimezoneOffset(), which uses the opposite sign. */
function localUtcOffsetMinutes() {
  return -new Date().getTimezoneOffset();
}

/** PROVIDER_STATUS_LABELS maps a models.CloudBillingStatus to a short
 * display label shown on a provider card's status badge. */
const PROVIDER_STATUS_LABELS = {
  ok: "OK",
  not_installed: "Not installed",
  not_configured: "Not configured",
  auth_failed: "Auth failed",
  permission_denied: "Permission denied",
  error: "Error",
};

/** PROVIDER_NAMES maps a models.CloudBillingProvider to its display
 * name. */
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

  async function refresh() {
    clearChildren(container);
    const root = el("div", { class: "cp-costs-page" });
    root.append(el("h1", { class: "cp-page-title", text: "Costs" }));
    root.append(
      el("p", {
        class: "cp-page-subtitle",
        text: "Cloud provider billing and estimated network egress cost per host.",
      }),
    );

    let view;
    try {
      view = await getBilling(controller.signal);
    } catch (err) {
      if (err?.name === "AbortError") return;
      root.append(errorBanner(describeError(err)));
      container.append(root);
      return;
    }

    root.append(
      providerCardsSection({
        snapshots: view.snapshots || [],
        intervalSeconds: view.interval_seconds || 0,
        signal: controller.signal,
        announce,
        onRefreshed: refresh,
      }),
    );
    root.append(hostCostTable(view.hosts || [], view.display_currency));
    container.append(root);
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
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-costs-providers-heading" } });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h2", { id: "cp-costs-providers-heading", text: "Cloud billing" }));

  const refreshBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" } });
  refreshBtn.append(icon("refreshCw"), el("span", { text: "Refresh now" }));
  refreshBtn.addEventListener("click", async () => {
    refreshBtn.disabled = true;
    try {
      await refreshBilling(signal);
      announce?.("Billing refreshed.");
      await onRefreshed();
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (err instanceof RateLimitError) {
        showToast({
          message: `Refresh throttled — try again in ${formatDuration(err.retryAfterSeconds)}.`,
          variant: "error",
        });
        return;
      }
      showToast({ message: `Refresh failed: ${describeError(err)}`, variant: "error" });
    } finally {
      refreshBtn.disabled = false;
    }
  });
  head.append(refreshBtn);
  section.append(head);

  if (snapshots.length === 0) {
    section.append(
      emptyState({
        title: "Cloud billing not enabled",
        message: "Set CP_BILLING=auto and configure the aws/oci CLI to see provider cost estimates here.",
        iconName: "cloud",
      }),
    );
    return section;
  }

  const grid = el("div", { class: "cp-costs-provider-grid" });
  for (const snap of snapshots) {
    grid.append(providerCard(snap, intervalSeconds));
  }
  section.append(grid);
  return section;
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
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h3", { class: "cp-card-title", text: PROVIDER_NAMES[snap.provider] || snap.provider.toUpperCase() }));
  head.append(statusBadge(snap.status, snap.stale));
  card.append(head);

  if (snap.status === "ok") {
    card.append(costLine("MTD", snap.mtd_cost, snap.currency));
    card.append(costLine("Forecast", snap.forecast_cost, snap.currency));
    if (snap.forecast_method === "linear") {
      card.append(el("p", { class: "cp-muted-small", text: "Forecast: linear projection (provider forecast unavailable)." }));
    }
    if (snap.account_level) {
      card.append(el("p", { class: "cp-muted-small", text: "Account total (resource-level billing not enabled)." }));
    }
  } else {
    card.append(el("p", { class: "cp-muted-small", text: snap.status_detail || "Not connected." }));
    const lastKnown = lastKnownCostViewModel(snap);
    if (lastKnown) {
      card.append(el("p", { class: "cp-muted-small cp-costs-last-known", text: lastKnown.mtdLabel }));
      card.append(el("p", { class: "cp-muted-small cp-costs-last-known", text: lastKnown.forecastLabel }));
    }
  }

  card.append(freshnessNote(snap, intervalSeconds));
  card.append(el("p", { class: "cp-muted-small", text: "Excl. tax." }));
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
    wrap.append(el("span", { class: "cp-chip cp-chip-warning", text: "Stale data", attrs: { title: "Last successful fetch is more than 2× the poll interval old" } }));
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
  const row = el("div", { class: "cp-metric-head" });
  row.append(el("span", { class: "cp-metric-label", text: label }));
  row.append(el("span", { class: "cp-metric-value", text: formatUSD(amount, currency, { taxNote: false }) }));
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
function hostCostTable(hosts, displayCurrency) {
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-costs-hosts-heading" } });
  section.append(el("h2", { id: "cp-costs-hosts-heading", text: "Per-host costs" }));

  if (hosts.length === 0) {
    section.append(el("p", { class: "cp-muted", text: "No hosts reporting yet." }));
    return section;
  }

  const table = el("table", { class: "cp-table" });
  const thead = el("thead");
  const headRow = el("tr");
  for (const label of ["Host", "Provider", "Cloud MTD / forecast", "Network estimate", "Total (MTD / forecast)"]) {
    headRow.append(el("th", { text: label }));
  }
  thead.append(headRow);
  table.append(thead);

  const tbody = el("tbody");
  for (const h of hosts) {
    tbody.append(hostCostRow(h, displayCurrency));
  }
  table.append(tbody);
  const tableWrap = el("div", { class: "cp-table-wrap" });
  tableWrap.append(table);
  section.append(tableWrap);
  section.append(el("p", { class: "cp-muted-small", text: "All amounts excl. tax." }));
  return section;
}

function hostCostRow(h, displayCurrency) {
  const row = el("tr");
  row.append(el("td", { children: [el("a", { text: h.hostname, attrs: { href: `#/host/${encodeURIComponent(h.host_id)}` } })] }));
  row.append(el("td", { children: h.provider ? [providerBadge(h.provider)] : [el("span", { class: "cp-muted-small", text: "—" })] }));

  const amounts = hostCostDisplayAmounts(h, displayCurrency);

  let cloudCell;
  if (!h.provider) {
    cloudCell = el("span", { class: "cp-muted-small", text: "Not connected" });
  } else if (h.matched) {
    cloudCell = el("span", { text: `${amounts.cloudMTD} / ${amounts.cloudForecast}` });
  } else {
    cloudCell = el("span", { class: "cp-muted-small", text: "Account total only" });
  }
  row.append(el("td", { children: [cloudCell] }));

  row.append(
    el("td", {
      text: amounts.networkMTD ? `${amounts.networkMTD} / ${amounts.networkForecast}` : "No plan",
    }),
  );
  row.append(el("td", { text: `${amounts.totalMTD} / ${amounts.totalForecast}` }));
  return row;
}

function describeError(err) {
  if (err instanceof ApiError) return err.message;
  return String(err?.message || err);
}
