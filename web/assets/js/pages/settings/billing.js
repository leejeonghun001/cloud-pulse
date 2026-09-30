// settings/billing.js — Settings → Billing section (SPEC-v0.6 §1/§3):
// provider status, polling interval selection, pricing plan editor,
// display currency, and audit log.
import { el, clearChildren, errorBanner, providerBadge } from "../../ui/components.js";
import { icon } from "../../ui/icons.js";
import { showToast } from "../../ui/toast.js";
import { selectField } from "../../ui/select.js";
import { createDialog, buildDialogActions } from "../../ui/dialog.js";
import { formatUSD } from "../../core/currency.js";
import { formatRelativeTimeFromUnixSeconds } from "../../core/format.js";
import { freshnessViewModel } from "../../core/billing-view.js";
import {
  getBilling,
  setBillingInterval,
  getPricingPlans,
  createPricingPlan,
  updatePricingPlan,
  deletePricingPlan,
  getDisplayCurrency,
  setDisplayCurrency,
  getAuditEntries,
  ApiError,
} from "../../core/api.js";

/** INTERVAL_API_COST_HINTS shows the AWS Cost Explorer API cost
 * estimate for each poll interval (SPEC-v0.6 §1: 24h ≈ $0.6/mo, 12h ≈
 * $1.2/mo, 6h ≈ $2.4/mo). */
const INTERVAL_API_COST_HINTS = {
  "6h": "6 hours (~$2.4/mo AWS Cost Explorer API cost)",
  "12h": "12 hours (~$1.2/mo AWS Cost Explorer API cost)",
  "24h": "24 hours (~$0.6/mo AWS Cost Explorer API cost, default)",
};

/**
 * mountBillingSection renders the Billing section into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountBillingSection(container, { announce }) {
  const controller = new AbortController();

  async function refresh() {
    clearChildren(container);

    let billing;
    let plans;
    let currency;
    try {
      [billing, plans, currency] = await Promise.all([
        getBilling(controller.signal),
        getPricingPlans(controller.signal),
        getDisplayCurrency(controller.signal),
      ]);
    } catch (err) {
      if (err?.name === "AbortError") return;
      container.append(errorBanner(describeError(err)));
      return;
    }

    container.append(intervalCard({ billing, signal: controller.signal, announce, onChanged: refresh }));
    container.append(currencyCard({ currency, signal: controller.signal, announce, onChanged: refresh }));
    container.append(plansCard({ plans, signal: controller.signal, announce, onChanged: refresh }));
    container.append(auditLogCard({ signal: controller.signal }));
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

/** intervalCard builds the provider status + polling interval card. */
function intervalCard({ billing, signal, announce, onChanged }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: "Cloud billing polling" }));

  const currentInterval = secondsToInterval(billing.interval_seconds);
  const { node: intervalNode, select: intervalSelect } = selectField({
    id: "cp-billing-interval",
    label: "Polling interval",
    options: Object.entries(INTERVAL_API_COST_HINTS).map(([value, label]) => ({ value, label })),
    value: currentInterval,
  });
  card.append(intervalNode);

  const status = el("p", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  const saveBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Save interval" });
  saveBtn.addEventListener("click", async () => {
    saveBtn.disabled = true;
    status.textContent = "Saving…";
    try {
      await setBillingInterval(intervalSelect.value, signal);
      status.textContent = "Saved. Takes effect on the next scheduled poll.";
      announce?.(`Billing polling interval set to ${intervalSelect.value}.`);
      await onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = `Save failed: ${describeError(err)}`;
    } finally {
      saveBtn.disabled = false;
    }
  });
  card.append(saveBtn, status);

  if ((billing.snapshots || []).length > 0) {
    const list = el("ul", { class: "cp-billing-provider-status-list", attrs: { role: "list" } });
    for (const snap of billing.snapshots) {
      const item = el("li", { class: "cp-billing-provider-status-row" });
      item.append(el("strong", { text: snap.provider.toUpperCase() }));
      item.append(el("span", { class: "cp-chip", text: snap.status }));
      if (snap.stale) {
        item.append(el("span", { class: "cp-chip cp-chip-warning", text: "Stale data" }));
      }
      const vm = freshnessViewModel({
        status: snap.status,
        lastSuccessAt: snap.last_success_at || 0,
        nowUnixSeconds: Math.floor(Date.now() / 1000),
        intervalSeconds: billing.interval_seconds || 0,
        utcOffsetMinutes: -new Date().getTimezoneOffset(),
      });
      item.append(el("span", { class: "cp-muted-small", text: vm.text }));
      list.append(item);
    }
    card.append(list);
  } else {
    card.append(el("p", { class: "cp-muted-small", text: "Billing is disabled on this hub (CP_BILLING=off, or no snapshots yet)." }));
  }

  return card;
}

/** currencyCard builds the display currency settings card. */
function currencyCard({ currency, signal, announce, onChanged }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: "Display currency" }));
  card.append(el("p", { class: "cp-muted-small", text: "All amounts are stored and calculated in USD. KRW is a display-only conversion using a manually entered exchange rate — no external rate lookup is ever performed." }));

  const { node: currencyNode, select: currencySelect } = selectField({
    id: "cp-billing-currency",
    label: "Currency",
    options: [
      { value: "USD", label: "USD" },
      { value: "KRW", label: "KRW (manual rate)" },
    ],
    value: currency.currency || "USD",
  });
  card.append(currencyNode);

  const rateField = el("div", { class: "cp-field" });
  rateField.append(el("label", { class: "cp-label", attrs: { for: "cp-billing-krw-rate" }, text: "KRW per 1 USD" }));
  const rateInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input",
      attrs: { id: "cp-billing-krw-rate", type: "text", inputmode: "decimal", value: currency.krw_per_usd ? String(currency.krw_per_usd) : "" },
    })
  );
  rateField.append(rateInput);
  card.append(rateField);

  function syncRateVisibility() {
    rateField.hidden = currencySelect.value !== "KRW";
  }
  syncRateVisibility();
  currencySelect.addEventListener("change", syncRateVisibility);

  if (currency.rate_updated_at) {
    card.append(
      el("p", {
        class: "cp-muted-small",
        text: `Rate last entered ${new Date(currency.rate_updated_at * 1000).toISOString().slice(0, 10)}.`,
      }),
    );
  }

  const status = el("p", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  const saveBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Save currency" });
  saveBtn.addEventListener("click", async () => {
    const rate = Number(rateInput.value);
    if (currencySelect.value === "KRW" && (!Number.isFinite(rate) || rate <= 0)) {
      status.textContent = "Enter a positive exchange rate to use KRW.";
      return;
    }
    saveBtn.disabled = true;
    status.textContent = "Saving…";
    try {
      await setDisplayCurrency(currencySelect.value, rate, signal);
      status.textContent = "Saved.";
      announce?.(`Display currency set to ${currencySelect.value}.`);
      await onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (err instanceof ApiError && err.code === "rate_required") {
        status.textContent = "KRW requires a positive exchange rate.";
      } else {
        status.textContent = `Save failed: ${describeError(err)}`;
      }
    } finally {
      saveBtn.disabled = false;
    }
  });
  card.append(saveBtn, status);
  return card;
}

/** plansCard builds the pricing plan list + editor dialog trigger. */
function plansCard({ plans, signal, announce, onChanged }) {
  const card = el("div", { class: "cp-card" });
  const head = el("div", { class: "cp-metric-head" });
  head.append(el("h2", { class: "cp-card-title", text: "Pricing plans" }));
  const addBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" } });
  addBtn.append(icon("plus"), el("span", { text: "Add plan" }));
  addBtn.addEventListener("click", () => openPlanDialog({ signal, announce, onChanged }));
  head.append(addBtn);
  card.append(head);

  const list = el("ul", { class: "cp-billing-plan-list", attrs: { role: "list" } });
  for (const plan of plans) {
    list.append(planRow({ plan, signal, announce, onChanged }));
  }
  card.append(list);
  return card;
}

/**
 * pricingPlanProviderFamily maps a pricing plan's free-form provider
 * string (e.g. "aws", "oci-na-eu", "oci-apac", "other" — see
 * models.PricingPlan.Provider's doc comment; these are NOT the same
 * vocabulary as a host's models.Provider) down to the coarse
 * "aws"|"oci"|"other" family providerBadge() actually recognizes, so a
 * region-specific OCI plan still shows an "OCI" badge instead of
 * falling through to the generic "Other" style.
 * @param {string} planProvider
 * @returns {"aws"|"oci"|"other"}
 */
function pricingPlanProviderFamily(planProvider) {
  if (planProvider === "aws") return "aws";
  if (planProvider === "oci-na-eu" || planProvider === "oci-apac" || planProvider === "oci") return "oci";
  return "other";
}

function planRow({ plan, signal, announce, onChanged }) {
  const row = el("li", { class: "cp-billing-plan-row" });
  row.append(providerBadge(pricingPlanProviderFamily(plan.provider)));
  row.append(el("span", { class: "cp-billing-plan-row-name", text: plan.name }));
  row.append(el("span", { class: "cp-muted-small", text: `Free: ${plan.egress_free_gb} GB${plan.pool_free_tier ? " (pooled)" : ""}` }));
  if (plan.builtin) {
    row.append(el("span", { class: "cp-chip cp-chip-other", text: "Built-in" }));
  } else {
    const editBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm cp-btn-icon", attrs: { type: "button", "aria-label": `Edit ${plan.name}` } });
    editBtn.append(icon("pencil"));
    editBtn.addEventListener("click", () => openPlanDialog({ plan, signal, announce, onChanged }));

    const deleteBtn = el("button", { class: "cp-btn cp-btn-danger cp-btn-sm cp-btn-icon", attrs: { type: "button", "aria-label": `Delete ${plan.name}` } });
    deleteBtn.append(icon("trash"));
    deleteBtn.addEventListener("click", async () => {
      if (!globalThis.confirm(`Delete pricing plan "${plan.name}"?`)) return;
      try {
        await deletePricingPlan(plan.id, signal);
        announce?.(`Deleted plan ${plan.name}.`);
        await onChanged();
      } catch (err) {
        if (err?.name === "AbortError") return;
        showToast({ message: `Delete failed: ${describeError(err)}`, variant: "error" });
      }
    });
    row.append(editBtn, deleteBtn);
  }
  return row;
}

/**
 * openPlanDialog opens the Add/Edit pricing plan dialog: free tier
 * allowance, a tier table (add/remove rows), inbound price, pooling
 * toggle, and a live "1 TB egress costs $X" preview.
 */
function openPlanDialog({ plan, signal, announce, onChanged }) {
  const isEdit = Boolean(plan);
  const { dialog, body, open, close } = createDialog({
    titleId: "cp-plan-dialog-title",
    title: isEdit ? `Edit plan: ${plan.name}` : "Add pricing plan",
    wide: true,
  });

  const draft = {
    name: plan?.name || "",
    provider: plan?.provider || "other",
    egressFreeGB: plan?.egress_free_gb ?? 0,
    egressTiers: (plan?.egress_tiers || [{ up_to_gb: 0, price_per_gb: 0 }]).map((t) => ({ ...t })),
    ingressPricePerGB: plan?.ingress_price_per_gb ?? 0,
    poolFreeTier: plan?.pool_free_tier ?? false,
  };

  const nameField = el("div", { class: "cp-field" });
  nameField.append(el("label", { class: "cp-label", attrs: { for: "cp-plan-name" }, text: "Name" }));
  const nameInput = /** @type {HTMLInputElement} */ (el("input", { class: "cp-input", attrs: { id: "cp-plan-name", type: "text", value: draft.name } }));
  nameInput.addEventListener("input", () => (draft.name = nameInput.value));
  nameField.append(nameInput);
  body.append(nameField);

  const freeField = el("div", { class: "cp-field" });
  freeField.append(el("label", { class: "cp-label", attrs: { for: "cp-plan-free" }, text: "Free egress allowance (GB)" }));
  const freeInput = /** @type {HTMLInputElement} */ (el("input", { class: "cp-input", attrs: { id: "cp-plan-free", type: "text", inputmode: "decimal", value: String(draft.egressFreeGB) } }));
  freeInput.addEventListener("input", () => {
    draft.egressFreeGB = Number(freeInput.value) || 0;
    renderPreview();
  });
  freeField.append(freeInput);
  body.append(freeField);

  const tiersHost = el("div", { class: "cp-billing-tiers-editor" });
  body.append(tiersHost);

  function renderTiers() {
    clearChildren(tiersHost);
    tiersHost.append(el("h3", { class: "cp-network-subheading", text: "Tiers" }));
    for (const [i, tier] of draft.egressTiers.entries()) {
      tiersHost.append(tierRow(tier, i));
    }
    const addTierBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: "Add tier" });
    addTierBtn.addEventListener("click", () => {
      draft.egressTiers.splice(draft.egressTiers.length - 1, 0, { up_to_gb: 0, price_per_gb: 0 });
      renderTiers();
      renderPreview();
    });
    tiersHost.append(addTierBtn);
  }

  function tierRow(tier, i) {
    const isLast = i === draft.egressTiers.length - 1;
    const row = el("div", { class: "cp-billing-tier-row" });
    const upToInput = /** @type {HTMLInputElement} */ (
      el("input", {
        class: "cp-input cp-limit-input",
        attrs: { type: "text", inputmode: "decimal", value: isLast ? "" : String(tier.up_to_gb), placeholder: isLast ? "Unbounded" : "Up to GB", "aria-label": `Tier ${i + 1} upper bound in GB` },
      })
    );
    upToInput.disabled = isLast;
    upToInput.addEventListener("input", () => {
      tier.up_to_gb = Number(upToInput.value) || 0;
      renderPreview();
    });
    const priceInput = /** @type {HTMLInputElement} */ (
      el("input", {
        class: "cp-input cp-limit-input",
        attrs: { type: "text", inputmode: "decimal", value: String(tier.price_per_gb), "aria-label": `Tier ${i + 1} price per GB` },
      })
    );
    priceInput.addEventListener("input", () => {
      tier.price_per_gb = Number(priceInput.value) || 0;
      renderPreview();
    });
    row.append(upToInput, el("span", { text: "GB @ $" }), priceInput, el("span", { text: "/GB" }));
    if (draft.egressTiers.length > 1) {
      const removeBtn = el("button", { class: "cp-btn cp-btn-danger cp-btn-sm cp-btn-icon", attrs: { type: "button", "aria-label": `Remove tier ${i + 1}` } });
      removeBtn.append(icon("trash"));
      removeBtn.addEventListener("click", () => {
        draft.egressTiers.splice(i, 1);
        renderTiers();
        renderPreview();
      });
      row.append(removeBtn);
    }
    return row;
  }
  renderTiers();

  const ingressField = el("div", { class: "cp-field" });
  ingressField.append(el("label", { class: "cp-label", attrs: { for: "cp-plan-ingress" }, text: "Inbound price per GB (USD)" }));
  const ingressInput = /** @type {HTMLInputElement} */ (el("input", { class: "cp-input", attrs: { id: "cp-plan-ingress", type: "text", inputmode: "decimal", value: String(draft.ingressPricePerGB) } }));
  ingressInput.addEventListener("input", () => {
    draft.ingressPricePerGB = Number(ingressInput.value) || 0;
  });
  ingressField.append(ingressInput);
  body.append(ingressField);

  const poolField = el("label", { class: "cp-checkbox-field" });
  const poolInput = /** @type {HTMLInputElement} */ (el("input", { attrs: { type: "checkbox" } }));
  poolInput.checked = draft.poolFreeTier;
  poolInput.addEventListener("change", () => (draft.poolFreeTier = poolInput.checked));
  poolField.append(poolInput, el("span", { text: "Share free tier across every host on this plan (account/tenancy-wide billing)" }));
  body.append(poolField);

  const previewHost = el("p", { class: "cp-muted-small" });
  body.append(previewHost);
  function renderPreview() {
    const cost = previewCostForOneTB(draft.egressFreeGB, draft.egressTiers);
    previewHost.textContent = `Preview: 1 TB of egress on this plan costs ${formatUSD(cost)}.`;
  }
  renderPreview();

  const errorsHost = el("div");
  body.append(errorsHost);

  const cancelBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Cancel" });
  cancelBtn.addEventListener("click", () => close());

  const saveBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: isEdit ? "Save changes" : "Add plan" });
  saveBtn.addEventListener("click", async () => {
    clearChildren(errorsHost);
    if (!draft.name.trim()) {
      errorsHost.append(el("p", { class: "cp-error-banner", text: "Name is required." }));
      return;
    }
    const payload = {
      name: draft.name,
      provider: draft.provider,
      egress_free_gb: draft.egressFreeGB,
      egress_tiers: draft.egressTiers,
      ingress_price_per_gb: draft.ingressPricePerGB,
      pool_free_tier: draft.poolFreeTier,
    };
    saveBtn.disabled = true;
    try {
      if (isEdit) {
        await updatePricingPlan(plan.id, payload, signal);
        announce?.(`Saved plan ${draft.name}.`);
      } else {
        await createPricingPlan(payload, signal);
        announce?.(`Added plan ${draft.name}.`);
      }
      close();
      await onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      clearChildren(errorsHost);
      const details = err instanceof ApiError && err.details ? err.details : { form: describeError(err) };
      for (const message of Object.values(details)) {
        errorsHost.append(el("p", { class: "cp-error-banner", text: String(message) }));
      }
    } finally {
      saveBtn.disabled = false;
    }
  });

  body.append(buildDialogActions([cancelBtn, saveBtn]));
  document.body.append(dialog);
  open();
}

/**
 * previewCostForOneTB computes the cost of exactly 1 TB (1000 GB, the
 * same decimal-GB convention as internal/billing/network.go) of egress
 * against a draft plan's free allowance + tiers, mirroring
 * internal/billing/network.go's tierCost logic closely enough for a
 * live editor preview (no pooling — this is a single-host preview).
 */
function previewCostForOneTB(freeGB, tiers) {
  const usageGB = 1000;
  let remaining = Math.max(0, usageGB - Math.max(0, freeGB));
  if (remaining <= 0) return 0;
  let cost = 0;
  let coveredSoFar = 0;
  for (const tier of tiers) {
    const tierCapacity = tier.up_to_gb === 0 ? Infinity : Math.max(0, tier.up_to_gb - coveredSoFar);
    const usedInTier = Math.min(remaining, tierCapacity);
    if (usedInTier <= 0 && tier.up_to_gb !== 0 && coveredSoFar >= tier.up_to_gb) continue;
    cost += usedInTier * tier.price_per_gb;
    remaining -= usedInTier;
    coveredSoFar = tier.up_to_gb === 0 ? coveredSoFar : tier.up_to_gb;
    if (remaining <= 0) break;
  }
  return cost;
}

/** auditLogCard builds the "변경 이력" (change history) table. */
function auditLogCard({ signal }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: "Change history" }));

  const tableHost = el("div");
  card.append(tableHost);

  loadPage(0);

  async function loadPage(before) {
    clearChildren(tableHost);
    tableHost.append(el("p", { class: "cp-muted-small", text: "Loading…" }));
    let view;
    try {
      view = await getAuditEntries({ limit: 20, before: before || undefined }, signal);
    } catch (err) {
      if (err?.name === "AbortError") return;
      clearChildren(tableHost);
      tableHost.append(errorBanner(describeError(err)));
      return;
    }
    clearChildren(tableHost);
    if ((view.entries || []).length === 0) {
      tableHost.append(el("p", { class: "cp-muted", text: "No changes recorded yet." }));
      return;
    }

    const table = el("table", { class: "cp-table" });
    const thead = el("thead");
    thead.append(el("tr", { children: ["When", "Who", "Action", "Entity", "Before → after"].map((t) => el("th", { text: t })) }));
    table.append(thead);
    const tbody = el("tbody");
    for (const entry of view.entries) {
      tbody.append(auditRow(entry));
    }
    table.append(tbody);
    const tableWrap = el("div", { class: "cp-table-wrap" });
    tableWrap.append(table);
    tableHost.append(tableWrap);

    if (view.has_more) {
      const oldest = view.entries[view.entries.length - 1];
      const moreBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: "Load older entries" });
      moreBtn.addEventListener("click", () => loadPage(oldest.at));
      tableHost.append(moreBtn);
    }
  }

  return card;
}

/**
 * auditRow renders one audit entry. The optional clock is a deterministic
 * test seam; production callers intentionally use the current time.
 */
export function auditRow(entry, nowMs = Date.now()) {
  const row = el("tr");
  row.append(el("td", { text: formatRelativeTimeFromUnixSeconds(entry.at, nowMs) }));
  row.append(el("td", { text: entry.actor }));
  row.append(el("td", { text: entry.action }));
  row.append(el("td", { text: `${entry.entity_type}${entry.entity_id ? ` #${entry.entity_id}` : ""}` }));
  const diffCell = el("td", { class: "cp-billing-audit-diff" });
  diffCell.append(auditJSONValue(entry.before_json, "(created)"));
  diffCell.append(el("span", { text: " → " }));
  diffCell.append(auditJSONValue(entry.after_json, "(deleted)"));
  row.append(diffCell);
  return row;
}

/** AUDIT_DIFF_PREVIEW_MAX_CHARS bounds how much of a before/after JSON
 * value is shown inline in the audit table before truncating with an
 * ellipsis — a remote-update-batch entry's AfterJSON can embed a whole
 * nested jobs[] array (see models.AuditEntry's doc comment), which
 * would otherwise force the row to an unreadable height. The full,
 * untruncated value is always still available via the element's
 * title attribute (a native browser tooltip on hover/focus). */
const AUDIT_DIFF_PREVIEW_MAX_CHARS = 160;

/**
 * auditJSONValue renders one side of an audit entry's before/after
 * diff: emptyLabel (e.g. "(created)") when raw is empty/absent,
 * otherwise the raw JSON text truncated to
 * AUDIT_DIFF_PREVIEW_MAX_CHARS with the full value available via a
 * title tooltip. Uses .text (never innerHTML), so arbitrary
 * before/after content (e.g. a pricing plan name containing HTML) is
 * always rendered as a literal string, never parsed as markup.
 * @param {string} raw
 * @param {string} emptyLabel
 * @returns {HTMLElement}
 */
function auditJSONValue(raw, emptyLabel) {
  if (!raw) return el("code", { text: emptyLabel });
  const truncated = raw.length > AUDIT_DIFF_PREVIEW_MAX_CHARS;
  const preview = truncated ? `${raw.slice(0, AUDIT_DIFF_PREVIEW_MAX_CHARS)}…` : raw;
  return el("code", { text: preview, attrs: truncated ? { title: raw } : {} });
}

/** secondsToInterval converts a billing view's interval_seconds back
 * to a "6h"/"12h"/"24h" string for the select's initial value. */
function secondsToInterval(seconds) {
  if (seconds === 6 * 3600) return "6h";
  if (seconds === 12 * 3600) return "12h";
  return "24h";
}

function describeError(err) {
  if (err instanceof ApiError) return err.message;
  return String(err?.message || err);
}
