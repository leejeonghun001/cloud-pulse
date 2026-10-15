// settings/index.js — Settings page shell: page header, a sidebar of
// section links (General, Network, Security, Agents, Traffic limits,
// Notifications), and a content region each section module renders
// into. Routed at "#/settings" (defaults to "general") and
// "#/settings/<section>". Mobile (<768px, matched via the .cp-settings
// layout in settings.css) collapses the sidebar into a horizontal
// scroll-tab row via CSS alone — this module always renders the same
// markup.
import { el, clearChildren } from "../../ui/components.js";
import { icon } from "../../ui/icons.js";
import { mountGeneralSection } from "./general.js";
import { mountNetworkSection } from "./network.js";
import { mountSecuritySection } from "./security.js";
import { mountAgentsSection } from "./agents.js";
import { mountTrafficSection } from "./traffic.js";
import { mountNotificationsSection } from "./notifications.js";
import { mountAlertRulesSection } from "./alert-rules.js";
import { mountBillingSection } from "./billing.js";
import { mountStorageSection } from "./storage.js";
import { t } from "../../core/i18n.js";

/**
 * copyToClipboard copies text to the clipboard using the async
 * Clipboard API when available (secure context + permission), falling
 * back to selecting the given text-holding input/textarea element so the
 * user can press Ctrl+C themselves. Returns a status string suitable for
 * an aria-live announcement.
 * @param {string} text
 * @param {HTMLInputElement|HTMLTextAreaElement} [selectEl] element whose
 *   content mirrors text, selected as a fallback when the Clipboard API
 *   is unavailable or rejects.
 * @returns {Promise<"copied"|"select-fallback">}
 */
export async function copyToClipboard(text, selectEl) {
  try {
    if (globalThis.navigator?.clipboard?.writeText) {
      await globalThis.navigator.clipboard.writeText(text);
      return "copied";
    }
  } catch {
    // Fall through to the selection fallback (permission denied,
    // insecure context, etc.).
  }
  if (selectEl) {
    selectEl.removeAttribute("hidden");
    selectEl.focus();
    selectEl.select();
  }
  return "select-fallback";
}

/** SECTIONS lists the sidebar entries in display order: id (route
 * segment), label, icon name, and the mount function each section
 * module exports (container, ctx) -> {refresh, teardown}. Exported
 * (id/label/iconName only matter outside this module) so the command
 * palette in main.js can list settings sections without importing
 * every section's mount function. */
export const SECTIONS = [
  { id: "general", get label() { return t("settings.section_general"); }, iconName: "settings", mount: mountGeneralSection },
  { id: "network", get label() { return t("settings.section_network"); }, iconName: "network", mount: mountNetworkSection },
  { id: "security", get label() { return t("settings.section_security"); }, iconName: "shield", mount: mountSecuritySection },
  { id: "agents", get label() { return t("settings.section_agents"); }, iconName: "server", mount: mountAgentsSection },
  { id: "traffic", get label() { return t("settings.section_traffic"); }, iconName: "gauge", mount: mountTrafficSection },
  { id: "notifications", get label() { return t("settings.section_notifications"); }, iconName: "bell", mount: mountNotificationsSection },
  { id: "alert-rules", get label() { return t("settings.section_alert_rules"); }, iconName: "triangleAlert", mount: mountAlertRulesSection },
  { id: "billing", get label() { return t("settings.section_billing"); }, iconName: "cloud", mount: mountBillingSection },
  { id: "storage", get label() { return t("settings.section_storage"); }, iconName: "hardDrive", mount: mountStorageSection },
];

/** DEFAULT_SECTION is used when "#/settings" is visited with no
 * section segment. */
const DEFAULT_SECTION = "general";

/**
 * resolveSection maps a route section param to a known section id,
 * defaulting to DEFAULT_SECTION for an unset/unknown value. Pure and
 * exported for testing the routing fallback without mounting anything.
 * @param {string|undefined|null} sectionParam
 * @returns {string}
 */
export function resolveSection(sectionParam) {
  return SECTIONS.some((s) => s.id === sectionParam) ? sectionParam : DEFAULT_SECTION;
}

/** Keeps the active item visible in the horizontally scrollable mobile tab row. */
export function scrollActiveSettingsTab(activeLink) {
  activeLink?.scrollIntoView?.({ inline: "center", block: "nearest" });
}

/**
 * mountSettingsPage renders the Settings page shell (header + sidebar +
 * active section) into container.
 * @param {HTMLElement} container
 * @param {Object} ctx
 * @param {(text: string) => void} ctx.announce
 * @param {string} [ctx.section] route section param (e.g. "network");
 *   resolved via resolveSection.
 * @param {string} [ctx.focusHostID]
 * @param {(section: string) => void} [ctx.onNavigateSection] called when
 *   the user clicks a different sidebar entry — the router owns actual
 *   navigation, this just requests it.
 * @returns {{refresh: () => Promise<void>, teardown: () => void}}
 */
export function mountSettingsPage(container, { announce, section, focusHostID, onNavigateSection }) {
  const activeID = resolveSection(section);

  clearChildren(container);
  const root = el("div", { class: "cp-settings-page" });
  root.append(el("h1", { class: "cp-page-title", text: t("settings.title") }));
  root.append(el("p", { class: "cp-page-subtitle", text: t("settings.subtitle") }));

  const layout = el("div", { class: "cp-settings-layout" });
  const nav = el("nav", { class: "cp-settings-nav", attrs: { "aria-label": t("settings.sections_aria_label") } });
  const list = el("ul", { class: "cp-settings-nav-list", attrs: { role: "list" } });
  let activeLink = null;

  for (const s of SECTIONS) {
    const li = el("li");
    const link = el("a", {
      class: `cp-settings-nav-link${s.id === activeID ? " cp-settings-nav-link-active" : ""}`,
      attrs: { href: `#/settings/${s.id}`, ...(s.id === activeID ? { "aria-current": "page" } : {}) },
    });
    if (s.id === activeID) activeLink = link;
    link.append(icon(s.iconName), el("span", { text: s.label }));
    if (onNavigateSection) {
      link.addEventListener("click", (ev) => {
        ev.preventDefault();
        onNavigateSection(s.id);
      });
    }
    li.append(link);
    list.append(li);
  }
  nav.append(list);
  layout.append(nav);

  const contentHost = el("div", { class: "cp-settings-content" });
  layout.append(contentHost);
  root.append(layout);
  container.append(root);

  // On narrow screens the section nav is horizontally scrollable. Centering
  // the current item on each mount prevents a deep-linked active section from
  // being rendered beyond the visible row.
  scrollActiveSettingsTab(activeLink);

  const activeDef = SECTIONS.find((s) => s.id === activeID);
  const handle = activeDef.mount(contentHost, { announce, focusHostID });

  return {
    refresh: () => handle.refresh(),
    teardown: () => handle.teardown(),
  };
}

