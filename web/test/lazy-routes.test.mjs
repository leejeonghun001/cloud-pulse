import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const source = readFileSync(new URL("../assets/js/main.js", import.meta.url), "utf8");

function lazyRouteHarness() {
  const lifecycle = source.slice(source.indexOf("let activePage ="), source.indexOf("function clearLoginRoot("));
  const autoRefresh = source.slice(source.indexOf("function mountPageWithAutoRefresh("), source.indexOf("function routeOverview("));
  const lazyRoute = source.slice(source.indexOf("function mountLazyRoute("), source.indexOf("function routeHostDetail("));
  const container = { children: [], append(...nodes) { this.children.push(...nodes); } };
  const context = {
    __shell: { mainEl: container },
    REFRESH_INTERVAL_MS: 15000,
    document: { hidden: false },
    setTimeout: () => 1,
    clearTimeout() {},
    clearLoginRoot() {},
    clearChildren(node) { node.children = []; },
    errorBanner(text) { return { kind: "error", text }; },
    el(_tag, options) {
      return { ...options, listeners: {}, addEventListener(type, handler) { this.listeners[type] = handler; } };
    },
    t(key) { return key; },
    globalThis: { location: { reload() { container.reloaded = true; } } },
  };
  const api = runInNewContext(`${lifecycle}\n${autoRefresh}\n${lazyRoute}\nshell = __shell; ({ mountLazyRoute });`, context);
  return { ...context, ...api, container };
}

const settle = async () => { for (let i = 0; i < 6; i++) await Promise.resolve(); };

test("a failed lazy route offers a full reload instead of re-importing a mixed build", async () => {
  const { mountLazyRoute, container } = lazyRouteHarness();
  let attempts = 0;
  let mounts = 0;
  const load = () => {
    attempts++;
    return attempts === 1 ? Promise.reject(new Error("chunk unavailable")) : Promise.resolve({ page: true });
  };
  mountLazyRoute(load, () => {
    mounts++;
    return { refresh: async () => {}, teardown() {} };
  });
  await settle();
  assert.equal(container.children[0].kind, "error");
  assert.equal(container.children[1].text, "common.reload_page");
  container.children[1].listeners.click();
  await settle();
  assert.equal(container.reloaded, true);
  assert.equal(attempts, 1);
  assert.equal(mounts, 0);
});

test("main keeps overview eager and puts route-only pages behind dynamic imports", () => {
  assert.match(source, /import \{ mountOverviewPage \} from "\.\/pages\/overview\.js"/);
  for (const route of ["host", "compare", "settings/index", "alerts", "costs", "rollout"]) {
    assert.match(source, new RegExp(`import\\("\\./pages/${route.replace("/", "\\/")}\\.js"\\)`));
  }
  assert.doesNotMatch(source, /import \{ mount(?:HostDetail|Compare|Settings|Alerts|Costs|Rollout)Page \} from/);
});
