// theme.js — theme preference (system|dark|light) persisted to
// localStorage "cp_theme", applied to <html class="dark">. Mirrors
// theme-init.js's resolution rule exactly (see that file's header
// comment for why the two can't share code directly); this module is
// the source of truth used after bootstrap (toggle clicks, "system"
// media-query changes, chart re-render hooks).
const STORAGE_KEY = "cp_theme";

/** @typedef {"system"|"dark"|"light"} ThemePreference */

/**
 * getStoredTheme returns the persisted theme preference, defaulting to
 * "system" when unset or localStorage is unavailable.
 * @returns {ThemePreference}
 */
export function getStoredTheme() {
  try {
    const v = globalThis.localStorage?.getItem(STORAGE_KEY);
    if (v === "dark" || v === "light" || v === "system") return v;
  } catch {
    // localStorage unavailable: fall through to the default.
  }
  return "system";
}

/**
 * setStoredTheme persists pref as the theme preference.
 * @param {ThemePreference} pref
 */
export function setStoredTheme(pref) {
  try {
    globalThis.localStorage?.setItem(STORAGE_KEY, pref);
  } catch {
    // No persistence available; applyTheme still updates the current
    // page's appearance for this session.
  }
}

/**
 * resolveIsDark computes whether dark mode should be active for a given
 * preference, resolving "system" via the prefers-color-scheme media
 * query. Pure aside from the media-query read, so it's easy to reason
 * about/test with an injected matchMedia result.
 * @param {ThemePreference} pref
 * @param {boolean} [systemPrefersDark] override for tests; defaults to
 *   reading matchMedia when available.
 * @returns {boolean}
 */
export function resolveIsDark(pref, systemPrefersDark) {
  if (pref === "dark") return true;
  if (pref === "light") return false;
  if (systemPrefersDark !== undefined) return systemPrefersDark;
  try {
    return Boolean(globalThis.matchMedia?.("(prefers-color-scheme: dark)").matches);
  } catch {
    return false;
  }
}

/**
 * applyTheme sets/removes the "dark" class on <html> for pref, and
 * dispatches a "cp-theme-change" event on window (isDark in detail) so
 * chart code can re-read CSS variable colors and re-render.
 * @param {ThemePreference} pref
 */
export function applyTheme(pref) {
  const isDark = resolveIsDark(pref);
  const root = globalThis.document?.documentElement;
  if (root) {
    root.classList.toggle("dark", isDark);
  }
  try {
    globalThis.dispatchEvent(new CustomEvent("cp-theme-change", { detail: { isDark, pref } }));
  } catch {
    // CustomEvent unavailable in a non-browser test environment; the
    // class toggle above already happened, which is what matters for
    // rendering.
  }
}

/**
 * initThemeWatcher applies the stored preference once and, when pref is
 * "system", re-applies on OS theme changes. Returns an unsubscribe
 * function. Call once during bootstrap.
 * @returns {() => void}
 */
export function initThemeWatcher() {
  applyTheme(getStoredTheme());

  let mq;
  try {
    mq = globalThis.matchMedia?.("(prefers-color-scheme: dark)");
  } catch {
    mq = null;
  }
  if (!mq) return () => {};

  const onChange = () => {
    if (getStoredTheme() === "system") applyTheme("system");
  };
  mq.addEventListener?.("change", onChange);
  return () => mq.removeEventListener?.("change", onChange);
}

/**
 * cycleTheme returns the next preference in the system → light → dark →
 * system cycle used by the navbar toggle button.
 * @param {ThemePreference} current
 * @returns {ThemePreference}
 */
export function cycleTheme(current) {
  if (current === "system") return "light";
  if (current === "light") return "dark";
  return "system";
}
