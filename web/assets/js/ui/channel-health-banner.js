// ui/channel-health-banner.js — the app-shell banner shown when a
// notification channel is failing and no healthy channel exists to
// carry the "channel X is failing" fallback notification (SPEC-v0.8
// §3-2's "정상 채널이 없으면 대시보드 배너로 표시" requirement). A
// pure render function plus a small polling driver, mirroring
// core/updates.js's shouldShowUpdateBanner + main.js's
// renderUpdateBanner/pollVersion split so the two banners share one
// bannerHost predictably (see main.js's hook point).
import { el } from "./components.js";
import { icon } from "./icons.js";
import { t } from "../core/i18n.js";
import { getChannelsHealth, ApiError } from "../core/api.js";

/** DEFAULT_POLL_MS is how often the banner driver re-checks the
 * channel health report. Independent of (much less frequent than) the
 * hub's own per-minute scheduler tick — this only needs to notice a
 * banner-worthy state within a reasonable page-load-adjacent window,
 * not drive the underlying detection itself. */
export const DEFAULT_POLL_MS = 60000;

/**
 * channelHealthBanner builds the banner node for the given channel
 * names, or null if none are provided (pure — no DOM side effects
 * beyond building the returned node).
 * @param {string[]} channelNames
 * @param {() => void} [onDismiss]
 * @returns {HTMLElement|null}
 */
export function channelHealthBanner(channelNames, onDismiss) {
  if (!channelNames || channelNames.length === 0) return null;

  const node = el("div", { class: "cp-update-banner cp-channel-health-banner", attrs: { role: "alert" } });
  node.append(icon("triangleAlert"));

  const text =
    channelNames.length === 1
      ? t("health.banner_message", { count: 1, name: channelNames[0] })
      : t("health.banner_message", { count: channelNames.length });
  node.append(el("span", { class: "cp-update-banner-text", text }));

  const viewLink = el("a", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { href: "#/settings/notifications?tab=health" }, text: t("health.banner_view") });
  node.append(viewLink);

  if (onDismiss) {
    const dismissBtn = el("button", { class: "cp-btn cp-btn-secondary cp-btn-sm", attrs: { type: "button" }, text: t("health.banner_dismiss") });
    dismissBtn.addEventListener("click", onDismiss);
    node.append(dismissBtn);
  }

  return node;
}

/**
 * startChannelHealthBannerPolling drives the banner: fetches GET
 * /api/v1/alerts/channels/health on an interval (immediately on
 * start), calling render(channelNames) with the current
 * banner_channel_names (empty array when no banner is needed) every
 * time. A fetch error (including 401/403 for a signed-out or non-
 * admin session — this report is admin-only) silently leaves the
 * previous state in place, matching main.js's pollVersion/
 * pollActiveAlerts "swallow and try again next tick" convention.
 * @param {(channelNames: string[]) => void} render
 * @param {{intervalMs?: number, isActive?: () => boolean}} [opts]
 *   isActive lets the caller skip polling while signed out, mirroring
 *   pollActiveAlerts' authState.authenticated guard.
 * @returns {() => void} stop function
 */
export function startChannelHealthBannerPolling(render, opts = {}) {
  const intervalMs = opts.intervalMs ?? DEFAULT_POLL_MS;
  const isActive = opts.isActive ?? (() => true);

  async function poll() {
    if (!isActive()) return;
    try {
      const report = await getChannelsHealth();
      render(report.banner_needed ? report.banner_channel_names || [] : []);
    } catch (err) {
      if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
        render([]);
      }
      // Otherwise swallow: leave the previous banner state in place.
    }
  }

  poll();
  const timer = setInterval(poll, intervalMs);
  return () => clearInterval(timer);
}
