// storage-overview.js — builds the overview page's "Storage" section
// (SPEC-v0.7 §3/§5): one compact card per connected Google Drive/
// Dropbox account with a usage bar, quota, trash usage, and a status +
// freshness badge — the personal/team cloud-drive counterpart to the
// existing "Object storage" (S3/R2 bucket) section built by buckets.js,
// which is kept unchanged and shown separately.
import { el, progressBar, emptyState } from "../ui/components.js";
import { formatBytes, formatPercent, formatRelativeTimeFromUnixSeconds } from "../core/format.js";
import { thresholdBarClass } from "./hosttable.js";
import { providerLabel, storageStatusLabel, storageStatusChipClass, storageUsedPercent } from "../core/storageusage.js";
import { t } from "../core/i18n.js";

/**
 * buildStorageAccountsSection renders the overview's Storage section:
 * a card per connected account, or an empty state pointing at
 * Settings → Storage when none are connected yet.
 * @param {Object} opts
 * @param {Array} opts.accounts models.StorageAccountView[] JSON (from
 *   GET /api/v1/storage/accounts's `accounts` field)
 * @param {number} opts.nowMs
 * @returns {HTMLElement}
 */
export function buildStorageAccountsSection({ accounts, nowMs }) {
  const section = el("section", { class: "cp-section", attrs: { "aria-labelledby": "cp-storage-accounts-heading" } });
  section.append(el("h2", { id: "cp-storage-accounts-heading", text: t("host.storage_section_title") }));

  if (!accounts || accounts.length === 0) {
    section.append(
      emptyState({
        iconName: "cloud",
        title: t("host.storage_empty_title"),
        message: t("host.storage_empty_message"),
      }),
    );
    return section;
  }

  const grid = el("div", { class: "cp-bucket-grid" });
  for (const view of accounts) {
    grid.append(storageAccountCard(view, nowMs));
  }
  section.append(grid);
  return section;
}

/**
 * storageAccountCard builds one connected account's compact overview
 * card.
 * @param {Object} view {account: models.StorageAccount,
 *   snapshot: models.StorageAccountSnapshot|null}
 * @param {number} nowMs
 * @returns {HTMLElement}
 */
function storageAccountCard(view, nowMs) {
  const account = view.account;
  const snap = view.snapshot;
  const card = el("div", { class: "cp-card cp-bucket-card" });

  const header = el("div", { class: "cp-bucket-card-header" });
  header.append(el("h3", { class: "cp-bucket-card-name", text: account.name }));
  card.append(header);
  card.append(el("p", { class: "cp-muted-small", text: providerLabel(account.provider) }));

  const statusRow = el("div", { class: "cp-metric-head" });
  statusRow.append(el("span", { class: storageStatusChipClass(snap), text: storageStatusLabel(snap) }));
  if (snap?.stale) {
    statusRow.append(el("span", { class: "cp-chip cp-chip-warning", text: t("host.stale") }));
  }
  card.append(statusRow);

  if (snap?.status === "ok") {
    const quota = snap.quota;
    if (quota.unlimited) {
      card.append(el("p", { class: "cp-muted-small", text: t("host.storage_used_unlimited", { amount: formatBytes(quota.used_bytes) }) }));
    } else {
      const pct = storageUsedPercent(quota);
      card.append(
        progressBar({
          value: quota.used_bytes,
          max: quota.limit_bytes,
          label: t("host.storage_usage_label", { name: account.name }),
          levelClass: thresholdBarClass(pct),
          valueText: `${formatBytes(quota.used_bytes)} / ${formatBytes(quota.limit_bytes)} (${formatPercent(pct)})`,
        }),
      );
    }
    if (quota.trash_bytes) {
      card.append(el("p", { class: "cp-muted-small", text: t("host.storage_trash", { amount: formatBytes(quota.trash_bytes) }) }));
    }
    if (snap.account_email) {
      card.append(el("p", { class: "cp-muted-small", text: snap.account_email }));
    }
  } else if (snap?.status_detail) {
    card.append(el("p", { class: "cp-muted-small", text: snap.status_detail }));
  }

  card.append(
    el("p", {
      class: "cp-muted-small",
      text: snap?.last_success_at
        ? t("host.storage_last_checked", { relative: formatRelativeTimeFromUnixSeconds(snap.last_success_at, nowMs) })
        : t("host.storage_never_checked"),
    }),
  );

  return card;
}
