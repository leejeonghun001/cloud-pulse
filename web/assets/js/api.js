// api.js — fetch wrapper for the cloud-pulse hub REST API. Adds the
// Authorization header from the token stored in localStorage under
// "cp_ui_token", and on a 401 response shows a native <dialog> asking
// for a token, then retries the original request once with the new
// token. No DOM building beyond the token dialog lives here; page
// rendering stays in components.js/main.js.

const TOKEN_STORAGE_KEY = "cp_ui_token";

/** ApiError is thrown for non-2xx responses (after any 401 retry). */
export class ApiError extends Error {
  /**
   * @param {string} message
   * @param {number} status
   */
  constructor(message, status) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

/** getToken returns the stored UI token, or "" if none is set. */
export function getToken() {
  try {
    return globalThis.localStorage.getItem(TOKEN_STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}

/** setToken persists tok as the UI token (empty string clears it). */
export function setToken(tok) {
  try {
    if (tok) {
      globalThis.localStorage.setItem(TOKEN_STORAGE_KEY, tok);
    } else {
      globalThis.localStorage.removeItem(TOKEN_STORAGE_KEY);
    }
  } catch {
    // localStorage unavailable (private mode, disabled storage): the
    // token simply won't persist across reloads; requests still work
    // for the current page lifetime via the in-memory dialog retry.
  }
}

/**
 * @typedef {Object} TokenDialogElements
 * @property {HTMLDialogElement} dialog
 * @property {HTMLFormElement} form
 * @property {HTMLInputElement} input
 * @property {HTMLElement} errorEl
 */

/** @type {TokenDialogElements | null} */
let dialogEls = null;

/**
 * registerTokenDialog wires up the token-prompt <dialog> already present
 * in index.html so promptForToken() can drive it. Call once during
 * bootstrap.
 * @param {TokenDialogElements} els
 */
export function registerTokenDialog(els) {
  dialogEls = els;
}

/**
 * promptForToken shows the token dialog and resolves with the entered
 * token, or rejects if the user cancels. Safe to call multiple times;
 * concurrent calls share the same pending prompt.
 * @returns {Promise<string>}
 */
let pendingPrompt = null;
function promptForToken() {
  if (pendingPrompt) return pendingPrompt;
  if (!dialogEls) {
    return Promise.reject(new Error("api: token dialog not registered"));
  }

  const { dialog, form, input, errorEl } = dialogEls;
  errorEl.textContent = "";
  input.value = getToken();

  pendingPrompt = new Promise((resolve, reject) => {
    /** @param {SubmitEvent} ev */
    const onSubmit = (ev) => {
      ev.preventDefault();
      const value = input.value.trim();
      if (!value) {
        errorEl.textContent = "Token is required.";
        return;
      }
      cleanup();
      dialog.close();
      resolve(value);
    };

    const onCancel = () => {
      cleanup();
      reject(new Error("api: token prompt cancelled"));
    };

    function cleanup() {
      form.removeEventListener("submit", onSubmit);
      dialog.removeEventListener("cancel", onCancel);
      dialog.removeEventListener("close", onCloseWithoutSubmit);
      pendingPrompt = null;
    }

    /** Handles dismissal via the Cancel button (dialog.close() with no submit). */
    const onCloseWithoutSubmit = () => {
      if (dialog.returnValue !== "submit") {
        cleanup();
        reject(new Error("api: token prompt cancelled"));
      }
    };

    form.addEventListener("submit", onSubmit);
    dialog.addEventListener("cancel", onCancel);
    dialog.addEventListener("close", onCloseWithoutSubmit);
  });

  if (typeof dialog.showModal === "function") {
    dialog.showModal();
  }
  return pendingPrompt;
}

/**
 * markTokenError displays an error message inside the open token dialog
 * (used when a retried request still 401s with the newly entered token).
 * @param {string} message
 */
function markTokenError(message) {
  if (dialogEls) {
    dialogEls.errorEl.textContent = message;
  }
}

/**
 * apiFetch performs a JSON request against the hub API, attaching the
 * bearer token if one is set. On 401, it prompts for a token via the
 * registered dialog and retries once. Throws ApiError on any other
 * non-2xx response, or on repeated 401 (with a message shown in the
 * dialog).
 * @param {string} path e.g. "/api/v1/hosts"
 * @param {RequestInit} [init]
 * @returns {Promise<any>}
 */
export async function apiFetch(path, init = {}) {
  const attempt = async () => {
    const headers = new Headers(init.headers || {});
    const tok = getToken();
    if (tok) headers.set("Authorization", `Bearer ${tok}`);
    return fetch(path, { ...init, headers });
  };

  let res = await attempt();

  if (res.status === 401) {
    let hadToken = Boolean(getToken());
    try {
      const newTok = await promptForToken();
      setToken(newTok);
      res = await attempt();
      if (res.status === 401) {
        markTokenError("Invalid token. Please try again.");
        setToken("");
        throw new ApiError("unauthorized", 401);
      }
    } catch (err) {
      if (err instanceof ApiError) throw err;
      // Prompt cancelled: surface the original 401 without retry.
      void hadToken;
      throw new ApiError("unauthorized", 401);
    }
  }

  if (!res.ok) {
    let message = `request failed: ${res.status}`;
    try {
      const body = await res.json();
      if (body && typeof body.error === "string") message = body.error;
    } catch {
      // Non-JSON error body: keep the generic message.
    }
    throw new ApiError(message, res.status);
  }

  if (res.status === 204) return null;
  return res.json();
}

/** getHosts fetches GET /api/v1/hosts. */
export function getHosts(signal) {
  return apiFetch("/api/v1/hosts", { signal });
}

/** getHost fetches GET /api/v1/hosts/{id}. */
export function getHost(id, signal) {
  return apiFetch(`/api/v1/hosts/${encodeURIComponent(id)}`, { signal });
}

/** getHostMetrics fetches GET /api/v1/hosts/{id}/metrics?range=. */
export function getHostMetrics(id, range, signal) {
  const qs = new URLSearchParams({ range });
  return apiFetch(`/api/v1/hosts/${encodeURIComponent(id)}/metrics?${qs}`, {
    signal,
  });
}

/** getEgress fetches GET /api/v1/egress?month=. */
export function getEgress(month, signal) {
  const qs = month ? `?${new URLSearchParams({ month })}` : "";
  return apiFetch(`/api/v1/egress${qs}`, { signal });
}

/** getBuckets fetches GET /api/v1/buckets. */
export function getBuckets(signal) {
  return apiFetch("/api/v1/buckets", { signal });
}

/** getVersion fetches GET /api/v1/version. */
export function getVersion(signal) {
  return apiFetch("/api/v1/version", { signal });
}

/**
 * AdminError extends ApiError to expose the machine-readable error code
 * (e.g. "admin_disabled") admin endpoints send alongside a 403/401.
 */
export class AdminError extends ApiError {
  /**
   * @param {string} message
   * @param {number} status
   * @param {string} [code]
   */
  constructor(message, status, code) {
    super(message, status);
    this.name = "AdminError";
    this.code = code || "";
  }
}

/**
 * adminFetch wraps apiFetch, translating a 403 {code:"admin_disabled"}
 * body into an AdminError with .code set, so callers (the settings page)
 * can distinguish "admin disabled" from "wrong/missing token" (plain
 * 401, handled by apiFetch's existing token-dialog retry) without
 * re-parsing the body themselves.
 * @param {string} path
 * @param {RequestInit} [init]
 * @returns {Promise<any>}
 */
async function adminFetch(path, init = {}) {
  try {
    return await apiFetch(path, init);
  } catch (err) {
    if (err instanceof ApiError && err.status === 403) {
      throw new AdminError(err.message, err.status, "admin_disabled");
    }
    throw err;
  }
}

/** getSettings fetches GET /api/v1/settings (admin). */
export function getSettings(signal) {
  return adminFetch("/api/v1/settings", { signal });
}

/** getAgentToken fetches GET /api/v1/settings/agent-token (admin). */
export function getAgentToken(signal) {
  return adminFetch("/api/v1/settings/agent-token", { signal });
}

/**
 * setHostLimits calls PUT /api/v1/hosts/{id}/limits (admin) with the
 * given override values (number or null; null clears that override).
 * @param {string} hostID
 * @param {{egressLimitBytes: number|null, ingressLimitBytes: number|null}} limits
 * @param {AbortSignal} [signal]
 */
export function setHostLimits(hostID, { egressLimitBytes, ingressLimitBytes }, signal) {
  return adminFetch(`/api/v1/hosts/${encodeURIComponent(hostID)}/limits`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      egress_limit_bytes: egressLimitBytes,
      ingress_limit_bytes: ingressLimitBytes,
    }),
    signal,
  });
}

/** setAlertWebhook calls PUT /api/v1/settings/alerts (admin); "" clears
 * the hub override, falling back to the env-configured URL. */
export function setAlertWebhook(webhookURL, signal) {
  return adminFetch("/api/v1/settings/alerts", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ webhook_url: webhookURL }),
    signal,
  });
}

/** testAlertWebhook calls POST /api/v1/settings/alerts/test (admin). */
export function testAlertWebhook(signal) {
  return adminFetch("/api/v1/settings/alerts/test", { method: "POST", signal });
}
