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
} from "../../core/api.js";
import { copyToClipboard } from "./index.js";

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
  card.append(el("h2", { class: "cp-card-title", text: "Connect a storage account" }));
  card.append(
    el("p", {
      class: "cp-muted-small",
      text: "Monitor a Google Drive or Dropbox account's storage usage alongside your S3/R2 buckets. Requires an OAuth app you've registered with that provider — see the field hints below.",
    }),
  );

  const googleNameInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "text", placeholder: "Label (e.g. \"Team Drive\")" } })
  );
  const googleClientIDInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "text", placeholder: "Google OAuth client ID" } })
  );
  const googleClientSecretInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "password", placeholder: "Google OAuth client secret" } })
  );
  const googleConnectBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: "Connect Google Drive" });
  const googleFields = el("div", { class: "cp-settings-row" });
  googleFields.append(googleNameInput, googleClientIDInput, googleClientSecretInput, googleConnectBtn);
  const googleHint = el("p", {
    class: "cp-muted-small",
    text: 'Google Cloud Console → APIs & Services → Credentials → create an OAuth client of type "TVs and Limited Input devices".',
  });

  const dropboxNameInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "text", placeholder: "Label (e.g. \"Personal Dropbox\")" } })
  );
  const dropboxAppKeyInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "text", placeholder: "Dropbox app key" } })
  );
  const dropboxConnectBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: "Connect Dropbox" });
  const dropboxFields = el("div", { class: "cp-settings-row" });
  dropboxFields.append(dropboxNameInput, dropboxAppKeyInput, dropboxConnectBtn);
  const dropboxHint = el("p", {
    class: "cp-muted-small",
    text: "Dropbox App Console → create an app with the account_info.read scope; only the App key is needed (PKCE requires no app secret).",
  });

  const statusLine = el("p", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });

  googleConnectBtn.addEventListener("click", async () => {
    const name = googleNameInput.value.trim();
    const clientID = googleClientIDInput.value.trim();
    const clientSecret = googleClientSecretInput.value;
    if (!name || !clientID || !clientSecret) {
      statusLine.textContent = "Label, client ID, and client secret are all required.";
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
      statusLine.textContent = `Failed to start Google Drive connection: ${err.message}`;
    } finally {
      googleConnectBtn.disabled = false;
    }
  });

  dropboxConnectBtn.addEventListener("click", async () => {
    const name = dropboxNameInput.value.trim();
    const appKey = dropboxAppKeyInput.value.trim();
    if (!name || !appKey) {
      statusLine.textContent = "Label and app key are both required.";
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
      statusLine.textContent = `Failed to start Dropbox connection: ${err.message}`;
    } finally {
      dropboxConnectBtn.disabled = false;
    }
  });

  card.append(googleFields);
  card.append(googleHint);
  card.append(dropboxFields);
  card.append(dropboxHint);
  card.append(statusLine);
  return card;
}

// ---------------------------------------------------------------------------
// Google Drive device-flow dialog: show user_code + verification_url,
// poll oauth/complete until connected/denied/expired.
// ---------------------------------------------------------------------------

function openGoogleDeviceFlowDialog({ accountID, start, signal, announce, onChanged, setPollTimer, container }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: "Connect Google Drive" }));
  card.append(el("p", { class: "cp-muted-small", text: "1. Open the link below. 2. Enter the code. 3. Approve access." }));

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
  const copyCodeBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Copy code" });
  copyCodeBtn.addEventListener("click", async () => {
    await copyToClipboard(start.user_code, codeDisplay);
  });
  codeRow.append(codeDisplay, copyCodeBtn);
  card.append(codeRow);

  const statusLine = el("p", {
    class: "cp-muted-small",
    attrs: { role: "status", "aria-live": "polite" },
    text: "Waiting for you to approve access…",
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
        statusLine.textContent = "Connected.";
        announce("Google Drive account connected.");
        card.remove();
        await onChanged();
        return;
      }
      if (resp.status === "denied" || resp.status === "expired") {
        statusLine.textContent = resp.status === "denied" ? "Access denied." : "Code expired — reconnect to try again.";
        return;
      }
      if (attempts >= GOOGLE_DEVICE_POLL_MAX_ATTEMPTS) {
        statusLine.textContent =
          "Timed out waiting in this browser tab — the account may still connect in the background; reload this page to check.";
        return;
      }
      setPollTimer(setTimeout(poll, GOOGLE_DEVICE_POLL_INTERVAL_MS));
    } catch (err) {
      if (err?.name === "AbortError") return;
      statusLine.textContent = `Error checking connection status: ${err.message}`;
    }
  }
  setPollTimer(setTimeout(poll, GOOGLE_DEVICE_POLL_INTERVAL_MS));
}

// ---------------------------------------------------------------------------
// Dropbox PKCE dialog: show authorize link, accept the pasted code.
// ---------------------------------------------------------------------------

function openDropboxPKCEDialog({ accountID, start, signal, announce, onChanged, container }) {
  const card = el("div", { class: "cp-card" });
  card.append(el("h2", { class: "cp-card-title", text: "Connect Dropbox" }));
  card.append(
    el("p", { class: "cp-muted-small", text: "1. Open the link below and approve access. 2. Paste the resulting code here." }),
  );

  const linkRow = el("div", { class: "cp-settings-row" });
  linkRow.append(
    el("a", {
      attrs: { href: start.authorize_url, target: "_blank", rel: "noopener noreferrer" },
      text: "Open Dropbox authorization page",
    }),
  );
  card.append(linkRow);

  const codeRow = el("div", { class: "cp-settings-row" });
  const codeInput = /** @type {HTMLInputElement} */ (
    el("input", { class: "cp-input", attrs: { type: "text", placeholder: "Paste the code Dropbox shows you" } })
  );
  const submitBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: "Submit code" });
  codeRow.append(codeInput, submitBtn);
  card.append(codeRow);

  const statusLine = el("p", { class: "cp-muted-small", attrs: { role: "status", "aria-live": "polite" } });
  card.append(statusLine);

  container.prepend(card);

  submitBtn.addEventListener("click", async () => {
    const code = codeInput.value.trim();
    if (!code) {
      statusLine.textContent = "Paste the code Dropbox showed you first.";
      return;
    }
    submitBtn.disabled = true;
    try {
      const resp = await completeStorageAccountOAuth(accountID, code, signal);
      if (resp.status === "connected") {
        statusLine.textContent = "Connected.";
        announce("Dropbox account connected.");
        card.remove();
        await onChanged();
        return;
      }
      statusLine.textContent = `Unexpected status: ${resp.status}`;
    } catch (err) {
      if (err?.name === "AbortError") return;
      statusLine.textContent = `Failed to complete connection: ${err.message}`;
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
  card.append(el("h2", { class: "cp-card-title", text: "Connected accounts" }));

  let accounts;
  try {
    const data = await getStorageAccounts(signal);
    accounts = data.accounts || [];
  } catch (err) {
    if (err?.name === "AbortError") return;
    card.append(el("p", { class: "cp-error-text", text: `Failed to load storage accounts: ${err.message}` }));
    return;
  }

  if (accounts.length === 0) {
    card.append(emptyState({ iconName: "cloud", title: "No storage accounts connected", message: "Connect one above." }));
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
    header.append(el("span", { class: "cp-chip cp-chip-warning", text: "Stale" }));
  }
  row.append(header);

  if (snap?.status === "ok") {
    const quota = snap.quota;
    if (quota.unlimited) {
      row.append(el("span", { class: "cp-muted-small", text: `${formatBytes(quota.used_bytes)} used (unlimited)` }));
    } else {
      const pct = storageUsedPercent(quota);
      row.append(
        progressBar({
          value: quota.used_bytes,
          max: quota.limit_bytes,
          label: `${account.name} storage usage`,
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
        el("p", { class: "cp-muted-small", text: `Last checked ${formatRelativeTimeFromUnixSeconds(snap.last_success_at)}` }),
      );
    }
  } else if (snap?.status_detail) {
    row.append(el("p", { class: "cp-muted-small", text: snap.status_detail }));
  }

  const actions = el("div", { class: "cp-settings-row" });
  const disconnectBtn = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: "Disconnect" });
  disconnectBtn.addEventListener("click", async () => {
    if (!globalThis.confirm(`Disconnect "${account.name}"? This revokes its stored access token.`)) return;
    disconnectBtn.disabled = true;
    await withToastOnError(async () => {
      await deleteStorageAccount(account.id, signal);
      await onChanged();
    }, "Failed to disconnect account");
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
  return withToastOnError(() => setStorageInterval(interval, signal), "Failed to set storage polling interval");
}
