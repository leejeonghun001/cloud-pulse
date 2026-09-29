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
  actions.append(themeBtn);

  const settingsLink = el("a", {
    class: "cp-icon-btn",
    attrs: { href: "#/settings", "aria-label": "Settings" },
  });
  settingsLink.append(icon("settings"));
  actions.append(settingsLink);

  const userBtn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-icon-btn", attrs: { type: "button", "aria-label": "User menu" } })
  );
  userBtn.append(icon("user"));
  let username = "";
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
  actions.append(userBtn);

  const addAgentBtn = el("button", {
    class: "cp-btn cp-btn-primary cp-navbar-primary-btn",
    attrs: { type: "button" },
  });
  addAgentBtn.append(icon("plus"), el("span", { text: "Add agent" }));
  addAgentBtn.addEventListener("click", () => onAddAgent());
  actions.append(addAgentBtn);

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

  return { mainEl, bannerHost, liveRegionEl, setUsername, setNavVisible };
}

/**
 * openAddAgentDialog builds and opens the "Add agent" dialog showing
 * the install command (token copy). Kept here (shell-level) since the
 * navbar button opens it from any page. Callers that already have the
 * install command (e.g. after revealing the agent token) can call this
 * directly; it does not fetch the token itself, keeping shell.js free
 * of the admin-token endpoint dependency — reuse the settings page's
 * agent-enrollment card for the fetch+reveal flow.
 * @param {string} installCommand
 */
export function openAddAgentDialog(installCommand) {
  const { body, open } = createDialog({
    titleId: "cp-add-agent-title",
    title: "Add an agent",
    description: "Run this on the host you want to monitor:",
    wide: true,
  });
  const pre = el("pre", { class: "cp-code-block cp-code-block-wrap" });
  pre.append(el("code", { text: installCommand }));
  body.append(pre);

  const copyBtn = el("button", { class: "cp-btn cp-btn-primary", attrs: { type: "button" }, text: "Copy command" });
  copyBtn.addEventListener("click", async () => {
    try {
      await globalThis.navigator.clipboard.writeText(installCommand);
      showToast({ message: "Install command copied.", variant: "success" });
    } catch {
      showToast({ message: "Could not copy automatically — select and copy manually.", variant: "error" });
    }
  });
  body.append(buildDialogActions([copyBtn]));
  open();
}
