// main.js — bootstrap for the cloud-pulse dashboard: theme watcher,
// app shell (navbar/command palette/toasts), hash router with auth
// guard, and the 401/403 API hooks that drive login redirects and the
// forced change-password modal. Routes: "#/login", "#/" overview,
// "#/host/<id>" host detail, "#/settings" and "#/settings/<section>".
import { initThemeWatcher } from "./core/theme.js";
import { Router, pathToHashRoute } from "./core/router.js";
import { getSessionToken, clearSessionToken, removeLegacyToken, parseLoginNext } from "./core/auth.js";
import { onUnauthorized, onPasswordChangeRequired, getMe, getVersion, getAgentToken, getActiveAlerts, getHosts, getRecentOrPendingHosts, clearHostsCache } from "./core/api.js";
import { loadCatalogs, htmlLang, t } from "./core/i18n.js";
import { mountShell, openAddAgentDialog } from "./ui/shell.js";
import { showToast } from "./ui/toast.js";
import { mountLoginPage, openForcedChangeModal } from "./pages/login.js";
import { mountOverviewPage } from "./pages/overview.js";
import { copyToClipboard } from "./core/clipboard.js";
import { SETTINGS_SECTIONS } from "./core/settings-sections.js";
import { updateBanner, clearChildren, el, errorBanner } from "./ui/components.js";
import {
  shouldShowUpdateBanner,
  getDismissedUpdateTag,
  setDismissedUpdateTag,
  VERSION_REFRESH_INTERVAL_MS,
} from "./core/updates.js";
import { channelHealthBanner, startChannelHealthBannerPolling } from "./ui/channel-health-banner.js";

const REFRESH_INTERVAL_MS = 15000;

/** authState caches the last-known auth status so the router's guard
 * doesn't need to fetch on every navigation; refreshed on bootstrap and
 * after login/logout. */
const authState = { authenticated: false, username: "", mustChangePassword: false };

/** activePage holds the currently mounted page's {refresh, teardown}
 * handle so navigation can tear down timers/charts/fetches. */
let activePage = null;
let refreshTimer = null;
let activeRefreshLoop = null;
let routeLoadGeneration = 0;

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
  activeRefreshLoop = null;
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

/** Complete a user-initiated route mount without affecting background data
 * refreshes. Every dashboard page supplies an h1; assigning tabindex here
 * keeps individual page modules free of router concerns. */
function completeRouteChange(routeTitle = "") {
  if (!focusRouteOnMount) return;
  focusRouteOnMount = false;
  globalThis.scrollTo?.({ top: 0, left: 0, behavior: "auto" });
  const heading = shell.mainEl.querySelector("h1");
  if (heading) {
    heading.setAttribute("tabindex", "-1");
    heading.focus({ preventScroll: true });
    announce(heading.textContent?.trim() || routeTitle);
    return;
  }
  // Lazy pages may render their heading on their first asynchronous refresh.
  // Keep focus inside the new route and give assistive tech its nav label now.
  shell.mainEl.setAttribute("tabindex", "-1");
  shell.mainEl.focus({ preventScroll: true, focusVisible: false });
  announce(routeTitle);
}

let shell;
let router;
let lastDispatchedRoute = null;
let focusRouteOnMount = false;

function mountPageWithAutoRefresh(handle) {
  teardownActivePage();
  activePage = handle;
  const loop = async (reason = "scheduled") => {
    if (activePage !== handle) return;
    // Existing pages ignore this optional argument. Activity uses it to
    // keep a reader's loaded history intact during background refreshes.
    await handle.refresh({ reason });
    if (activePage === handle) scheduleRefresh(loop);
  };
  activeRefreshLoop = loop;
  loop("initial");
}

function routeOverview() {
  routeLoadGeneration++;
  clearLoginRoot();
  const container = shell.mainEl;
  const handle = mountOverviewPage(container, { announce, onAddAgent: openAddAgent });
  mountPageWithAutoRefresh(handle);
  completeRouteChange(t("nav.overview"));
}

/** Render an existing lightweight loading/error state while a route chunk is
 * fetched. A failed chunk reloads the document so every module comes from the
 * same build, avoiding an unrecoverable mixed-version module graph. */
function mountLazyRoute(load, mount, { autoRefresh = true, routeTitle = "" } = {}) {
  const generation = ++routeLoadGeneration;
  clearLoginRoot();
  teardownActivePage();
  const container = shell.mainEl;
  clearChildren(container);
  const loading = el("p", { class: "cp-muted", attrs: { role: "status" }, text: t("common.loading") });
  container.append(loading);
  activePage = { refresh: async () => {}, teardown: () => {} };

  load().then((module) => {
    if (generation !== routeLoadGeneration) return;
    const handle = mount(module);
    if (autoRefresh) {
      mountPageWithAutoRefresh(handle);
    } else {
      teardownActivePage();
      activePage = handle;
      clearRefreshTimer();
      handle.refresh();
    }
    completeRouteChange(routeTitle);
  }).catch((err) => {
    // Also reached when mount() itself throws; keep the cause visible.
    console.error(err);
    if (generation !== routeLoadGeneration) return;
    clearChildren(container);
    container.append(errorBanner(t("errors.route_load_failed")));
    const reload = el("button", { class: "cp-btn cp-btn-secondary", attrs: { type: "button" }, text: t("common.reload_page") });
    reload.addEventListener("click", () => globalThis.location.reload());
    container.append(reload);
  });
}

function routeHostDetail(match) {
  const hostID = decodeURIComponent(match[1]);
  mountLazyRoute(() => import("./pages/host.js"), ({ mountHostDetailPage }) => (
    mountHostDetailPage(shell.mainEl, hostID, { announce, copyToClipboard })
  ), { routeTitle: t("host.detail_loading") });
}

function routeCompare(_match, query) {
  const initialHosts = query.get("hosts") || "";
  mountLazyRoute(() => import("./pages/compare.js"), ({ mountComparePage }) => (
    mountComparePage(shell.mainEl, { announce, initialHosts })
  ), { routeTitle: t("compare.palette_entry") });
}

function routeAlerts() {
  mountLazyRoute(() => import("./pages/alerts.js"), ({ mountAlertsPage }) => (
    mountAlertsPage(shell.mainEl, { announce })
  ), { routeTitle: t("nav.alerts") });
}

function routeCosts() {
  mountLazyRoute(() => import("./pages/costs.js"), ({ mountCostsPage }) => (
    mountCostsPage(shell.mainEl, { announce })
  ), { routeTitle: t("nav.costs") });
}

function routeRollout() {
  mountLazyRoute(() => import("./pages/rollout.js"), ({ mountRolloutPage }) => (
    mountRolloutPage(shell.mainEl, { announce })
  ), { routeTitle: t("nav.rollout") });
}

function routeUpdatesRedirect() {
  router.navigate("#/rollout");
}

function routeSettings(match, query) {
  const focusHostID = query.get("host") || undefined;
  const section = match[1];
  mountLazyRoute(() => import("./pages/settings/index.js"), ({ mountSettingsPage }) => {
    const handle = mountSettingsPage(shell.mainEl, {
      announce,
      section,
      focusHostID,
      onNavigateSection: (id) => router.navigate(`#/settings/${id}`),
    });
    return handle;
  }, { autoRefresh: false, routeTitle: t("nav.settings") });
}

function routeLogin(_match, query) {
  routeLoadGeneration++;
  focusRouteOnMount = false;
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
      resumeBackgroundPolling();
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
    { id: "nav-overview", label: t("nav.overview"), group: t("nav.palette_group_pages"), iconName: "house", onSelect: () => router.navigate("#/") },
    { id: "nav-alerts", label: t("nav.alerts"), group: t("nav.palette_group_pages"), iconName: "bell", onSelect: () => router.navigate("#/alerts") },
    { id: "nav-costs", label: t("nav.costs"), group: t("nav.palette_group_pages"), iconName: "cloud", onSelect: () => router.navigate("#/costs") },
    { id: "nav-rollout", label: t("nav.rollout"), group: t("nav.palette_group_pages"), iconName: "refreshCw", onSelect: () => router.navigate("#/rollout") },
    { id: "nav-settings", label: t("nav.settings"), group: t("nav.palette_group_pages"), iconName: "settings", onSelect: () => router.navigate("#/settings") },
    { id: "nav-compare", label: t("compare.palette_entry"), group: t("nav.palette_group_pages"), iconName: "server", onSelect: () => router.navigate("#/compare") },
  ];
  for (const section of SETTINGS_SECTIONS) {
    items.push({
      id: `settings-${section.id}`,
      label: t("nav.palette_settings_section", { section: section.label }),
      group: t("nav.palette_group_settings"),
      iconName: section.iconName,
      onSelect: () => router.navigate(`#/settings/${section.id}`),
    });
  }
  for (const host of paletteHostCache) {
    items.push({
      id: `host-${host.id}`,
      label: host.hostname || host.id,
      group: t("nav.palette_group_hosts"),
      iconName: "server",
      hint: host.status === "up" ? t("host.status_up") : t("host.status_down"),
      onSelect: () => router.navigate(`#/host/${encodeURIComponent(host.id)}`),
    });
  }
  items.push(
    {
      id: "action-theme",
      label: t("nav.toggle_theme"),
      group: t("nav.palette_group_actions"),
      iconName: "sun",
      onSelect: () => shell.toggleTheme(),
    },
    {
      id: "action-add-agent",
      label: t("nav.add_agent"),
      group: t("nav.palette_group_actions"),
      iconName: "plus",
      onSelect: () => openAddAgent(document.querySelector(".cp-navbar-search")),
    },
    {
      id: "action-keyboard-shortcuts",
      label: t("shortcuts.open"),
      group: t("nav.palette_group_actions"),
      iconName: "command",
      onSelect: () => shell.openShortcuts(),
    },
    {
      id: "action-signout",
      label: t("common.sign_out"),
      group: t("nav.palette_group_actions"),
      iconName: "logOut",
      onSelect: () => shell.signOut(),
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
  if (!isAuthenticated() || document.hidden) return;
  try {
    const reusable = getRecentOrPendingHosts(PALETTE_HOST_REFRESH_MS);
    const resp = reusable ? await reusable : await getHosts();
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

let addAgentRequest = 0;
async function openAddAgent(opener = document.activeElement, { signal } = {}) {
  const request = ++addAgentRequest;
  const page = activePage;
  const route = globalThis.location.hash;
  let canceled = false;
  const cancelPending = (event) => { if (event.key === "Escape") canceled = true; };
  const isCurrent = () => !canceled && request === addAgentRequest && !signal?.aborted
    && activePage === page && globalThis.location.hash === route && isAuthenticated()
    && (!opener || opener.isConnected);
  try {
    if (!isCurrent()) return;
    document.addEventListener("keydown", cancelPending);
    const data = await getAgentToken(signal);
    if (!isCurrent()) return;
    openAddAgentDialog(data.install_command, data.install_commands, { restoreFocusTo: opener, shouldRestoreFocus: isCurrent });
  } catch {
    if (isCurrent()) showToast({ message: t("errors.add_agent_sign_in_required"), variant: "error" });
  } finally {
    document.removeEventListener("keydown", cancelPending);
  }
}

// ---------------------------------------------------------------------------
// Global update banner (all authenticated pages)
// ---------------------------------------------------------------------------

let latestVersionInfo = null;
let updateBannerTimer = null;
let channelHealthBannerNames = [];
let stopChannelHealthPolling = null;

function renderUpdateBanner() {
  clearChildren(shell.bannerHost);
  if (!authState.authenticated) return;

  const healthNode = channelHealthBanner(channelHealthBannerNames, () => {
    channelHealthBannerNames = [];
    renderUpdateBanner();
  });
  if (healthNode) shell.bannerHost.append(healthNode);

  if (!shouldShowUpdateBanner(latestVersionInfo, getDismissedUpdateTag())) return;

  const { node, copyBtn, dismissBtn, command } = updateBanner({ versionInfo: latestVersionInfo });
  copyBtn.addEventListener("click", async () => {
    await copyToClipboard(command);
    announce(t("nav.update_command_copied"));
  });
  dismissBtn.addEventListener("click", () => {
    setDismissedUpdateTag(latestVersionInfo.latest_version || "");
    renderUpdateBanner();
  });
  shell.bannerHost.append(node);
}

async function pollVersion() {
  if (!authState.authenticated || document.hidden) return;
  try {
    latestVersionInfo = await getVersion();
    renderUpdateBanner();
  } catch {
    // Swallow: the banner simply doesn't update this cycle.
  }
}

function startUpdateBannerPolling() {
  if (updateBannerTimer !== null) clearInterval(updateBannerTimer);
  pollVersion();
  updateBannerTimer = setInterval(pollVersion, VERSION_REFRESH_INTERVAL_MS);
  void updateBannerTimer;
  if (!stopChannelHealthPolling) {
    stopChannelHealthPolling = startChannelHealthBannerPolling(
      (names) => {
        channelHealthBannerNames = names;
        renderUpdateBanner();
      },
      { isActive: () => authState.authenticated },
    );
  }
}

// ---------------------------------------------------------------------------
// Navbar bell: active-alert polling (all authenticated pages)
// ---------------------------------------------------------------------------

const ACTIVE_ALERTS_POLL_MS = 15000;
let activeAlertsTimer = null;

async function pollActiveAlerts() {
  if (!authState.authenticated || document.hidden) return;
  try {
    const resp = await getActiveAlerts();
    shell.setActiveAlerts(resp.events || []);
  } catch {
    // Swallow: the bell simply doesn't update this cycle.
  }
}

function startActiveAlertsPolling() {
  if (activeAlertsTimer !== null) clearInterval(activeAlertsTimer);
  pollActiveAlerts();
  activeAlertsTimer = setInterval(pollActiveAlerts, ACTIVE_ALERTS_POLL_MS);
  void activeAlertsTimer;
}

let paletteHostTimer = null;
function startPaletteHostPolling() {
  if (paletteHostTimer !== null) clearInterval(paletteHostTimer);
  refreshPaletteHostCache();
  paletteHostTimer = setInterval(refreshPaletteHostCache, PALETTE_HOST_REFRESH_MS);
}

function pauseBackgroundPolling() {
  if (activeAlertsTimer !== null) {
    clearInterval(activeAlertsTimer);
    activeAlertsTimer = null;
  }
  if (paletteHostTimer !== null) {
    clearInterval(paletteHostTimer);
    paletteHostTimer = null;
  }
  if (updateBannerTimer !== null) {
    clearInterval(updateBannerTimer);
    updateBannerTimer = null;
  }
}

function resumeBackgroundPolling() {
  if (document.hidden || !authState.authenticated) return;
  startActiveAlertsPolling();
  startPaletteHostPolling();
  startUpdateBannerPolling();
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

/**
 * wireSkipLink localizes index.html's static "Skip to main content" link
 * and makes it move focus to <main> without touching location.hash (the
 * hash is the router's; "#cp-main" would otherwise navigate away).
 */
function wireSkipLink() {
  const link = document.querySelector(".cp-skip-link");
  if (!link) return;
  link.textContent = t("nav.skip_to_main");
  link.addEventListener("click", (event) => {
    event.preventDefault();
    const main = document.getElementById("cp-main");
    if (main) main.focus();
  });
}

async function bootstrap() {
  // A deep link such as /login is served index.html by the hub; move it
  // into the hash route this app actually uses before anything renders.
  const hashURL = pathToHashRoute(location.pathname, location.search, location.hash);
  if (hashURL) history.replaceState(null, "", hashURL);

  await loadCatalogs();
  document.documentElement.lang = htmlLang();
  wireSkipLink();

  removeLegacyToken();
  initThemeWatcher();

  shell = mountShell({
    onSignOut: () => {
      authState.authenticated = false;
      authState.username = "";
      shell.setUsername("");
      shell.setActiveAlerts([]);
      paletteHostCache = [];
      clearHostsCache();
      pauseBackgroundPolling();
      router.navigate("#/login");
    },
    getPaletteItems: buildPaletteItems,
    onAddAgent: openAddAgent,
    onNavigate: (route) => router.navigate(route),
  });

  onUnauthorized(({ sessionExpired }) => {
    const wasAuthenticated = authState.authenticated;
    authState.authenticated = false;
    authState.username = "";
    shell.setUsername("");
    shell.setActiveAlerts([]);
    paletteHostCache = [];
    clearHostsCache();
    pauseBackgroundPolling();
    if (wasAuthenticated) {
      showToast({ message: sessionExpired ? t("errors.session_expired") : t("errors.please_sign_in"), variant: "error" });
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
  router.add(/^#\/compare(?:\?.*)?$/, routeCompare);
  router.add(/^#\/settings(?:\/([^/?]+))?(?:\?.*)?$/, routeSettings);
  router.add(/^#\/alerts(?:\?.*)?$/, routeAlerts);
  router.add(/^#\/costs(?:\?.*)?$/, routeCosts);
  router.add(/^#\/rollout(?:\?.*)?$/, routeRollout);
  router.add(/^#\/updates(?:\?.*)?$/, routeUpdatesRedirect);
  router.add(/^#\/$/, routeOverview);
  router.setNotFound(routeOverview);

  const originalDispatch = router.dispatch.bind(router);
  router.dispatch = (hash) => {
    const route = hash || globalThis.location.hash || "#/";
    // The first route of a page load keeps the browser's natural focus start;
    // only later in-app navigations move focus to the new heading.
    focusRouteOnMount = lastDispatchedRoute !== null && route !== lastDispatchedRoute;
    shell.setNavVisible(true);
    originalDispatch(hash);
    lastDispatchedRoute = route;
    shell.setActiveRoute(route);
  };

  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      clearRefreshTimer();
      pauseBackgroundPolling();
    } else if (activeRefreshLoop) {
      activeRefreshLoop("scheduled");
      resumeBackgroundPolling();
    } else if (activePage) {
      router.dispatch();
      resumeBackgroundPolling();
    }
  });

  checkInitialAuth().then(() => {
    shell.setUsername(authState.username);
    router.start();
    startUpdateBannerPolling();
    startActiveAlertsPolling();
    startPaletteHostPolling();
  });
}

bootstrap();

// Exported for potential future test hooks; not used by index.html directly.
export { getHosts };
