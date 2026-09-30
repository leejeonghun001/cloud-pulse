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
  section.append(el("h2", { id: "cp-storage-accounts-heading", text: "Storage" }));

  if (!accounts || accounts.length === 0) {
    section.append(
      emptyState({
        iconName: "cloud",
        title: "No storage accounts connected",
        message: "Connect a Google Drive or Dropbox account from Settings → Storage to monitor its usage here.",
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
    statusRow.append(el("span", { class: "cp-chip cp-chip-warning", text: "Stale" }));
  }
  card.append(statusRow);

  if (snap?.status === "ok") {
    const quota = snap.quota;
    if (quota.unlimited) {
      card.append(el("p", { class: "cp-muted-small", text: `${formatBytes(quota.used_bytes)} used (unlimited)` }));
    } else {
      const pct = storageUsedPercent(quota);
      card.append(
        progressBar({
          value: quota.used_bytes,
          max: quota.limit_bytes,
          label: `${account.name} storage usage`,
          levelClass: thresholdBarClass(pct),
          valueText: `${formatBytes(quota.used_bytes)} / ${formatBytes(quota.limit_bytes)} (${formatPercent(pct)})`,
        }),
      );
    }
    if (quota.trash_bytes) {
      card.append(el("p", { class: "cp-muted-small", text: `Trash: ${formatBytes(quota.trash_bytes)}` }));
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
        ? `Last checked ${formatRelativeTimeFromUnixSeconds(snap.last_success_at, nowMs)}`
        : "Never successfully checked yet.",
    }),
  );

  return card;
}
