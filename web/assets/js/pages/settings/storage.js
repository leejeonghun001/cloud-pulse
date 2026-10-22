// settings/storage.js — Settings → Storage accounts section (SPEC-v0.7
// §3): a list of connected Google Drive/Dropbox accounts (usage bar,
// quota, status/freshness badge), "Connect Google Drive" (device-code
// display + copy + poll-until-connected) and "Connect Dropbox"
// (authorize-link + pasted-code) flows. Replaces the prep-stage static
// placeholder.
import { el, clearChildren, emptyState, progressBar } from "../../ui/components.js";
import { formatBytes, formatPercent, formatRelativeTimeFromUnixSeconds } from "../../core/format.js";
import { thresholdBarClass } from "../hosttable.js";
import { providerLabel, storageStatusLabel, storageStatusChipClass, storageUsedPercent } from "../../core/storageusage.js";
import {
  getStorageAccounts,
  createStorageAccount,
  deleteStorageAccount,
  startStorageAccountOAuth,
  completeStorageAccountOAuth,
  setStorageInterval,
  withToastOnError,
  localizeApiError,
} from "../../core/api.js";
import { copyToClipboard } from "./index.js";
import { t } from "../../core/i18n.js";
import { disclosure } from "../../ui/disclosure.js";

// GOOGLE_DEVICE_POLL_MAX_ATTEMPTS/INTERVAL_MS bound how long the
// browser itself keeps polling oauth/complete after "Connect Google
// Drive" — independent of and shorter than the hub's own background
// poller (internal/hub/storagerun.go's googleDevicePollTimeout), so a
// user who navigates away isn't left with a runaway browser-side
// timer; the account can always be reconnected from this page later if
// the window is missed.
const GOOGLE_DEVICE_POLL_MAX_ATTEMPTS = 120; // ~10 minutes at 5s
const GOOGLE_DEVICE_POLL_INTERVAL_MS = 5000;

/**
 * mountStorageSection renders the Storage accounts section into
 * container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountStorageSection(container, { announce }) {
  const controller = new AbortController();
  let pollTimer = null;

  async function refresh() {
    clearChildren(container);
    container.append(connectCard({ signal: controller.signal, announce, onChanged: refresh, setPollTimer, container }));

    const listCard = el("div", { class: "cp-card" });
    container.append(listCard);
    await renderAccountListCard(listCard, { signal: controller.signal, onChanged: refresh });
  }

  function setPollTimer(id) {
    if (pollTimer !== null) clearTimeout(pollTimer);
    pollTimer = id;
  }

  function teardown() {
    controller.abort();
    if (pollTimer !== null) clearTimeout(pollTimer);
  }

  return { refresh, teardown };
}

// ---------------------------------------------------------------------------
// Connect card: provider forms (Google device flow / Dropbox PKCE)
// ---------------------------------------------------------------------------

function connectCard({ signal, announce, onChanged, setPollTimer, container }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("storage.connect_title") }));
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: t("storage.connect_description"),
    }),
  );

  const googleNameInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "text", placeholder: t("storage.google_label_placeholder") } })
  );
  const googleClientIDInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "text", placeholder: t("storage.google_client_id_placeholder") } })
  );
  const googleClientSecretInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "password", placeholder: t("storage.google_client_secret_placeholder") } })
  );
  const googleConnectBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: t("storage.connect_google_drive") });
  const googleFields = el("div", { class: "cp-settings-row" });
  googleFields.append(googleNameInput, googleClientIDInput, googleClientSecretInput, googleConnectBtn);
  const googleHint = el("p", {
    class: "cp-muted-small",
    text: t("storage.google_hint"),
  });

  const dropboxNameInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "text", placeholder: t("storage.dropbox_label_placeholder") } })
  );
  const dropboxAppKeyInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "text", placeholder: t("storage.dropbox_app_key_placeholder") } })
  );
  const dropboxConnectBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: t("storage.connect_dropbox") });
  const dropboxFields = el("div", { class: "cp-settings-row" });
  dropboxFields.append(dropboxNameInput, dropboxAppKeyInput, dropboxConnectBtn);
  const dropboxHint = el("p", {
    class: "cp-muted-small",
    text: t("storage.dropbox_hint"),
  });

  const statusLine = el("p", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });

  googleConnectBtn.addEventListener("click", async () => {
    const name = googleNameInput.value.trim();
    const clientID = googleClientIDInput.value.trim();
    const clientSecret = googleClientSecretInput.value;
    if (!name || !clientID || !clientSecret) {
      statusLine.textContent = t("storage.google_fields_required");
      return;
    }
    googleConnectBtn.disabled = true;
    try {
      const account = await createStorageAccount(
        { provider: "googledrive", name, config: { client_id: clientID }, secret: { client_secret: clientSecret } },
        signal,
      );
      const start = await startStorageAccountOAuth(account.id, signal);
      openGoogleDeviceFlowDialog({ accountID: account.id, start, signal, announce, onChanged, setPollTimer, container });
      googleNameInput.value = "";
      googleClientIDInput.value = "";
      googleClientSecretInput.value = "";
      statusLine.textContent = "";
    } catch (err) {
      if (err?.name === "AbortError") return;
      statusLine.textContent = t("storage.google_connect_failed", { reason: localizeApiError(err) });
    } finally {
      googleConnectBtn.disabled = false;
    }
  });

  dropboxConnectBtn.addEventListener("click", async () => {
    const name = dropboxNameInput.value.trim();
    const appKey = dropboxAppKeyInput.value.trim();
    if (!name || !appKey) {
      statusLine.textContent = t("storage.dropbox_fields_required");
      return;
    }
    dropboxConnectBtn.disabled = true;
    try {
      const account = await createStorageAccount({ provider: "dropbox", name, config: { app_key: appKey } }, signal);
      const start = await startStorageAccountOAuth(account.id, signal);
      openDropboxPKCEDialog({ accountID: account.id, start, signal, announce, onChanged, container });
      dropboxNameInput.value = "";
      dropboxAppKeyInput.value = "";
      statusLine.textContent = "";
    } catch (err) {
      if (err?.name === "AbortError") return;
      statusLine.textContent = t("storage.dropbox_connect_failed", { reason: localizeApiError(err) });
    } finally {
      dropboxConnectBtn.disabled = false;
    }
  });

  // One collapsed section per provider: the form and its setup hint
  // appear only when the user picks that provider.
  card.append(disclosure({ id: "storage-connect-google", summary: t("storage.section_google_drive"), children: [googleHint, googleFields] }).node);
  card.append(disclosure({ id: "storage-connect-dropbox", summary: t("storage.section_dropbox"), children: [dropboxHint, dropboxFields] }).node);
  card.append(statusLine);
  return card;
}

// ---------------------------------------------------------------------------
// Google Drive device-flow dialog: show user_code + verification_url,
// poll oauth/complete until connected/denied/expired.
// ---------------------------------------------------------------------------

function openGoogleDeviceFlowDialog({ accountID, start, signal, announce, onChanged, setPollTimer, container }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("storage.connect_google_drive") }));
  card.append(el("p", { class: "cp-muted-small", text: t("storage.google_device_flow_steps") }));

  const linkRow = el("div", { class: "cp-settings-row" });
  linkRow.append(
    el("a", {
      attrs: { href: start.verification_url, target: "_blank", rel: "noopener noreferrer" },
      text: start.verification_url,
    }),
  );
  card.append(linkRow);

  const codeRow = el("div", { class: "cp-settings-row" });
  const codeDisplay = el("code", { class: "cp-code-inline", text: start.user_code });
  const copyCodeBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("storage.copy_code") });
  copyCodeBtn.addEventListener("click", async () => {
    await copyToClipboard(start.user_code, codeDisplay);
  });
  codeRow.append(codeDisplay, copyCodeBtn);
  card.append(codeRow);

  const statusLine = el("p", {
    class: "cp-muted-small",
    attrs: { role: "status", "aria-live": "polite" },
    text: t("storage.waiting_for_approval"),
  });
  card.append(statusLine);

  container.prepend(card);

  let attempts = 0;
  async function poll() {
    if (signal.aborted) return;
    attempts += 1;
    try {
      const resp = await completeStorageAccountOAuth(accountID, undefined, signal);
      if (resp.status === "connected") {
        statusLine.textContent = t("storage.connected");
        announce(t("storage.google_drive_connected_announce"));
        card.remove();
        await onChanged();
        return;
      }
      if (resp.status === "denied" || resp.status === "expired") {
        statusLine.textContent = resp.status === "denied" ? t("storage.access_denied") : t("storage.code_expired");
        return;
      }
      if (attempts >= GOOGLE_DEVICE_POLL_MAX_ATTEMPTS) {
        statusLine.textContent = t("storage.poll_timed_out");
        return;
      }
      setPollTimer(setTimeout(poll, GOOGLE_DEVICE_POLL_INTERVAL_MS));
    } catch (err) {
      if (err?.name === "AbortError") return;
      statusLine.textContent = t("storage.error_checking_status", { reason: localizeApiError(err) });
    }
  }
  setPollTimer(setTimeout(poll, GOOGLE_DEVICE_POLL_INTERVAL_MS));
}

// ---------------------------------------------------------------------------
// Dropbox PKCE dialog: show authorize link, accept the pasted code.
// ---------------------------------------------------------------------------

function openDropboxPKCEDialog({ accountID, start, signal, announce, onChanged, container }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: t("storage.connect_dropbox") }));
  card.append(
    el("p", { class: "cp-muted-small", text: t("storage.dropbox_pkce_steps") }),
  );

  const linkRow = el("div", { class: "cp-settings-row" });
  linkRow.append(
    el("a", {
      attrs: { href: start.authorize_url, target: "_blank", rel: "noopener noreferrer" },
      text: t("storage.open_dropbox_auth_page"),
    }),
  );
  card.append(linkRow);

  const codeRow = el("div", { class: "cp-settings-row" });
  const codeInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "text", placeholder: t("storage.paste_code_placeholder") } })
  );
  const submitBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: t("storage.submit_code") });
  codeRow.append(codeInput, submitBtn);
  card.append(codeRow);

  const statusLine = el("p", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  card.append(statusLine);

  container.prepend(card);

  submitBtn.addEventListener("click", async () => {
    const code = codeInput.value.trim();
    if (!code) {
      statusLine.textContent = t("storage.paste_code_first");
      return;
    }
    submitBtn.disabled = true;
    try {
      const resp = await completeStorageAccountOAuth(accountID, code, signal);
      if (resp.status === "connected") {
        statusLine.textContent = t("storage.connected");
        announce(t("storage.dropbox_connected_announce"));
        card.remove();
        await onChanged();
        return;
      }
      statusLine.textContent = t("storage.unexpected_status", { status: resp.status });
    } catch (err) {
      if (err?.name === "AbortError") return;
      statusLine.textContent = t("storage.complete_connection_failed", { reason: localizeApiError(err) });
    } finally {
      submitBtn.disabled = false;
    }
  });
}

// ---------------------------------------------------------------------------
// Account list card: usage bar, quota, status/freshness, disconnect
// ---------------------------------------------------------------------------

async function renderAccountListCard(card, { signal, onChanged }) {
  clearChildren(card);
  card.append(el("h2", { class: "cp-card-title", text: t("storage.connected_accounts_title") }));

  let accounts;
  try {
    const data = await getStorageAccounts(signal);
    accounts = data.accounts || [];
  } catch (err) {
    if (err?.name === "AbortError") return;
    card.append(el("p", { class: "cp-error-text", text: t("storage.failed_to_load_accounts", { reason: localizeApiError(err) }) }));
    return;
  }

  if (accounts.length === 0) {
    card.append(emptyState({ iconName: "cloud", title: t("host.storage_empty_title"), message: t("storage.connect_one_above") }));
    return;
  }

  const list = el("ul", { class: "cp-storage-account-list", attrs: { role: "list" } });
  for (const view of accounts) {
    list.append(accountRow(view, { signal, onChanged }));
  }
  card.append(list);
}

function accountRow(view, { signal, onChanged }) {
  const row = el("li", { class: "cp-storage-account-row" });
  const account = view.account;
  const snap = view.snapshot;

  const header = el("div", { class: "cp-storage-account-row-header" });
  header.append(el("strong", { text: account.name }));
  header.append(el("span", { class: "cp-muted-small", text: providerLabel(account.provider) }));

  const statusText = storageStatusLabel(snap);
  const chipClass = storageStatusChipClass(snap);
  header.append(el("span", { class: chipClass, text: statusText }));
  if (snap?.stale) {
    header.append(el("span", { class: "cp-chip cp-chip-warning", text: t("host.stale") }));
  }
  row.append(header);

  if (snap?.status === "ok") {
    const quota = snap.quota;
    if (quota.unlimited) {
      row.append(el("span", { class: "cp-muted-small", text: t("host.storage_used_unlimited", { amount: formatBytes(quota.used_bytes) }) }));
    } else {
      const pct = storageUsedPercent(quota);
      row.append(
        progressBar({
          value: quota.used_bytes,
          max: quota.limit_bytes,
          label: t("host.storage_usage_label", { name: account.name }),
          levelClass: thresholdBarClass(pct),
          valueText: `${formatBytes(quota.used_bytes)} / ${formatBytes(quota.limit_bytes)} (${formatPercent(pct)})`,
        }),
      );
    }
    if (snap.account_email) {
      row.append(el("p", { class: "cp-muted-small", text: snap.account_email }));
    }
    if (snap.last_success_at) {
      row.append(
        el("p", { class: "cp-muted-small", text: t("host.storage_last_checked", { relative: formatRelativeTimeFromUnixSeconds(snap.last_success_at) }) }),
      );
    }
  } else if (snap?.status_detail) {
    row.append(el("p", { class: "cp-muted-small", text: snap.status_detail }));
  }

  const actions = el("div", { class: "cp-settings-row" });
  const disconnectBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("storage.disconnect") });
  disconnectBtn.addEventListener("click", async () => {
    if (!globalThis.confirm(t("storage.confirm_disconnect", { name: account.name }))) return;
    disconnectBtn.disabled = true;
    await withToastOnError(async () => {
      await deleteStorageAccount(account.id, signal);
      await onChanged();
    }, t("storage.disconnect_failed_prefix"));
    disconnectBtn.disabled = false;
  });
  actions.append(disconnectBtn);
  row.append(actions);

  return row;
}

/**
 * setStorageIntervalWithToast wraps setStorageInterval with the shared
 * error-toast helper (exported for a future interval-picker control —
 * SPEC-v0.7 §3's polling cadence is hub-wide, not surfaced as a
 * per-account control on this page).
 * @param {"15m"|"1h"|"6h"|"24h"} interval
 */
export async function setStorageIntervalWithToast(interval, signal) {
  return withToastOnError(() => setStorageInterval(interval, signal), t("storage.set_interval_failed_prefix"));
}
