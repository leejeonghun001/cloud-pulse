// settings/billing.js — Settings → Billing section (SPEC-v0.6 §1/§3):
// provider status, polling interval selection, pricing plan editor,
// display currency, and audit log.
import { el, clearChildren, errorBanner, providerBadge } from "../../ui/components.js";
import { icon } from "../../ui/icons.js";
import { disclosure } from "../../ui/disclosure.js";
import { showToast } from "../../ui/toast.js";
import { selectField } from "../../ui/select.js";
import { createDialog, buildDialogActions } from "../../ui/dialog.js";
import { formatUSD } from "../../core/currency.js";
import { formatRelativeTimeFromUnixSeconds } from "../../core/format.js";
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
  localizeApiError,
} from "../../core/api.js";
import { t } from "../../core/i18n.js";
import { accountsCard } from "./billing-accounts.js";

/** INTERVAL_API_COST_HINTS shows the AWS Cost Explorer API cost
 * estimate for each poll interval (SPEC-v0.6 §1: 24h ≈ $0.6/mo, 12h ≈
 * $1.2/mo, 6h ≈ $2.4/mo). A getter object so labels re-resolve against
 * the current language on every read. */
const INTERVAL_API_COST_HINTS = {
  get "6h"() { return t("billing.interval_6h"); },
  get "12h"() { return t("billing.interval_12h"); },
  get "24h"() { return t("billing.interval_24h"); },
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
  let generation = 0;

  async function refresh(feedback) {
    const current = ++generation;
    clearChildren(container);
    const root = el("div", { class: "cp-billing-page", attrs: { "aria-busy": "true" } });
    root.append(el("p", { class: "cp-muted", text: t("billing.loading"), attrs: { role: "status" } }));
    container.append(root);

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
      if (err?.name === "AbortError" || controller.signal.aborted || current !== generation) return;
      clearChildren(root);
      root.setAttribute("aria-busy", "false");
      root.append(errorBanner(t("billing.load_failed", { reason: describeError(err) })));
      const retry = el("button", { class: "cp-btn cp-btn-secondary", text: t("billing.retry"), attrs: { type: "button" } });
      retry.addEventListener("click", () => refresh());
      root.append(retry);
      return;
    }
    if (controller.signal.aborted || current !== generation) return;
    clearChildren(root);
    root.setAttribute("aria-busy", "false");

    const controls = el("div", { class: "cp-billing-controls" });
    controls.append(intervalCard({ billing, signal: controller.signal, announce, onChanged: refresh, feedback }));
    controls.append(currencyCard({ currency, signal: controller.signal, announce, onChanged: refresh, feedback }));
    root.append(controls);
    root.append(accountsCard({ billing, signal: controller.signal, announce, onChanged: refresh }));
    root.append(plansCard({ plans, signal: controller.signal, announce, onChanged: refresh }));
    root.append(auditLogCard({ signal: controller.signal }));
    if (feedback?.id) document.getElementById(feedback.id)?.focus();
  }

  function teardown() {
    controller.abort();
  }

  return { refresh, teardown };
}

/** intervalCard builds the provider status + polling interval card. */
function intervalCard({ billing, signal, announce, onChanged, feedback }) {
  const card = el("section", { class: "cp-card cp-billing-card", attrs: { "aria-labelledby": "cp-billing-polling-heading" } });
  card.append(el("h2", { class: "cp-card-title", text: t("billing.polling_title"), attrs: { id: "cp-billing-polling-heading" } }));
  const form = el("form", { class: "cp-billing-form" });

  const currentInterval = secondsToInterval(billing.interval_seconds);
  const { node: intervalNode, select: intervalSelect } = selectField({
    id: "cp-billing-interval",
    label: t("billing.polling_interval_label"),
    options: Object.keys(INTERVAL_API_COST_HINTS).map((value) => ({ value, label: t(`billing.interval_option_${value}`) })),
    value: currentInterval,
  });
  form.append(intervalNode);
  const intervalHint = el("p", { class: "cp-muted-small", attrs: { id: "cp-billing-interval-hint" } });
  intervalSelect.setAttribute("aria-describedby", "cp-billing-interval-hint");
  function syncIntervalHint() { intervalHint.textContent = INTERVAL_API_COST_HINTS[intervalSelect.value]; }
  syncIntervalHint();
  intervalSelect.addEventListener("change", syncIntervalHint);
  form.append(intervalHint);

  const status = saveStatus(feedback?.id === "cp-billing-save-interval" ? feedback.message : "", "cp-billing-interval-status");
  const saveBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "submit", id: "cp-billing-save-interval", "aria-describedby": "cp-billing-interval-status" }, text: t("billing.save_interval") });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (saveBtn.disabled) return;
    saveBtn.disabled = true;
    status.textContent = t("billing.saving");
    try {
      await setBillingInterval(intervalSelect.value, signal);
      status.textContent = t("billing.saved_takes_effect");
      announce?.(t("billing.interval_set_to", { interval: intervalSelect.value }));
      await onChanged({ id: "cp-billing-save-interval", message: t("billing.saved_takes_effect") });
    } catch (err) {
      if (err?.name === "AbortError") return;
      status.textContent = t("billing.save_failed", { reason: describeError(err) });
    } finally {
      saveBtn.disabled = false;
    }
  });
  form.append(el("div", { class: "cp-billing-actions", children: [saveBtn, status] }));
  card.append(form);
  card.append(disclosure({ id: "billing-interval-help", summary: t("billing.help_summary"), children: [el("p", { class: "cp-muted-small", text: t("billing.provider_setup_note") })] }).node);

  return card;
}

/** currencyCard builds the display currency settings card. */
function currencyCard({ currency, signal, announce, onChanged, feedback }) {
  const card = el("section", { class: "cp-card cp-billing-card", attrs: { "aria-labelledby": "cp-billing-currency-heading" } });
  card.append(el("h2", { class: "cp-card-title", text: t("billing.display_currency_title"), attrs: { id: "cp-billing-currency-heading" } }));
  card.append(el("p", { class: "cp-muted-small", text: t("billing.display_currency_description") }));
  const form = el("form", { class: "cp-billing-form", attrs: { novalidate: "" } });

  const { node: currencyNode, select: currencySelect } = selectField({
    id: "cp-billing-currency",
    label: t("billing.currency_label"),
    options: [
      { value: "USD", get label() { return t("billing.usd"); } },
      { value: "KRW", get label() { return t("billing.krw_manual_rate"); } },
    ],
    value: currency.currency || "USD",
  });
  form.append(currencyNode);

  const rateField = el("div", { class: "cp-field" });
  rateField.append(el("label", { class: "cp-label", attrs: { for: "cp-billing-krw-rate" }, text: t("billing.krw_per_usd_label") }));
  const rateInput = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input",
      attrs: { id: "cp-billing-krw-rate", name: "krw_per_usd", type: "text", inputmode: "decimal", value: currency.krw_per_usd ? String(currency.krw_per_usd) : "", "aria-describedby": "cp-billing-rate-hint cp-billing-currency-status" },
    })
  );
  rateField.append(rateInput);
  rateField.append(el("p", { class: "cp-muted-small", text: t("billing.manual_rate_note"), attrs: { id: "cp-billing-rate-hint" } }));
  form.append(rateField);

  function syncRateVisibility() {
    rateField.hidden = currencySelect.value !== "KRW";
    rateInput.required = !rateField.hidden;
    rateInput.removeAttribute("aria-invalid");
  }
  syncRateVisibility();
  currencySelect.addEventListener("change", syncRateVisibility);

  if (currency.rate_updated_at) {
    rateField.append(
      el("p", {
        class: "cp-muted-small",
        text: t("billing.rate_last_entered", { date: new Date(currency.rate_updated_at * 1000).toISOString().slice(0, 10) }),
      }),
    );
  }

  const status = saveStatus(feedback?.id === "cp-billing-save-currency" ? feedback.message : "", "cp-billing-currency-status");
  const saveBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "submit", id: "cp-billing-save-currency", "aria-describedby": "cp-billing-currency-status" }, text: t("billing.save_currency") });
  rateInput.addEventListener("input", () => rateInput.removeAttribute("aria-invalid"));
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (saveBtn.disabled) return;
    const rate = Number(rateInput.value);
    if (currencySelect.value === "KRW" && (!Number.isFinite(rate) || rate <= 0)) {
      status.textContent = t("billing.enter_positive_rate");
      rateInput.setAttribute("aria-invalid", "true");
      rateInput.focus();
      return;
    }
    saveBtn.disabled = true;
    status.textContent = t("billing.saving");
    try {
      await setDisplayCurrency(currencySelect.value, rate, signal);
      status.textContent = t("billing.saved");
      announce?.(t("billing.currency_set_to", { currency: currencySelect.value }));
      await onChanged({ id: "cp-billing-save-currency", message: t("billing.saved") });
    } catch (err) {
      if (err?.name === "AbortError") return;
      if (err instanceof ApiError && err.code === "rate_required") {
        status.textContent = t("billing.krw_requires_positive_rate");
        rateInput.setAttribute("aria-invalid", "true");
        rateInput.focus();
      } else {
        status.textContent = t("billing.save_failed", { reason: describeError(err) });
      }
    } finally {
      saveBtn.disabled = false;
    }
  });
  form.append(el("div", { class: "cp-billing-actions", children: [saveBtn, status] }));
  card.append(form);
  card.append(disclosure({ id: "billing-currency-help", summary: t("billing.help_summary"), children: [el("p", { class: "cp-muted-small", text: t("billing.tax_note") })], className: "cp-billing-tax-note" }).node);
  return card;
}

function saveStatus(message, id) {
  return el("p", { class: "cp-muted-small cp-billing-save-status", text: message, attrs: { id, role: "status", "aria-live": "polite", "aria-atomic": "true" } });
}

/** plansCard builds the pricing plan list + editor dialog trigger. */
function plansCard({ plans, signal, announce, onChanged }) {
  const card = el("section", { class: "cp-card cp-billing-card", attrs: { "aria-labelledby": "cp-billing-plans-heading" } });
  const head = el("div", { class: "cp-billing-section-head" });
  head.append(el("h2", { class: "cp-card-title", text: t("billing.plans_title"), attrs: { id: "cp-billing-plans-heading" } }));
  const addBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" } });
  addBtn.append(icon("plus"), el("span", { text: t("billing.add_plan") }));
  addBtn.addEventListener("click", () => openPlanDialog({ signal, announce, onChanged }));
  head.append(addBtn);
  card.append(head);
  card.append(el("p", { class: "cp-muted-small", text: t("billing.plans_description") }));

  const list = el("ul", { class: "cp-billing-plan-list", attrs: { role: "list" } });
  for (const plan of plans) {
    list.append(planRow({ plan, signal, announce, onChanged }));
  }
  card.append(list);
  if (plans.length === 0) card.append(el("p", { class: "cp-muted", text: t("billing.plans_empty") }));
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
  const info = el("div", { class: "cp-billing-plan-info" });
  info.append(el("span", { class: "cp-billing-plan-row-name", text: plan.name }));
  info.append(el("span", { class: "cp-muted-small", text: plan.pool_free_tier ? t("billing.free_gb_pooled", { gb: plan.egress_free_gb }) : t("billing.free_gb", { gb: plan.egress_free_gb }) }));
  row.append(info);
  const actions = el("div", { class: "cp-billing-plan-actions" });
  if (plan.builtin) {
    actions.append(el("span", { class: "cp-chip cp-chip-other", text: t("billing.builtin") }));
  } else {
    const editBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm cp-btn-icon", attrs: { type: "button", "aria-label": t("billing.edit_plan_aria", { name: plan.name }) } });
    editBtn.append(icon("pencil"));
    editBtn.addEventListener("click", () => openPlanDialog({ plan, signal, announce, onChanged }));

    const deleteBtn = el("button", { class: "cp-btn cp-btn-danger cp-btn-sm cp-btn-icon", attrs: { type: "button", "aria-label": t("billing.delete_plan_aria", { name: plan.name }) } });
    deleteBtn.append(icon("trash"));
    deleteBtn.addEventListener("click", async () => {
      if (!globalThis.confirm(t("billing.delete_plan_confirm", { name: plan.name }))) return;
      try {
        await deletePricingPlan(plan.id, signal);
        announce?.(t("billing.plan_deleted", { name: plan.name }));
        await onChanged();
      } catch (err) {
        if (err?.name === "AbortError") return;
        showToast({ message: t("billing.delete_failed", { reason: describeError(err) }), variant: "error" });
      }
    });
    actions.append(editBtn, deleteBtn);
  }
  row.append(actions);
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
    title: isEdit ? t("billing.edit_plan_title", { name: plan.name }) : t("billing.add_plan_title"),
    wide: true,
  });
  dialog.classList.add("cp-billing-plan-dialog");

  const draft = {
    name: plan?.name || "",
    provider: plan?.provider || "other",
    egressFreeGB: plan?.egress_free_gb ?? 0,
    egressTiers: (plan?.egress_tiers || [{ up_to_gb: 0, price_per_gb: 0 }]).map((t) => ({ ...t })),
    ingressPricePerGB: plan?.ingress_price_per_gb ?? 0,
    poolFreeTier: plan?.pool_free_tier ?? false,
  };

  const nameField = el("div", { class: "cp-field" });
  nameField.append(el("label", { class: "cp-label", attrs: { for: "cp-plan-name" }, text: t("billing.plan_name") }));
  const nameInput = /** @type {HTMLInputElement} */ (el("input", { class: "cp-input", attrs: { id: "cp-plan-name", type: "text", value: draft.name } }));
  nameInput.addEventListener("input", () => (draft.name = nameInput.value));
  nameField.append(nameInput);
  body.append(nameField);

  const freeField = el("div", { class: "cp-field" });
  freeField.append(el("label", { class: "cp-label", attrs: { for: "cp-plan-free" }, text: t("billing.free_allowance_label") }));
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
    tiersHost.append(el("h3", { class: "cp-network-subheading", text: t("billing.tiers") }));
    for (const [i, tier] of draft.egressTiers.entries()) {
      tiersHost.append(tierRow(tier, i));
    }
    const addTierBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: t("billing.add_tier") });
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
        attrs: { type: "text", inputmode: "decimal", value: isLast ? "" : String(tier.up_to_gb), placeholder: isLast ? t("billing.tier_unbounded") : t("billing.tier_up_to_placeholder"), "aria-label": t("billing.tier_upper_bound_aria", { n: i + 1 }) },
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
        attrs: { type: "text", inputmode: "decimal", value: String(tier.price_per_gb), "aria-label": t("billing.tier_price_aria", { n: i + 1 }) },
      })
    );
    priceInput.addEventListener("input", () => {
      tier.price_per_gb = Number(priceInput.value) || 0;
      renderPreview();
    });
    row.append(upToInput, el("span", { text: t("billing.tier_gb_at") }), priceInput, el("span", { text: t("billing.tier_per_gb") }));
    if (draft.egressTiers.length > 1) {
      const removeBtn = el("button", { class: "cp-btn cp-btn-danger cp-btn-sm cp-btn-icon", attrs: { type: "button", "aria-label": t("billing.remove_tier_aria", { n: i + 1 }) } });
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
  ingressField.append(el("label", { class: "cp-label", attrs: { for: "cp-plan-ingress" }, text: t("billing.ingress_price_label") }));
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
  poolField.append(poolInput, el("span", { text: t("billing.pool_free_tier_label") }));
  body.append(poolField);

  const previewHost = el("p", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  body.append(previewHost);
  function renderPreview() {
    const cost = previewCostForOneTB(draft.egressFreeGB, draft.egressTiers);
    previewHost.textContent = t("billing.preview_1tb", { cost: formatUSD(cost, undefined, { taxNote: false }) });
  }
  renderPreview();

  const errorsHost = el("div", { attrs: { role: "alert" } });
  body.append(errorsHost);

  const cancelBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("common.cancel") });
  cancelBtn.addEventListener("click", () => close());

  const saveBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: isEdit ? t("billing.save_changes") : t("billing.add_plan") });
  saveBtn.addEventListener("click", async () => {
    clearChildren(errorsHost);
    if (!draft.name.trim()) {
      errorsHost.append(el("p", { class: "cp-error-banner", text: t("billing.name_required") }));
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
        announce?.(t("billing.plan_saved", { name: draft.name }));
      } else {
        await createPricingPlan(payload, signal);
        announce?.(t("billing.plan_added", { name: draft.name }));
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
  const card = el("section", { class: "cp-card cp-billing-card cp-billing-history", attrs: { "aria-labelledby": "cp-billing-history-heading" } });
  card.append(el("h2", { class: "cp-card-title", text: t("billing.history_title"), attrs: { id: "cp-billing-history-heading" } }));

  const tableHost = el("div");
  card.append(disclosure({ id: "billing-history", summary: t("billing.history_show"), children: [tableHost] }).node);

  loadPage(0);

  async function loadPage(before) {
    clearChildren(tableHost);
    tableHost.append(el("p", { class: "cp-muted-small", text: t("common.loading"), attrs: { role: "status" } }));
    let view;
    try {
      view = await getAuditEntries({ limit: 20, before: before || undefined }, signal);
    } catch (err) {
      if (err?.name === "AbortError" || signal.aborted) return;
      clearChildren(tableHost);
      tableHost.append(errorBanner(describeError(err)));
      return;
    }
    if (signal.aborted) return;
    clearChildren(tableHost);
    if ((view.entries || []).length === 0) {
      tableHost.append(el("p", { class: "cp-muted", text: t("billing.history_empty") }));
      return;
    }

    const table = el("table", { class: "cp-table", attrs: { "aria-labelledby": "cp-billing-history-heading" } });
    const thead = el("thead");
    thead.append(el("tr", { children: [t("billing.col_when"), t("billing.col_who"), t("billing.col_action"), t("billing.col_entity"), t("billing.col_before_after")].map((label) => el("th", { text: label, attrs: { scope: "col" } })) }));
    table.append(thead);
    const tbody = el("tbody");
    for (const entry of view.entries) {
      tbody.append(auditRow(entry));
    }
    table.append(tbody);
    const tableWrap = el("div", { class: "cp-table-wrap", attrs: { tabindex: "0", role: "region", "aria-labelledby": "cp-billing-history-heading" } });
    tableWrap.append(table);
    tableHost.append(tableWrap);

    if (view.has_more) {
      const oldest = view.entries[view.entries.length - 1];
      const moreBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: t("billing.load_older") });
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
  row.append(el("td", { text: entry.entity_id ? t("billing.audit_entity_with_id", { type: entry.entity_type, id: entry.entity_id }) : entry.entity_type }));
  const diffCell = el("td", { class: "cp-billing-audit-diff" });
  diffCell.append(auditJSONValue(entry.before_json, t("billing.audit_created")));
  diffCell.append(el("span", { text: " → " }));
  diffCell.append(auditJSONValue(entry.after_json, t("billing.audit_deleted")));
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
  if (err instanceof ApiError) return localizeApiError(err);
  return String(err?.message || err);
}

/** providerStatusLabel localizes a models.CloudBillingStatus value
 * (shared with the Costs page's costs.status_* keys), falling back to
 * the raw value for a status this dashboard version doesn't know. */
export function providerStatusLabel(status) {
  const key = `costs.status_${status}`;
  const label = t(key);
  return label === key ? status : label;
}
