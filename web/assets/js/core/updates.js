// updates.js — pure helpers for the update banner, host card/detail
// update badges, settings hub-info panel. No DOM access here so these
// are covered by node --test without a browser environment; DOM
// building for these pieces lives in ui/components.js/pages/settings.

const DISMISSED_STORAGE_KEY = "cp_update_dismissed";

/**
 * shouldShowUpdateBanner decides whether the global update banner
 * should render, given the hub's GET /api/v1/version response and the
 * currently-dismissed tag (or null/undefined if nothing was dismissed).
 * The banner shows only when an update is available AND the available
 * version differs from whatever tag was last dismissed — dismissing
 * persists per-tag (localStorage `cp_update_dismissed`), so a newer
 * release after a dismissal reinstates the banner.
 * @param {{update_available?: boolean, latest_version?: string}} versionInfo
 * @param {string|null|undefined} dismissedTag
 * @returns {boolean}
 */
export function shouldShowUpdateBanner(versionInfo, dismissedTag) {
  if (!versionInfo || !versionInfo.update_available) return false;
  const latest = versionInfo.latest_version || "";
  if (!latest) return false;
  if (dismissedTag && dismissedTag === latest) return false;
  return true;
}

/**
 * getDismissedUpdateTag reads the persisted dismissed-tag from
 * localStorage, or "" if unset/unavailable (private mode, disabled
 * storage).
 * @returns {string}
 */
export function getDismissedUpdateTag() {
  try {
    return globalThis.localStorage.getItem(DISMISSED_STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}

/**
 * setDismissedUpdateTag persists tag as the dismissed version so the
 * banner won't reappear for it (a different/newer tag still shows).
 * @param {string} tag
 */
export function setDismissedUpdateTag(tag) {
  try {
    globalThis.localStorage.setItem(DISMISSED_STORAGE_KEY, tag);
  } catch {
    // localStorage unavailable: dismissal simply won't persist across
    // reloads for this session, matching the token-storage fallback
    // behavior elsewhere in this app.
  }
}

/**
 * bannerText builds the two display strings for the update banner: a
 * headline ("cloud-pulse vX is available — hub is running vY") and the
 * ready-to-copy update command.
 * @param {{version?: string, latest_version?: string, update_command?: string}} versionInfo
 * @returns {{headline: string, command: string}}
 */
export function bannerText(versionInfo) {
  const latest = versionInfo?.latest_version || "unknown";
  const current = versionInfo?.version || "unknown";
  return {
    headline: `cloud-pulse ${latest} is available — hub is running ${current}`,
    command: versionInfo?.update_command || "",
  };
}

/**
 * hostUpdateBadgeText builds the amber "update available" badge's
 * visible text and its title/aria-label (which names the latest
 * version), for a host summary's `update` field. Returns null when no
 * badge should render (update missing or unavailable).
 * @param {{available?: boolean, latest?: string}|null|undefined} update
 * @returns {{text: string, label: string}|null}
 */
export function hostUpdateBadgeText(update) {
  if (!update || !update.available) return null;
  const latest = update.latest || "a newer version";
  return {
    text: "update available",
    label: `A newer agent version (${latest}) is available`,
  };
}

/**
 * agentVersionText renders the small "agent vX" text shown on a host
 * card, given the host's reported agent_version (empty/unknown renders
 * a generic placeholder).
 * @param {string|null|undefined} agentVersion
 * @returns {string}
 */
export function agentVersionText(agentVersion) {
  return `agent ${agentVersion || "unknown"}`;
}

/**
 * outdatedAgentCount counts how many hosts in a hosts list have an
 * available agent update, for the optional overview summary strip item
 * ("N agents outdated").
 * @param {Array<{update?: {available?: boolean}|null}>} hosts
 * @returns {number}
 */
export function outdatedAgentCount(hosts) {
  if (!Array.isArray(hosts)) return 0;
  return hosts.reduce((n, h) => n + (h?.update?.available ? 1 : 0), 0);
}

/**
 * legacyAgentExplanation is the fixed explanatory copy shown in the host
 * detail "Agent update" panel when the agent predates the built-in
 * updater (self_update === false).
 */
export const LEGACY_AGENT_EXPLANATION =
  "This agent predates the built-in updater; run the installer once, later updates use `sudo cloud-pulse-agent update`.";

/**
 * agentUpdatePanelText builds the host-detail "Agent update" panel's
 * display strings for a host summary's `update` field: whether to show
 * the panel at all, the headline, the copyable command, and whether to
 * show the legacy explanation. Returns null when there's no update
 * signal to show (update missing) — the panel simply isn't rendered.
 * @param {{available?: boolean, latest?: string, self_update?: boolean, command?: string}|null|undefined} update
 * @returns {{headline: string, command: string, showLegacyNote: boolean}|null}
 */
export function agentUpdatePanelText(update) {
  if (!update) return null;
  const latest = update.latest || "a newer version";
  const headline = update.available
    ? `A newer agent version (${latest}) is available.`
    : "This agent is up to date.";
  return {
    headline,
    command: update.command || "",
    showLegacyNote: !update.self_update,
  };
}

/**
 * hubInfoUpdateFields builds the display strings for the settings page's
 * Hub info panel update-related rows: latest known version, last
 * checked (relative, computed by the caller from checked_at), whether
 * checking is enabled, and any check error.
 * @param {{latest_version?: string, update_available?: boolean, update_check_enabled?: boolean, checked_at?: number, check_error?: string}} versionInfo
 * @returns {{latest: string, checkEnabled: boolean, checkedAtUnixSeconds: number|null, checkError: string}}
 */
export function hubInfoUpdateFields(versionInfo) {
  return {
    latest: versionInfo?.latest_version || "unknown",
    checkEnabled: Boolean(versionInfo?.update_check_enabled),
    checkedAtUnixSeconds: versionInfo?.checked_at ? versionInfo.checked_at : null,
    checkError: versionInfo?.check_error || "",
  };
}

/** VERSION_REFRESH_INTERVAL_MS is the polling interval for
 * GET /api/v1/version driving the update banner (30 minutes). */
export const VERSION_REFRESH_INTERVAL_MS = 30 * 60 * 1000;
