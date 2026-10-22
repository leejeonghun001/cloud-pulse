// settings/billing-accounts.js — Settings → Billing's account list: one
// row per billing account (each OCI tenancy / AWS account an agent
// reports, plus the hub's own), with its status, who collects it, and
// rename/delete.
import { el } from "../../ui/components.js";
import { showToast } from "../../ui/toast.js";
import { freshnessViewModel, accountTitle, providerStatusHint } from "../../core/billing-view.js";
import { setBillingAccountLabel, deleteBillingAccount, localizeApiError } from "../../core/api.js";
import { t } from "../../core/i18n.js";
import { disclosure } from "../../ui/disclosure.js";
import { ociAgentGuideCard } from "./oci-agent-guide.js";
import { awsAgentGuideCard } from "./aws-agent-guide.js";

/**
 * accountsCard lists billing accounts.
 * @param {Object} opts
 * @param {Object} opts.billing GET /api/v1/billing response
 * @param {AbortSignal} opts.signal
 * @param {(text: string) => void} [opts.announce]
 * @param {() => Promise<void>} opts.onChanged re-renders the section
 * @returns {HTMLElement}
 */
export function accountsCard({ billing, signal, announce, onChanged }) {
  const card = el("section", { class: "cp-card cp-billing-card cp-billing-accounts", attrs: { id: "billing-accounts", "aria-labelledby": "cp-billing-accounts-heading" } });
  card.append(el("h2", { class: "cp-card-title", text: t("billing.accounts_title"), attrs: { id: "cp-billing-accounts-heading" } }));
  card.append(el("p", { class: "cp-muted-small", text: t("billing.accounts_description") }));

  if ((billing.snapshots || []).length === 0) {
    card.append(el("p", { class: "cp-muted-small", text: t("billing.billing_disabled_note") }));
    return card;
  }
  const accounts = (billing.snapshots || []).filter((snap) => snap.account_id || snap.source_host || snap.last_success_at > 0 || !["not_installed", "not_configured"].includes(snap.status));

  // One collapsible section per provider: its accounts, then its setup
  // guide — itself collapsed once an account is connected.
  for (const { provider, name, guide } of PROVIDER_SECTIONS) {
    const mine = accounts.filter((snap) => snap.provider === provider);
    const children = [];
    if (mine.length > 0) {
      const list = el("ul", { class: "cp-billing-account-list", attrs: { role: "list" } });
      for (const snap of mine) list.append(accountRow(snap, billing.interval_seconds || 0, { signal, announce, onChanged }));
      children.push(list);
      children.push(disclosure({
        id: `billing-guide-${provider}`,
        summary: t("billing.provider_guide_add", { provider: name }),
        children: [guide()],
        className: "cp-billing-guide-disclosure",
      }).node);
    } else {
      children.push(guide());
    }
    card.append(disclosure({
      id: `billing-provider-${provider}`,
      summary: providerSummary(name, mine),
      children,
      className: "cp-billing-provider-disclosure",
    }).node);
  }
  return card;
}

const PROVIDER_SECTIONS = [
  { provider: "oci", name: "OCI", guide: () => ociAgentGuideCard({ bare: true }) },
  { provider: "aws", name: "AWS", guide: () => awsAgentGuideCard({ bare: true }) },
];

/** providerSummary is a provider section's collapsed line: how many
 * accounts, and whether any needs attention. */
function providerSummary(name, accounts) {
  if (accounts.length === 0) return t("billing.provider_summary_none", { provider: name });
  const failing = accounts.filter((snap) => snap.status !== "ok").length;
  return failing === 0
    ? t("billing.provider_summary_ok", { provider: name, count: accounts.length })
    : t("billing.provider_summary_attention", { provider: name, count: accounts.length, failing });
}

function accountRow(snap, intervalSeconds, { signal, announce, onChanged }) {
  const row = el("li", { class: "cp-billing-account" });
  const head = el("div", { class: "cp-billing-account-head" });
  head.append(el("strong", { class: "cp-billing-account-title", text: accountTitle(snap) }));
  const tone = snap.status === "ok" ? "cp-chip-ok" : ["auth_failed", "permission_denied", "error"].includes(snap.status) ? "cp-chip-critical" : "cp-chip-other";
  const statusKey = `costs.status_${snap.status}`;
  const statusLabel = t(statusKey);
  head.append(el("span", { class: `cp-chip ${tone}`, text: statusLabel === statusKey ? snap.status : statusLabel }));
  if (snap.stale) head.append(el("span", { class: "cp-chip cp-chip-warning", text: t("costs.stale_data") }));
  row.append(head);

  const meta = [snap.source_host ? t("billing.account_source_agent", { host: snap.source_host }) : t("billing.account_source_hub")];
  if (snap.account_id && !snap.account_id.startsWith("host:")) meta.push(snap.account_id);
  row.append(el("p", { class: "cp-muted-small cp-billing-account-meta", text: meta.join(" · ") }));
  const vm = freshnessViewModel({
    status: snap.status,
    lastSuccessAt: snap.last_success_at || 0,
    nowUnixSeconds: Math.floor(Date.now() / 1000),
    intervalSeconds,
    utcOffsetMinutes: -new Date().getTimezoneOffset(),
  });
  row.append(el("p", { class: "cp-muted-small", text: vm.text }));
  if (snap.status !== "ok" && snap.status_detail) row.append(el("p", { class: "cp-muted-small", text: snap.status_detail }));
  const hint = providerStatusHint(snap);
  if (hint) row.append(el("p", { class: "cp-billing-account-hint", text: t(hint.key, hint.params) }));

  const actions = el("div", { class: "cp-billing-account-actions" });
  const inputID = `cp-account-label-${snap.provider}-${snap.account_id || "hub"}`.replace(/[^A-Za-z0-9_-]/g, "_");
  const field = el("div", { class: "cp-field cp-billing-account-label-field" });
  field.append(el("label", { class: "cp-label", attrs: { for: inputID }, text: t("billing.account_label") }));
  const input = /** @type {HTMLInputElement} */ (el("input", { class: "cp-input", attrs: { id: inputID, type: "text", maxlength: "64", value: snap.label || "", placeholder: t("billing.account_label_placeholder") } }));
  field.append(input);
  const save = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("billing.account_label_save") });
  save.addEventListener("click", async () => {
    save.disabled = true;
    try {
      await setBillingAccountLabel(snap.provider, snap.account_id || "", input.value.trim(), signal);
      announce?.(t("billing.account_label_saved"));
      await onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: t("billing.account_action_failed", { reason: localizeApiError(err) }), variant: "error" });
    } finally {
      save.disabled = false;
    }
  });
  const remove = el("button", { class: "cp-btn cp-btn-secondary cp-billing-account-delete", attrs: { type: "button" }, text: t("billing.account_delete") });
  remove.addEventListener("click", async () => {
    const message = snap.source_host ? t("billing.account_delete_confirm_agent", { account: accountTitle(snap), host: snap.source_host }) : t("billing.account_delete_confirm", { account: accountTitle(snap) });
    if (!globalThis.confirm(message)) return;
    remove.disabled = true;
    try {
      await deleteBillingAccount(snap.provider, snap.account_id || "", signal);
      announce?.(t("billing.account_deleted"));
      await onChanged();
    } catch (err) {
      if (err?.name === "AbortError") return;
      showToast({ message: t("billing.account_action_failed", { reason: localizeApiError(err) }), variant: "error" });
      remove.disabled = false;
    }
  });
  actions.append(field, save, remove);
  row.append(disclosure({ summary: t("billing.account_manage"), children: [actions], className: "cp-billing-account-manage" }).node);
  return row;
}
