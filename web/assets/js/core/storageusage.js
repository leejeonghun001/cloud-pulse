// storageusage.js — pure display helpers for SPEC-v0.7 §3's connected
// storage accounts (Google Drive/Dropbox usage), shared between the
// Settings → Storage accounts page and the overview page's compact
// Storage section. No DOM access here so this module is fully covered
// by node --test without a browser.

/** STATUS_LABELS maps a models.StorageAccountStatus to display text. */
export const STATUS_LABELS = {
  ok: "Connected",
  pending_oauth: "Pending connection",
  not_configured: "Not configured",
  auth_failed: "Authentication failed",
  permission_denied: "Permission denied",
  error: "Error",
};

/** PROVIDER_LABELS maps a models.StorageAccountProvider to display text. */
export const PROVIDER_LABELS = {
  googledrive: "Google Drive",
  dropbox: "Dropbox",
};

/**
 * providerLabel renders a storage account's provider for display,
 * falling back to the raw value for an unrecognized one.
 * @param {string} provider
 * @returns {string}
 */
export function providerLabel(provider) {
  return PROVIDER_LABELS[provider] || provider;
}

/**
 * storageStatusLabel renders a storage account snapshot's status for
 * display. A missing snapshot (never yet polled, e.g. an OAuth flow
 * that hasn't completed) renders as "Pending connection".
 * @param {{status?: string}|null|undefined} snapshot
 * @returns {string}
 */
export function storageStatusLabel(snapshot) {
  if (!snapshot) return "Pending connection";
  return STATUS_LABELS[snapshot.status] || snapshot.status;
}

/**
 * storageStatusChipClass picks a cp-chip-* class for a storage account
 * snapshot's status: green for "ok", gray for "pending_oauth"/missing,
 * amber for anything else (not_configured/auth_failed/
 * permission_denied/error).
 * @param {{status?: string}|null|undefined} snapshot
 * @returns {string}
 */
export function storageStatusChipClass(snapshot) {
  if (!snapshot) return "cp-chip cp-chip-other";
  if (snapshot.status === "ok") return "cp-chip cp-chip-ok";
  if (snapshot.status === "pending_oauth") return "cp-chip cp-chip-other";
  return "cp-chip cp-chip-warning";
}

/**
 * storageUsedPercent computes a quota's used percentage, mirroring the
 * hub's models.StorageQuota.UsedPercent (0 when unlimited or the limit
 * is 0, to match a zero-value struct before any snapshot exists).
 * @param {{used_bytes?: number, limit_bytes?: number, unlimited?: boolean}|null|undefined} quota
 * @returns {number}
 */
export function storageUsedPercent(quota) {
  if (!quota || quota.unlimited || !quota.limit_bytes) return 0;
  return (quota.used_bytes / quota.limit_bytes) * 100;
}
