// api.js — fetch wrapper for the cloud-pulse hub REST API (v0.4+
// session-based auth). Attaches `Authorization: Bearer <cp_session>`
// from core/auth.js. On 401, clears the session and redirects to
// #/login (with a toast, distinguishing "session expired" from a
// generic auth failure); on 403 password_change_required, invokes a
// registered callback so the shell can show the forced change-password
// modal without api.js needing to know about DOM/dialogs; on 429,
// throws a RateLimitError carrying retry_after_seconds so callers (the
// login page) can render a countdown.
import { getSessionToken, clearSessionToken, buildLoginRedirect } from "./auth.js";
import { showToast } from "../ui/toast.js";

/** ApiError is thrown for non-2xx responses. */
export class ApiError extends Error {
  /**
   * @param {string} message
   * @param {number} status
   * @param {string} [code] machine-readable error code from the body's
   *   {"code": "..."} field, "" if absent/unparseable.
   * @param {Object<string,string>} [details] field-level validation
   *   errors from the body's {"details": {...}} map, undefined if
   *   absent (see models.APIError.Details).
   */
  constructor(message, status, code = "", details = undefined) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

/** RateLimitError extends ApiError for a 429 response, exposing the
 * server's suggested retry delay. */
export class RateLimitError extends ApiError {
  /**
   * @param {string} message
   * @param {number} retryAfterSeconds
   */
  constructor(message, retryAfterSeconds) {
    super(message, 429, "rate_limited");
    this.name = "RateLimitError";
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

/**
 * @callback PasswordChangeRequiredHandler
 * @returns {void}
 */

/** @type {PasswordChangeRequiredHandler|null} */
let passwordChangeHandler = null;

/**
 * onPasswordChangeRequired registers the callback invoked when any API
 * request receives 403 {code: "password_change_required"}. The shell
 * wires this to opening the forced change-password modal. Call once
 * during bootstrap.
 * @param {PasswordChangeRequiredHandler} handler
 */
export function onPasswordChangeRequired(handler) {
  passwordChangeHandler = handler;
}

/**
 * @callback UnauthorizedHandler
 * @param {{sessionExpired: boolean}} info
 * @returns {void}
 */

/** @type {UnauthorizedHandler|null} */
let unauthorizedHandler = null;

/**
 * onUnauthorized registers the callback invoked when any API request
 * receives 401. The shell wires this to redirecting to #/login (with a
 * return-to hash) and showing a toast. Call once during bootstrap.
 * @param {UnauthorizedHandler} handler
 */
export function onUnauthorized(handler) {
  unauthorizedHandler = handler;
}

/**
 * parseErrorBody best-effort parses a models.APIError JSON body,
 * returning {message, code, retryAfterSeconds, details}. Falls back to
 * a generic message when the body isn't JSON or lacks the expected
 * shape.
 * @param {Response} res
 * @returns {Promise<{message: string, code: string, retryAfterSeconds: number, details: Object<string,string>|undefined}>}
 */
async function parseErrorBody(res) {
  let message = `request failed: ${res.status}`;
  let code = "";
  let retryAfterSeconds = 0;
  let details;
  try {
    const body = await res.json();
    if (body && typeof body.error === "string") message = body.error;
    if (body && typeof body.code === "string") code = body.code;
    if (body && typeof body.retry_after_seconds === "number") retryAfterSeconds = body.retry_after_seconds;
    if (body && body.details && typeof body.details === "object") details = body.details;
  } catch {
    // Non-JSON error body: keep the generic message/empty code.
  }
  if (!retryAfterSeconds) {
    const header = res.headers.get("Retry-After");
    const parsed = header ? Number(header) : NaN;
    if (Number.isFinite(parsed) && parsed > 0) retryAfterSeconds = parsed;
  }
  return { message, code, retryAfterSeconds, details };
}

/**
 * apiFetch performs a JSON request against the hub API, attaching the
 * session bearer token if one is set. Handles 401 (clear session,
 * notify shell, throw), 403 password_change_required (notify shell,
 * throw), and 429 (throw RateLimitError). Any other non-2xx response
 * throws a plain ApiError.
 * @param {string} path e.g. "/api/v1/hosts"
 * @param {RequestInit} [init]
 * @returns {Promise<any>}
 */
export async function apiFetch(path, init = {}) {
  const headers = new Headers(init.headers || {});
  const tok = getSessionToken();
  if (tok) headers.set("Authorization", `Bearer ${tok}`);

  const res = await fetch(path, { ...init, headers });

  if (res.status === 401) {
    const { message, code } = await parseErrorBody(res);
    clearSessionToken();
    unauthorizedHandler?.({ sessionExpired: code === "session_expired" });
    throw new ApiError(message, 401, code || "unauthorized");
  }

  if (res.status === 403) {
    const { message, code } = await parseErrorBody(res);
    if (code === "password_change_required") {
      passwordChangeHandler?.();
    }
    throw new ApiError(message, 403, code);
  }

  if (res.status === 429) {
    const { message, retryAfterSeconds } = await parseErrorBody(res);
    throw new RateLimitError(message || "rate limited", retryAfterSeconds);
  }

  if (!res.ok) {
    const { message, code, details } = await parseErrorBody(res);
    throw new ApiError(message, res.status, code, details);
  }

  if (res.status === 204) return null;
  return res.json();
}

/**
 * unauthenticatedFetch performs a JSON request without attaching a
 * bearer token or triggering the 401/403 handlers above — used only for
 * POST /api/v1/auth/login, which is unauthenticated by design and
 * handles its own error responses (invalid_credentials, rate_limited)
 * inline on the login page.
 * @param {string} path
 * @param {RequestInit} [init]
 * @returns {Promise<any>}
 */
export async function unauthenticatedFetch(path, init = {}) {
  const res = await fetch(path, init);
  if (res.status === 429) {
    const { message, retryAfterSeconds } = await parseErrorBody(res);
    throw new RateLimitError(message || "rate limited", retryAfterSeconds);
  }
  if (!res.ok) {
    const { message, code } = await parseErrorBody(res);
    throw new ApiError(message, res.status, code);
  }
  return res.json();
}

// ---------------------------------------------------------------------------
// Auth endpoints
// ---------------------------------------------------------------------------

/** login calls POST /api/v1/auth/login. Returns models.LoginResponse. */
export function login(username, password, signal) {
  return unauthenticatedFetch("/api/v1/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
    signal,
  });
}

/** logout calls POST /api/v1/auth/logout. */
export function logout(signal) {
  return apiFetch("/api/v1/auth/logout", { method: "POST", signal });
}

/** getMe calls GET /api/v1/auth/me. Returns models.MeResponse. */
export function getMe(signal) {
  return apiFetch("/api/v1/auth/me", { signal });
}

/** changePassword calls POST /api/v1/auth/password. Returns models.LoginResponse. */
export function changePassword(currentPassword, newPassword, signal) {
  return apiFetch("/api/v1/auth/password", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
    signal,
  });
}

/** getSessions calls GET /api/v1/auth/sessions. Returns models.SessionView[]. */
export function getSessions(signal) {
  return apiFetch("/api/v1/auth/sessions", { signal });
}

/** revokeOtherSessions calls POST /api/v1/auth/sessions/revoke-others. */
export function revokeOtherSessions(signal) {
  return apiFetch("/api/v1/auth/sessions/revoke-others", { method: "POST", signal });
}

// ---------------------------------------------------------------------------
// Dashboard data endpoints (unchanged from v0.3.x, now session-authed)
// ---------------------------------------------------------------------------

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

/** getSettings fetches GET /api/v1/settings (admin). */
export function getSettings(signal) {
  return apiFetch("/api/v1/settings", { signal });
}

/** getAgentToken fetches GET /api/v1/settings/agent-token (admin). */
export function getAgentToken(signal) {
  return apiFetch("/api/v1/settings/agent-token", { signal });
}

/**
 * setHostLimits calls PUT /api/v1/hosts/{id}/limits (admin) with the
 * given override values (number or null; null clears that override).
 * @param {string} hostID
 * @param {{egressLimitBytes: number|null, ingressLimitBytes: number|null}} limits
 * @param {AbortSignal} [signal]
 */
export function setHostLimits(hostID, { egressLimitBytes, ingressLimitBytes }, signal) {
  return apiFetch(`/api/v1/hosts/${encodeURIComponent(hostID)}/limits`, {
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
  return apiFetch("/api/v1/settings/alerts", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ webhook_url: webhookURL }),
    signal,
  });
}

/** testAlertWebhook calls POST /api/v1/settings/alerts/test (admin). */
export function testAlertWebhook(signal) {
  return apiFetch("/api/v1/settings/alerts/test", { method: "POST", signal });
}

// ---------------------------------------------------------------------------
// Alerting (SPEC-v0.5 §B/§D): notify channels, alert rules, events.
// ---------------------------------------------------------------------------

/** getNotifyChannels fetches GET /api/v1/alerts/channels (admin);
 * secrets are redacted by the hub. */
export function getNotifyChannels(signal) {
  return apiFetch("/api/v1/alerts/channels", { signal });
}

/**
 * createNotifyChannel calls POST /api/v1/alerts/channels (admin).
 * @param {{name: string, type: string, enabled: boolean, config: Object<string,string>}} channel
 */
export function createNotifyChannel(channel, signal) {
  return apiFetch("/api/v1/alerts/channels", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(channel),
    signal,
  });
}

/**
 * updateNotifyChannel calls PUT /api/v1/alerts/channels/{id} (admin). A
 * secret Config field omitted (or "***") preserves the stored value.
 * @param {number} id
 * @param {{name: string, type: string, enabled: boolean, config: Object<string,string>}} channel
 */
export function updateNotifyChannel(id, channel, signal) {
  return apiFetch(`/api/v1/alerts/channels/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(channel),
    signal,
  });
}

/** deleteNotifyChannel calls DELETE /api/v1/alerts/channels/{id} (admin). */
export function deleteNotifyChannel(id, signal) {
  return apiFetch(`/api/v1/alerts/channels/${encodeURIComponent(id)}`, { method: "DELETE", signal });
}

/** testNotifyChannel calls POST /api/v1/alerts/channels/{id}/test
 * (admin) for a saved channel. Returns {ok: boolean, error?: string}. */
export function testNotifyChannel(id, signal) {
  return apiFetch(`/api/v1/alerts/channels/${encodeURIComponent(id)}/test`, { method: "POST", signal });
}

/** testDraftNotifyChannel calls POST /api/v1/alerts/channels/test
 * (admin) with an unsaved channel configuration from the "Add channel"
 * dialog. Returns {ok: boolean, error?: string}. */
export function testDraftNotifyChannel(channel, signal) {
  return apiFetch("/api/v1/alerts/channels/test", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(channel),
    signal,
  });
}

/** getAlertRules fetches GET /api/v1/alerts/rules (admin). */
export function getAlertRules(signal) {
  return apiFetch("/api/v1/alerts/rules", { signal });
}

/**
 * createAlertRule calls POST /api/v1/alerts/rules (admin).
 * @param {Object} rule a ruleRequest-shaped object (see
 *   internal/hub/alertroutes.go's ruleRequest)
 */
export function createAlertRule(rule, signal) {
  return apiFetch("/api/v1/alerts/rules", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(rule),
    signal,
  });
}

/**
 * updateAlertRule calls PUT /api/v1/alerts/rules/{id} (admin).
 * @param {number} id
 * @param {Object} rule
 */
export function updateAlertRule(id, rule, signal) {
  return apiFetch(`/api/v1/alerts/rules/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(rule),
    signal,
  });
}

/** deleteAlertRule calls DELETE /api/v1/alerts/rules/{id} (admin). */
export function deleteAlertRule(id, signal) {
  return apiFetch(`/api/v1/alerts/rules/${encodeURIComponent(id)}`, { method: "DELETE", signal });
}

/** previewAlertRule calls POST /api/v1/alerts/rules/{id}/preview
 * (admin). Returns a {[hostID]: boolean} map of "would fire now". */
export function previewAlertRule(id, signal) {
  return apiFetch(`/api/v1/alerts/rules/${encodeURIComponent(id)}/preview`, { method: "POST", signal });
}

/**
 * getAlertEvents fetches GET /api/v1/alerts/events with optional
 * filters. Returns {events: models.AlertEvent[]}.
 * @param {{state?: string, host?: string, limit?: number, before?: number}} [params]
 */
export function getAlertEvents(params = {}, signal) {
  const qs = new URLSearchParams();
  if (params.state) qs.set("state", params.state);
  if (params.host) qs.set("host", params.host);
  if (params.limit) qs.set("limit", String(params.limit));
  if (params.before) qs.set("before", String(params.before));
  const suffix = qs.toString() ? `?${qs}` : "";
  return apiFetch(`/api/v1/alerts/events${suffix}`, { signal });
}

/** getActiveAlerts fetches GET /api/v1/alerts/active. Returns
 * {events: models.AlertEvent[]}. Used by the navbar bell. */
export function getActiveAlerts(signal) {
  return apiFetch("/api/v1/alerts/active", { signal });
}

// ---------------------------------------------------------------------------
// Inventory (SPEC-v0.5 §C/§D): Docker containers + listening ports.
// ---------------------------------------------------------------------------

/** getHostInventory fetches GET /api/v1/hosts/{id}/inventory. Throws
 * ApiError{status:404, code:"no_inventory"} when the host has never
 * reported one. */
export function getHostInventory(id, signal) {
  return apiFetch(`/api/v1/hosts/${encodeURIComponent(id)}/inventory`, { signal });
}


// ---------------------------------------------------------------------------
// Network settings (SPEC-v0.4 §2) — thin passthroughs for the settings
// page's Network section (implemented by a later stage's JS, api.js
// just exposes the endpoints so it doesn't need its own fetch wiring).
// ---------------------------------------------------------------------------

/** getNetworkState fetches GET /api/v1/settings/network (admin). */
export function getNetworkState(signal) {
  return apiFetch("/api/v1/settings/network", { signal });
}

/** putNetworkConfig calls PUT /api/v1/settings/network (admin). */
export function putNetworkConfig(config, signal) {
  return apiFetch("/api/v1/settings/network", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(config),
    signal,
  });
}

/** confirmNetworkChange calls POST /api/v1/settings/network/confirm (admin). */
export function confirmNetworkChange(signal) {
  return apiFetch("/api/v1/settings/network/confirm", { method: "POST", signal });
}

/** revertNetworkChange calls POST /api/v1/settings/network/revert (admin). */
export function revertNetworkChange(signal) {
  return apiFetch("/api/v1/settings/network/revert", { method: "POST", signal });
}

/** deleteNetworkConfig calls DELETE /api/v1/settings/network (admin). */
export function deleteNetworkConfig(signal) {
  return apiFetch("/api/v1/settings/network", { method: "DELETE", signal });
}

// ---------------------------------------------------------------------------
// Cloud billing + network cost estimate (SPEC-v0.6 §1/§3).
// ---------------------------------------------------------------------------

/** getBilling fetches GET /api/v1/billing. Returns models.BillingView. */
export function getBilling(signal) {
  return apiFetch("/api/v1/billing", { signal });
}

/** refreshBilling calls POST /api/v1/billing/refresh (admin); throws
 * RateLimitError if called within the 10-minute throttle window. */
export function refreshBilling(signal) {
  return apiFetch("/api/v1/billing/refresh", { method: "POST", signal });
}

/**
 * setBillingInterval calls PUT /api/v1/settings/billing/interval
 * (admin). interval must be "6h", "12h", or "24h".
 * @param {"6h"|"12h"|"24h"} interval
 */
export function setBillingInterval(interval, signal) {
  return apiFetch("/api/v1/settings/billing/interval", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ interval }),
    signal,
  });
}

/** getPricingPlans fetches GET /api/v1/billing/plans. Returns
 * models.PricingPlan[]. */
export function getPricingPlans(signal) {
  return apiFetch("/api/v1/billing/plans", { signal });
}

/**
 * createPricingPlan calls POST /api/v1/billing/plans (admin).
 * @param {Object} plan a pricingPlanRequest-shaped object (see
 *   internal/hub/pricingroutes.go's pricingPlanRequest)
 */
export function createPricingPlan(plan, signal) {
  return apiFetch("/api/v1/billing/plans", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(plan),
    signal,
  });
}

/**
 * updatePricingPlan calls PUT /api/v1/billing/plans/{id} (admin).
 * @param {number} id
 * @param {Object} plan
 */
export function updatePricingPlan(id, plan, signal) {
  return apiFetch(`/api/v1/billing/plans/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(plan),
    signal,
  });
}

/** deletePricingPlan calls DELETE /api/v1/billing/plans/{id} (admin). */
export function deletePricingPlan(id, signal) {
  return apiFetch(`/api/v1/billing/plans/${encodeURIComponent(id)}`, { method: "DELETE", signal });
}

/** setHostPricing calls PUT /api/v1/hosts/{id}/pricing (admin);
 * planID 0 clears the assignment, reverting to the provider default. */
export function setHostPricing(hostID, planID, signal) {
  return apiFetch(`/api/v1/hosts/${encodeURIComponent(hostID)}/pricing`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ plan_id: planID }),
    signal,
  });
}

/** getNetworkBilling fetches GET /api/v1/billing/network?month=. */
export function getNetworkBilling(month, signal) {
  const qs = month ? `?${new URLSearchParams({ month })}` : "";
  return apiFetch(`/api/v1/billing/network${qs}`, { signal });
}

/** getDisplayCurrency fetches GET /api/v1/settings/billing/currency
 * (admin). Returns models.DisplayCurrencySettings. */
export function getDisplayCurrency(signal) {
  return apiFetch("/api/v1/settings/billing/currency", { signal });
}

/**
 * setDisplayCurrency calls PUT /api/v1/settings/billing/currency
 * (admin). Selecting "KRW" without a positive krwPerUSD is rejected by
 * the hub with 400 {code: "rate_required"}.
 * @param {"USD"|"KRW"} currency
 * @param {number} [krwPerUSD]
 */
export function setDisplayCurrency(currency, krwPerUSD, signal) {
  return apiFetch("/api/v1/settings/billing/currency", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ currency, krw_per_usd: krwPerUSD || 0 }),
    signal,
  });
}

// ---------------------------------------------------------------------------
// Remote agent updates (SPEC-v0.6 §2).
// ---------------------------------------------------------------------------

/** getUpdateJobs fetches GET /api/v1/agents/updates?batch= (admin).
 * Omit batchID to list every job. Returns {jobs: models.UpdateJob[]}. */
export function getUpdateJobs(batchID, signal) {
  const qs = batchID ? `?${new URLSearchParams({ batch: batchID })}` : "";
  return apiFetch(`/api/v1/agents/updates${qs}`, { signal });
}

/**
 * createUpdateBatch calls POST /api/v1/agents/updates (admin). Returns
 * models.UpdateBatch.
 * @param {{hostIDs: string[], target: string, maxParallel?: number}} params
 */
export function createUpdateBatch({ hostIDs, target, maxParallel }, signal) {
  return apiFetch("/api/v1/agents/updates", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ host_ids: hostIDs, target, max_parallel: maxParallel || 0 }),
    signal,
  });
}

/** retryUpdateJob calls POST /api/v1/agents/updates/{jobID}/retry
 * (admin). Returns the newly created models.UpdateJob. */
export function retryUpdateJob(jobID, signal) {
  return apiFetch(`/api/v1/agents/updates/${encodeURIComponent(jobID)}/retry`, { method: "POST", signal });
}

/** cancelUpdateBatch calls POST /api/v1/agents/updates/{batchID}/cancel
 * (admin). Returns {canceled: number}. */
export function cancelUpdateBatch(batchID, signal) {
  return apiFetch(`/api/v1/agents/updates/${encodeURIComponent(batchID)}/cancel`, { method: "POST", signal });
}

// ---------------------------------------------------------------------------
// Audit log (SPEC-v0.6 §3 개선 c).
// ---------------------------------------------------------------------------

/**
 * getAuditEntries fetches GET /api/v1/audit with optional filters
 * (admin). Returns models.AuditListView.
 * @param {{entityType?: string, limit?: number, before?: number}} [params]
 */
export function getAuditEntries(params = {}, signal) {
  const qs = new URLSearchParams();
  if (params.entityType) qs.set("entity_type", params.entityType);
  if (params.limit) qs.set("limit", String(params.limit));
  if (params.before) qs.set("before", String(params.before));
  const suffix = qs.toString() ? `?${qs}` : "";
  return apiFetch(`/api/v1/audit${suffix}`, { signal });
}

/**
 * withToastOnError wraps an async action, showing an error toast for
 * any thrown ApiError/RateLimitError that isn't already handled by the
 * 401/403 global hooks (which don't throw a *visible* toast themselves
 * for 401 — see bootstrap's unauthorizedHandler). Useful for settings
 * page mutations where the caller wants a toast without repeating
 * try/catch boilerplate.
 * @param {() => Promise<any>} action
 * @param {string} [failureMessagePrefix]
 * @returns {Promise<any|undefined>} the action's result, or undefined if it threw
 */
export async function withToastOnError(action, failureMessagePrefix = "Action failed") {
  try {
    return await action();
  } catch (err) {
    if (err?.name === "AbortError") return undefined;
    const msg = err instanceof ApiError ? err.message : String(err?.message || err);
    showToast({ message: `${failureMessagePrefix}: ${msg}`, variant: "error" });
    return undefined;
  }
}
