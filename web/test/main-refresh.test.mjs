import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const source = readFileSync(new URL("../assets/js/main.js", import.meta.url), "utf8");
function shellRefresh() {
  const timers = new Map();
  let timerID = 0, dispatches = 0, pauses = 0, resumes = 0;
  const document = { hidden: false };
  const lifecycle = source.slice(source.indexOf("let activePage ="), source.indexOf("function clearLoginRoot("));
  const mount = source.slice(source.indexOf("function mountPageWithAutoRefresh("), source.indexOf("function routeOverview("));
  const visibility = source.match(/document\.addEventListener\("visibilitychange", \(\) => \{([\s\S]*?)\n  \}\);/)[1];
  const api = runInNewContext(`${lifecycle}\n${mount}\n({ mountPageWithAutoRefresh, teardownActivePage, visibility: () => {${visibility}} });`, {
    document, REFRESH_INTERVAL_MS: 15000,
    setTimeout: (fn) => { timers.set(++timerID, fn); return timerID; },
    clearTimeout: (id) => timers.delete(id),
    router: { dispatch() { dispatches++; } },
    pauseBackgroundPolling() { pauses++; },
    resumeBackgroundPolling() { resumes++; },
  });
  return { api, document, timers, dispatches: () => dispatches, pauses: () => pauses, resumes: () => resumes };
}
const settle = async () => { for (let i = 0; i < 6; i++) await Promise.resolve(); };

test("real shell sends initial/scheduled reasons and resumes an auto-refresh page without remounting it", async () => {
  const { api, document, timers, dispatches, pauses, resumes } = shellRefresh();
  const reasons = [];
  let tornDown = 0;
  const handle = { refresh: async (options) => reasons.push(options.reason), teardown() { tornDown++; } };
  api.mountPageWithAutoRefresh(handle);
  await settle();
  assert.deepEqual(reasons, ["initial"]);
  await [...timers.values()][0]();
  assert.deepEqual(reasons, ["initial", "scheduled"]);
  document.hidden = true; api.visibility();
  assert.equal(timers.size, 0);
  assert.equal(pauses(), 1, "background pollers pause with the page refresh loop");
  document.hidden = false; api.visibility();
  await settle();
  assert.deepEqual(reasons, ["initial", "scheduled", "scheduled"]);
  assert.equal(dispatches(), 0, "tab visibility must not discard the loaded Activity page");
  assert.equal(tornDown, 0);
  assert.equal(timers.size, 1);
  assert.equal(resumes(), 1, "background pollers refresh once when visible");
  api.teardownActivePage();
  assert.equal(timers.size, 0);
  assert.equal(tornDown, 1);
});

test("refresh options stay optional for existing pages and old scheduled callbacks cannot refresh a replaced page", async () => {
  const { api, timers } = shellRefresh();
  let firstCalls = 0, secondCalls = 0, firstTeardowns = 0;
  // Existing page refresh functions take no arguments and keep working.
  api.mountPageWithAutoRefresh({ refresh: async () => { firstCalls++; }, teardown() { firstTeardowns++; } });
  await settle();
  const oldTimer = [...timers.values()][0];
  api.mountPageWithAutoRefresh({ refresh: async () => { secondCalls++; }, teardown() {} });
  await settle();
  await oldTimer();
  assert.equal(firstCalls, 1);
  assert.equal(secondCalls, 1);
  assert.equal(firstTeardowns, 1);
  assert.equal(timers.size, 1);
  api.teardownActivePage();
});
