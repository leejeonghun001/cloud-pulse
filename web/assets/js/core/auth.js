// auth.js — session-token storage (localStorage "cp_session", replacing
// the legacy "cp_ui_token" key from pre-v0.4) and small pure helpers for
// interpreting auth-related API responses. No fetch calls live here
// (api.js owns the request pipeline and calls back into this module's
// storage helpers) so this stays trivially unit-testable.

const SESSION_STORAGE_KEY = "cp_session";
/** LEGACY_TOKEN_KEY is the pre-v0.4 static-token storage key, removed on
 * bootstrap so a stale value never gets sent as a session bearer token. */
const LEGACY_TOKEN_KEY = "cp_ui_token";

/**
 * getSessionToken returns the stored session bearer token, or "" if
 * none is set or storage is unavailable.
 * @returns {string}
 */
export function getSessionToken() {
  try {
    return globalThis.localStorage.getItem(SESSION_STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}

/**
 * setSessionToken persists tok as the session bearer token (empty
 * string clears it).
 * @param {string} tok
 */
export function setSessionToken(tok) {
  try {
    if (tok) {
      globalThis.localStorage.setItem(SESSION_STORAGE_KEY, tok);
    } else {
      globalThis.localStorage.removeItem(SESSION_STORAGE_KEY);
    }
  } catch {
    // localStorage unavailable: the session simply won't persist
    // across reloads for this browser/profile.
  }
}

/**
 * clearSessionToken removes the stored session token (used on logout,
 * 401, etc.).
 */
export function clearSessionToken() {
  setSessionToken("");
}

/**
 * removeLegacyToken deletes the pre-v0.4 "cp_ui_token" localStorage key
 * if present. Call once during bootstrap so an old static UI token left
 * over from a v0.3.x profile is never sent as (and confused for) a
 * session bearer token.
 */
export function removeLegacyToken() {
  try {
    globalThis.localStorage.removeItem(LEGACY_TOKEN_KEY);
  } catch {
    // Nothing to clean up if storage is unavailable anyway.
  }
}

/**
 * isLoginRoute reports whether a hash-router path is the login page,
 * used to avoid redirect loops when handling a 401.
 * @param {string} hash e.g. "#/login"
 * @returns {boolean}
 */
export function isLoginRoute(hash) {
  return /^#\/login(?:\?|$)/.test(hash);
}

/**
 * buildLoginRedirect builds the "#/login?next=..." hash for redirecting
 * an unauthenticated request back to login, preserving the originally
 * requested route as a return-to target. Never wraps a route that's
 * already the login page (avoids "#/login?next=%23%2Flogin").
 * @param {string} currentHash e.g. "#/settings"
 * @returns {string}
 */
export function buildLoginRedirect(currentHash) {
  if (!currentHash || isLoginRoute(currentHash)) return "#/login";
  return `#/login?next=${encodeURIComponent(currentHash)}`;
}

/**
 * parseLoginNext extracts the "next" query param from the login route's
 * hash (e.g. "#/login?next=%23%2Fsettings" -> "#/settings"), defaulting
 * to "#/" when absent, empty, or itself pointing at the login page.
 * @param {string} hash
 * @returns {string}
 */
export function parseLoginNext(hash) {
  const qIdx = hash.indexOf("?");
  if (qIdx === -1) return "#/";
  const params = new URLSearchParams(hash.slice(qIdx + 1));
  const next = params.get("next") || "";
  if (!next || isLoginRoute(next)) return "#/";
  return next;
}
