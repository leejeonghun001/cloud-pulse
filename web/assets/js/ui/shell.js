// shell.js — the app shell: navbar (logo, search/palette trigger, theme
// toggle, settings icon, user menu, "Add agent" button), banner host
// (update banner etc.), and the <main> content region pages render
// into. Built once during bootstrap; mountShell returns handles the
// router/pages use to swap page content and update auth-dependent UI
// (username in the user menu).
import { el, clearChildren } from "./components.js";
import { icon } from "./icons.js";
import { createDropdown, dropdownItem, dropdownSeparator } from "./dropdown.js";
import { createDialog, buildDialogActions } from "./dialog.js";
import { createPalette } from "./palette.js";
import { getStoredTheme, setStoredTheme, applyTheme, cycleTheme } from "../core/theme.js";
import { registerGlobalShortcuts } from "../core/shortcuts.js";
import { logout } from "../core/api.js";
import { clearSessionToken } from "../core/auth.js";
import { showToast } from "./toast.js";

/**
 * mountShell builds the app shell into document.body and returns
 * handles for the router/pages to use.
 * @param {Object} opts
 * @param {() => void} opts.onSignOut called after a successful (or
 *   best-effort) logout, before navigating to #/login.
 * @param {() => Array<import('./palette.js').PaletteItem>} opts.getPaletteItems
 * @param {() => void} opts.onAddAgent opens the "Add agent" install dialog
 * @returns {{
 *   mainEl: HTMLElement,
 *   bannerHost: HTMLElement,
 *   liveRegionEl: HTMLElement,
 *   setUsername: (name: string) => void,
 *   setNavVisible: (visible: boolean) => void,
 *   setActiveAlerts: (events: Array) => void,
 * }}
 */
export function mountShell({ onSignOut, getPaletteItems, onAddAgent }) {
  const app = el("div", { class: "cp-app" });

  const header = el("header", { class: "cp-shell-header" });
  const navbar = el("nav", { class: "cp-navbar", attrs: { "aria-label": "Primary" } });

  const brand = el("a", { class: "cp-navbar-brand", attrs: { href: "#/" } });
  brand.append(icon("activity"), el("span", { class: "cp-navbar-wordmark", text: "cloud-pulse" }));
  navbar.append(brand);

  const searchBtn = el("button", {
    class: "cp-navbar-search",
    attrs: { type: "button", "aria-label": "Open command palette" },
  });
  searchBtn.append(
    icon("search"),
    el("span", { class: "cp-navbar-search-label", text: "Search…" }),
    el("kbd", { class: "cp-kbd", text: "Ctrl K" }),
  );
  navbar.append(searchBtn);

  const actions = el("div", { class: "cp-navbar-actions" });

  let activeAlertEvents = [];
  const bellBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-icon-btn cp-navbar-bell", attrs: { type: "button", "aria-label": "Alerts" } })
  );
  const bellBadge = el("span", { class: "cp-navbar-bell-badge", attrs: { "aria-hidden": "true" } });
  bellBadge.hidden = true;
  bellBtn.append(icon("bell"), bellBadge);
  actions.append(bellBtn);
  createDropdown({
    trigger: bellBtn,
    buildMenu: () => buildBellMenu(activeAlertEvents),
  });

  const themeBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-icon-btn", attrs: { type: "button", "aria-label": "Toggle theme" } })
  );
  function renderThemeIcon() {
    clearChildren(themeBtn);
    const pref = getStoredTheme();
    const iconName = pref === "dark" ? "moon" : pref === "light" ? "sun" : "monitor";
    themeBtn.append(icon(iconName, { title: `Theme: ${pref}` }));
  }
  renderThemeIcon();
  themeBtn.addEventListener("click", () => {
    const next = cycleTheme(getStoredTheme());
    setStoredTheme(next);
    applyTheme(next);
    renderThemeIcon();
  });
  themeBtn.classList.add("cp-navbar-theme-btn");
  actions.append(themeBtn);

  const settingsLink = el("a", {
    class: "cp-icon-btn cp-navbar-settings-link",
    attrs: { href: "#/settings", "aria-label": "Settings" },
  });
  settingsLink.append(icon("settings"));
  actions.append(settingsLink);

  const userBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-icon-btn", attrs: { type: "button", "aria-label": "User menu" } })
  );
  userBtn.append(icon("user"));
  let username = "";
  actions.append(userBtn);
  createDropdown({
    trigger: userBtn,
    buildMenu: () => {
      const frag = document.createDocumentFragment();
      if (username) {
        const label = el("div", { class: "cp-dropdown-item", attrs: { style: "pointer-events:none;font-weight:600" } });
        label.textContent = username;
        frag.append(label, dropdownSeparator());
      }
      frag.append(
        dropdownItem({ label: "Change password", icon: icon("keyRound"), onClick: () => { globalThis.location.hash = "#/settings/security"; } }),
        dropdownItem({
          label: "Sign out",
          icon: icon("logOut"),
          onClick: async () => {
            try {
              await logout();
            } catch {
              // Best-effort: proceed to clear local state regardless.
            }
            clearSessionToken();
            onSignOut();
          },
        }),
      );
      return frag;
    },
  });

  const addAgentBtn = el("button", {
    class: "cp-btn cp-btn-primary cp-navbar-primary-btn",
    attrs: { type: "button" },
  });
  addAgentBtn.append(icon("plus"), el("span", { text: "Add agent" }));
  addAgentBtn.addEventListener("click", () => onAddAgent());
  actions.append(addAgentBtn);

  // Mobile overflow menu (< 640px, SPEC-v0.7 §5): consolidates Theme,
  // Settings, and Add agent into a single dropdown so the navbar's
  // primary row fits at a 360px viewport without horizontal overflow —
  // .cp-navbar-theme-btn/.cp-navbar-settings-link/.cp-navbar-primary-btn
  // hide at that breakpoint (see web/src/base.css), this button (and
  // its mirror actions) only shows there. Bell and user-menu stay
  // visible at every width as the two most frequently needed actions.
  const overflowBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-icon-btn cp-navbar-overflow-btn", attrs: { type: "button", "aria-label": "More actions" } })
  );
  overflowBtn.append(icon("ellipsisVertical"));
  actions.append(overflowBtn);
  createDropdown({
    trigger: overflowBtn,
    buildMenu: () => {
      const frag = document.createDocumentFragment();
      frag.append(
        dropdownItem({
          label: "Add agent",
          icon: icon("plus"),
          onClick: () => onAddAgent(),
        }),
        dropdownItem({
          label: "Toggle theme",
          icon: icon("monitor"),
          onClick: () => {
            const next = cycleTheme(getStoredTheme());
            setStoredTheme(next);
            applyTheme(next);
            renderThemeIcon();
          },
        }),
        dropdownItem({
          label: "Settings",
          icon: icon("settings"),
          onClick: () => { globalThis.location.hash = "#/settings"; },
        }),
      );
      return frag;
    },
  });

  navbar.append(actions);
  header.append(navbar);
  app.append(header);

  const bannerHost = el("div", { class: "cp-shell-banners" });
  app.append(bannerHost);

  const mainEl = el("main", { class: "cp-main", attrs: { id: "cp-main", tabindex: "-1" } });
  app.append(mainEl);

  const footer = el("footer", { class: "cp-footer" });
  const liveRegionEl = el("p", { class: "cp-sr-only", attrs: { role: "status", "aria-live": "polite" } });
  footer.append(liveRegionEl);
  app.append(footer);

  document.body.append(app);

  // Command palette
  const palette = createPalette(getPaletteItems);
  searchBtn.addEventListener("click", () => palette.open());
  registerGlobalShortcuts(() => palette.open());

  // React to system theme changes / re-render the toggle icon on
  // programmatic theme changes triggered elsewhere.
  globalThis.addEventListener("cp-theme-change", renderThemeIcon);

  function setUsername(name) {
    username = name || "";
  }

  function setNavVisible(visible) {
    header.style.display = visible ? "" : "none";
    footer.style.display = visible ? "" : "none";
  }

  /**
   * setActiveAlerts updates the bell badge count and the dropdown's
   * contents from the latest GET /api/v1/alerts/active response. Safe
   * to call on every poll tick regardless of whether the dropdown is
   * currently open (buildMenu() is only invoked when it opens).
   * @param {Array} events models.AlertEvent[]
   */
  function setActiveAlerts(events) {
    activeAlertEvents = events || [];
    const count = activeAlertEvents.length;
    bellBadge.hidden = count === 0;
    bellBadge.textContent = count > 99 ? "99+" : String(count);
    bellBtn.setAttribute("aria-label", count > 0 ? `Alerts, ${count} firing` : "Alerts");
    bellBtn.classList.toggle("cp-navbar-bell-active", count > 0);
  }

  return { mainEl, bannerHost, liveRegionEl, setUsername, setNavVisible, setActiveAlerts };
}

/**
 * buildBellMenu builds the navbar bell dropdown's contents: up to 5
 * firing alerts (each a link to its host) plus a "View all" link to
 * #/alerts, or an empty-state message when nothing is firing.
 * @param {Array} events models.AlertEvent[], already sorted
 *   newest-first by the caller (core/api.js's getActiveAlerts response
 *   is sorted server-side).
 * @returns {DocumentFragment}
 */
function buildBellMenu(events) {
  const frag = document.createDocumentFragment();
  const header = el("div", { class: "cp-dropdown-item cp-navbar-bell-menu-title", attrs: { style: "pointer-events:none;font-weight:600" } });
  header.textContent = events.length > 0 ? `${events.length} firing` : "Alerts";
  frag.append(header, dropdownSeparator());

  if (events.length === 0) {
    const empty = el("div", { class: "cp-dropdown-item cp-navbar-bell-empty", attrs: { style: "pointer-events:none" } });
    empty.textContent = "No active alerts.";
    frag.append(empty);
  } else {
    for (const ev of events.slice(0, 5)) {
      const link = el("a", { class: "cp-dropdown-item cp-navbar-bell-item", attrs: { href: `#/host/${encodeURIComponent(ev.host_id)}` } });
      const title = el("span", { class: "cp-navbar-bell-item-title", text: `${ev.rule_name || ev.metric}` });
      const meta = el("span", { class: "cp-navbar-bell-item-meta", text: ev.hostname || ev.host_id });
      link.append(title, meta);
      frag.append(link);
    }
  }

  frag.append(dropdownSeparator());
  frag.append(el("a", { class: "cp-dropdown-item", attrs: { href: "#/alerts" }, text: "View all" }));
  return frag;
}

/**
 * openAddAgentDialog builds and opens the "Add agent" dialog showing
 * the install command (token copy). Kept here (shell-level) since the
 * navbar button opens it from any page. Callers that already have the
 * install command (e.g. after revealing the agent token) can call this
 * directly; it does not fetch the token itself, keeping shell.js free
 * of the admin-token endpoint dependency — reuse the settings page's
 * agent-enrollment card for the fetch+reveal flow.
 *
 * @param {string} installCommand Fallback single command (pre-v0.7.0
 *   shape, or used directly when installCommands is omitted/empty).
 * @param {Array<{os: string, label: string, command: string}>} [installCommands]
 *   Per-OS one-liners (SPEC-v0.7 §1). When present, renders one tab
 *   per OS instead of a single fixed command.
 */
export function openAddAgentDialog(installCommand, installCommands) {
  const { body, open } = createDialog({
    titleId: "cp-add-agent-title",
    title: "Add an agent",
    description: "Run this on the host you want to monitor:",
    wide: true,
  });

  const tabs = Array.isArray(installCommands) && installCommands.length > 0
    ? installCommands
    : [{ os: "linux", label: "Linux / macOS", command: installCommand }];

  const tabList = el("div", { class: "cp-btn-group", attrs: { role: "tablist", "aria-label": "Operating system" } });
  const pre = el("pre", { class: "cp-code-block cp-code-block-wrap" });
  const code = el("code", { text: tabs[0].command });
  pre.append(code);

  let activeCommand = tabs[0].command;
  const tabButtons = tabs.map((tab, index) =>
    el("button", {
      class: `cp-btn cp-btn-secondary${index === 0 ? " cp-btn-secondary-active" : ""}`,
      attrs: { type: "button", role: "tab", "aria-selected": index === 0 ? "true" : "false" },
      text: tab.label,
    }),
  );
  tabButtons.forEach((btn, index) => {
    btn.addEventListener("click", () => {
      activeCommand = tabs[index].command;
      code.textContent = activeCommand;
      tabButtons.forEach((b, i) => {
        b.classList.toggle("cp-btn-secondary-active", i === index);
        b.setAttribute("aria-selected", i === index ? "true" : "false");
      });
    });
    tabList.append(btn);
  });

  if (tabs.length > 1) {
    body.append(tabList);
  }
  body.append(pre);

  const copyBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: "Copy command" });
  copyBtn.addEventListener("click", async () => {
    try {
      await globalThis.navigator.clipboard.writeText(activeCommand);
      showToast({ message: "Install command copied.", variant: "success" });
    } catch {
      showToast({ message: "Could not copy automatically — select and copy manually.", variant: "error" });
    }
  });
  body.append(buildDialogActions([copyBtn]));
  open();
}
