// main.js — bootstrap for the cloud-pulse dashboard: theme watcher,
// app shell (navbar/command palette/toasts), hash router with auth
// guard, and the 401/403 API hooks that drive login redirects and the
// forced change-password modal. Routes: "#/login", "#/" overview,
// "#/host/<id>" host detail, "#/settings" and "#/settings/<section>".
import { initThemeWatcher } from "./core/theme.js";
import { Router } from "./core/router.js";
import { getSessionToken, clearSessionToken, removeLegacyToken, parseLoginNext } from "./core/auth.js";
import { onUnauthorized, onPasswordChangeRequired, getMe, getVersion, getAgentToken, getActiveAlerts } from "./core/api.js";
import { mountShell, openAddAgentDialog } from "./ui/shell.js";
import { showToast } from "./ui/toast.js";
import { mountLoginPage, openForcedChangeModal } from "./pages/login.js";
import { mountOverviewPage } from "./pages/overview.js";
import { mountHostDetailPage } from "./pages/host.js";
import { mountSettingsPage, copyToClipboard, SECTIONS as SETTINGS_SECTIONS } from "./pages/settings/index.js";
import { mountAlertsPage } from "./pages/alerts.js";
import { mountCostsPage } from "./pages/costs.js";
import { mountUpdatesPage } from "./pages/updates.js";
import { updateBanner, clearChildren } from "./ui/components.js";
import {
  shouldShowUpdateBanner,
  getDismissedUpdateTag,
  setDismissedUpdateTag,
  VERSION_REFRESH_INTERVAL_MS,
} from "./core/updates.js";
import { getHosts } from "./core/api.js";

const REFRESH_INTERVAL_MS = 15000;

/** authState caches the last-known auth status so the router's guard
 * doesn't need to fetch on every navigation; refreshed on bootstrap and
 * after login/logout. */
const authState = { authenticated: false, username: "", mustChangePassword: false };

/** activePage holds the currently mounted page's {refresh, teardown}
 * handle so navigation can tear down timers/charts/fetches. */
let activePage = null;
let refreshTimer = null;

function clearRefreshTimer() {
  if (refreshTimer !== null) {
    clearTimeout(refreshTimer);
    refreshTimer = null;
  }
}

function scheduleRefresh(fn) {
  clearRefreshTimer();
  if (document.hidden) return;
  refreshTimer = setTimeout(fn, REFRESH_INTERVAL_MS);
}

function teardownActivePage() {
  clearRefreshTimer();
  if (activePage) {
    activePage.teardown();
    activePage = null;
  }
}

/**
 * clearLoginRoot empties the dedicated login-page DOM root. Called
 * whenever any non-login route mounts so a stale login card never
 * lingers visually underneath/alongside the shell after a successful
 * sign-in (the login page and the shell are separate top-level nodes
 * under <body>, so leaving the login route doesn't otherwise remove
 * its content).
 */
function clearLoginRoot() {
  const root = document.getElementById("cp-login-root");
  if (root) clearChildren(root);
}

function announce(text) {
  shell.liveRegionEl.textContent = text;
}

let shell;
let router;

function mountPageWithAutoRefresh(handle) {
  teardownActivePage();
  activePage = handle;
  const loop = async () => {
    if (activePage !== handle) return;
    await handle.refresh();
    if (activePage === handle) scheduleRefresh(loop);
  };
  loop();
}

function routeOverview() {
  clearLoginRoot();
  const container = shell.mainEl;
  const handle = mountOverviewPage(container, { announce });
  mountPageWithAutoRefresh(handle);
}

function routeHostDetail(match) {
  clearLoginRoot();
  const hostID = decodeURIComponent(match[1]);
  const container = shell.mainEl;
  const handle = mountHostDetailPage(container, hostID, { announce, copyToClipboard });
  mountPageWithAutoRefresh(handle);
}

function routeAlerts() {
  clearLoginRoot();
  const container = shell.mainEl;
  const handle = mountAlertsPage(container, { announce });
  mountPageWithAutoRefresh(handle);
}

function routeCosts() {
  clearLoginRoot();
  const container = shell.mainEl;
  const handle = mountCostsPage(container, { announce });
  mountPageWithAutoRefresh(handle);
}

function routeUpdates() {
  clearLoginRoot();
  const container = shell.mainEl;
  const handle = mountUpdatesPage(container, { announce });
  mountPageWithAutoRefresh(handle);
}

function routeSettings(match, query) {
  clearLoginRoot();
  const container = shell.mainEl;
  const focusHostID = query.get("host") || undefined;
  const section = match[1];
  const handle = mountSettingsPage(container, {
    announce,
    section,
    focusHostID,
    onNavigateSection: (id) => router.navigate(`#/settings/${id}`),
  });
  teardownActivePage();
  activePage = handle;
  clearRefreshTimer();
  handle.refresh();
}

function routeLogin(_match, query) {
  shell.setNavVisible(false);
  teardownActivePage();
  const container = document.getElementById("cp-login-root");
  const handle = mountLoginPage(container, {
    onLoggedIn: async (loginResp) => {
      authState.authenticated = true;
      authState.username = loginResp.username || "admin";
      authState.mustChangePassword = Boolean(loginResp.must_change_password);
      shell.setUsername(authState.username);
      refreshPaletteHostCache();
      pollActiveAlerts();
      const next = parseLoginNext(`#/login?${query.toString() ? query.toString() : ""}`);
      router.navigate(next);
    },
  });
  activePage = { refresh: async () => {}, teardown: handle.teardown };
}

/**
 * isAuthenticated is the router's guard predicate: a session token
 * must be present. The token's validity is verified once at bootstrap
 * via GET /api/v1/auth/me; a 401 from any later request clears it via
 * the api.js onUnauthorized hook, which also flips authState here.
 */
function isAuthenticated() {
  return authState.authenticated && Boolean(getSessionToken());
}

function buildPaletteItems() {
  const items = [
    { id: "nav-overview", label: "Overview", group: "Pages", iconName: "house", onSelect: () => router.navigate("#/") },
    { id: "nav-alerts", label: "Alerts", group: "Pages", iconName: "bell", onSelect: () => router.navigate("#/alerts") },
    { id: "nav-costs", label: "Costs", group: "Pages", iconName: "cloud", onSelect: () => router.navigate("#/costs") },
    { id: "nav-updates", label: "Updates", group: "Pages", iconName: "refreshCw", onSelect: () => router.navigate("#/updates") },
    { id: "nav-settings", label: "Settings", group: "Pages", iconName: "settings", onSelect: () => router.navigate("#/settings") },
  ];
  for (const section of SETTINGS_SECTIONS) {
    items.push({
      id: `settings-${section.id}`,
      label: `Settings: ${section.label}`,
      group: "Settings",
      iconName: section.iconName,
      onSelect: () => router.navigate(`#/settings/${section.id}`),
    });
  }
  for (const host of paletteHostCache) {
    items.push({
      id: `host-${host.id}`,
      label: host.hostname || host.id,
      group: "Hosts",
      iconName: "server",
      hint: host.status === "up" ? "Up" : "Down",
      onSelect: () => router.navigate(`#/host/${encodeURIComponent(host.id)}`),
    });
  }
  items.push(
    {
      id: "action-theme",
      label: "Toggle theme",
      group: "Actions",
      iconName: "sun",
      onSelect: () => document.querySelector(".cp-icon-btn[aria-label='Toggle theme']")?.click(),
    },
    {
      id: "action-add-agent",
      label: "Add agent",
      group: "Actions",
      iconName: "plus",
      onSelect: () => openAddAgent(),
    },
    {
      id: "action-signout",
      label: "Sign out",
      group: "Actions",
      iconName: "logOut",
      onSelect: () => document.querySelector(".cp-icon-btn[aria-label='User menu']")?.click(),
    }
  );
  return items;
}

/** paletteHostCache backs the command palette's "Hosts" group.
 * createPalette's getItems() contract is synchronous (see
 * ui/palette.js), so host data can't be fetched on open — instead this
 * cache is kept warm by refreshPaletteHostCache(), called once at
 * bootstrap and then on a light interval independent of whichever page
 * is currently mounted (the palette can be opened from any page, not
 * just the overview). A stale-by-up-to-PALETTE_HOST_REFRESH_MS list is
 * an acceptable tradeoff for a fuzzy-search launcher, not a live data
 * view. */
let paletteHostCache = [];
const PALETTE_HOST_REFRESH_MS = 30000;

async function refreshPaletteHostCache() {
  if (!isAuthenticated()) return;
  try {
    const resp = await getHosts();
    paletteHostCache = (resp.hosts || []).map((h) => ({
      id: h.host.id,
      hostname: h.host.hostname,
      status: h.status,
    }));
  } catch {
    // Leave the previous cache in place; the palette just shows
    // slightly stale host data until the next successful refresh.
  }
}

async function openAddAgent() {
  try {
    const data = await getAgentToken();
    openAddAgentDialog(data.install_command, data.install_commands);
  } catch {
    showToast({ message: "Sign in as admin to reveal the install command, or use the Settings page.", variant: "error" });
  }
}

// ---------------------------------------------------------------------------
// Global update banner (all authenticated pages)
// ---------------------------------------------------------------------------

let latestVersionInfo = null;
let updateBannerTimer = null;

function renderUpdateBanner() {
  clearChildren(shell.bannerHost);
  if (!authState.authenticated) return;
  if (!shouldShowUpdateBanner(latestVersionInfo, getDismissedUpdateTag())) return;

  const { node, copyBtn, dismissBtn, command } = updateBanner({ versionInfo: latestVersionInfo });
  copyBtn.addEventListener("click", async () => {
    await copyToClipboard(command);
    announce("Update command copied to clipboard.");
  });
  dismissBtn.addEventListener("click", () => {
    setDismissedUpdateTag(latestVersionInfo.latest_version || "");
    renderUpdateBanner();
  });
  shell.bannerHost.append(node);
}

async function pollVersion() {
  try {
    latestVersionInfo = await getVersion();
    renderUpdateBanner();
  } catch {
    // Swallow: the banner simply doesn't update this cycle.
  }
}

function startUpdateBannerPolling() {
  pollVersion();
  updateBannerTimer = setInterval(pollVersion, VERSION_REFRESH_INTERVAL_MS);
  void updateBannerTimer;
}

// ---------------------------------------------------------------------------
// Navbar bell: active-alert polling (all authenticated pages)
// ---------------------------------------------------------------------------

const ACTIVE_ALERTS_POLL_MS = 15000;
let activeAlertsTimer = null;

async function pollActiveAlerts() {
  if (!authState.authenticated) return;
  try {
    const resp = await getActiveAlerts();
    shell.setActiveAlerts(resp.events || []);
  } catch {
    // Swallow: the bell simply doesn't update this cycle.
  }
}

function startActiveAlertsPolling() {
  pollActiveAlerts();
  activeAlertsTimer = setInterval(pollActiveAlerts, ACTIVE_ALERTS_POLL_MS);
  void activeAlertsTimer;
}

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

async function checkInitialAuth() {
  if (!getSessionToken()) return;
  try {
    const me = await getMe();
    authState.authenticated = true;
    authState.username = me.username;
    authState.mustChangePassword = Boolean(me.must_change_password);
  } catch {
    // getMe's 401 handling already cleared the token via onUnauthorized;
    // authState stays unauthenticated.
  }
}

function bootstrap() {
  removeLegacyToken();
  initThemeWatcher();

  shell = mountShell({
    onSignOut: () => {
      authState.authenticated = false;
      authState.username = "";
      shell.setUsername("");
      shell.setActiveAlerts([]);
      paletteHostCache = [];
      router.navigate("#/login");
    },
    getPaletteItems: buildPaletteItems,
    onAddAgent: openAddAgent,
  });

  onUnauthorized(({ sessionExpired }) => {
    const wasAuthenticated = authState.authenticated;
    authState.authenticated = false;
    authState.username = "";
    shell.setUsername("");
    shell.setActiveAlerts([]);
    paletteHostCache = [];
    if (wasAuthenticated) {
      showToast({ message: sessionExpired ? "Session expired. Please sign in again." : "Please sign in.", variant: "error" });
    }
    router.navigate("#/login");
  });

  onPasswordChangeRequired(() => {
    openForcedChangeModal({
      currentPassword: "",
      onDone: (resp) => {
        authState.mustChangePassword = false;
        authState.username = resp.username || authState.username;
        shell.setUsername(authState.username);
      },
    });
  });

  router = new Router({
    isAuthenticated,
    onGuardRedirect: () => {
      shell.setNavVisible(false);
    },
  });
  router.add(/^#\/login(?:\?.*)?$/, routeLogin, { public: true });
  router.add(/^#\/host\/([^/?]+)$/, routeHostDetail);
  router.add(/^#\/settings(?:\/([^/?]+))?(?:\?.*)?$/, routeSettings);
  router.add(/^#\/alerts(?:\?.*)?$/, routeAlerts);
  router.add(/^#\/costs(?:\?.*)?$/, routeCosts);
  router.add(/^#\/updates(?:\?.*)?$/, routeUpdates);
  router.add(/^#\/$/, routeOverview);
  router.setNotFound(routeOverview);

  const originalDispatch = router.dispatch.bind(router);
  router.dispatch = (hash) => {
    shell.setNavVisible(true);
    originalDispatch(hash);
  };

  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      clearRefreshTimer();
    } else if (activePage) {
      router.dispatch();
    }
  });

  checkInitialAuth().then(() => {
    shell.setUsername(authState.username);
    router.start();
    startUpdateBannerPolling();
    startActiveAlertsPolling();
    refreshPaletteHostCache();
    setInterval(refreshPaletteHostCache, PALETTE_HOST_REFRESH_MS);
  });
}

bootstrap();

// Exported for potential future test hooks; not used by index.html directly.
export { getHosts };
