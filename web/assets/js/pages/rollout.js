// rollout.js — Fleet → Rollout page (SPEC-v0.8 §3-1), routed at
// "#/rollout" (the former "#/updates" route redirects here, see
// main.js's routeUpdatesRedirect). This is a placeholder shell only:
// the rollout stage owns the real summary cards, version-distribution
// chart, outdated-host list, and batch history described in the spec.
// Kept intentionally minimal (no data fetching, no auto-refresh work)
// so the rollout stage can build directly on top of this file's
// mount contract without fighting placeholder state.
import { el, clearChildren } from "../ui/components.js";
import { t } from "../core/i18n.js";

/**
 * mountRolloutPage renders the Rollout page's placeholder content into
 * container. Replace the body of this function (not the export
 * signature) once the real dashboard lands.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountRolloutPage(container, { announce }) {
  clearChildren(container);

  const root = el("div", { class: "cp-page" });
  root.append(el("h1", { class: "cp-page-title", text: t("rollout.title") }));
  root.append(el("p", { class: "cp-page-subtitle", text: t("rollout.placeholder") }));
  container.append(root);

  announce(t("rollout.title"));

  async function refresh() {
    // Nothing to fetch yet; the rollout stage replaces this with
    // GET /api/v1/fleet/versions + /api/v1/agents/updates/batches.
  }

  function teardown() {
    // No timers/fetches to tear down yet.
  }

  return { refresh, teardown };
}
