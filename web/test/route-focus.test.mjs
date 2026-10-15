import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const source = readFileSync(new URL("../assets/js/main.js", import.meta.url), "utf8");
const start = source.indexOf("function completeRouteChange(");
let depth = 0, end = -1;
for (let i = source.indexOf("{", start); i < source.length; i++) {
  if (source[i] === "{") depth++;
  if (source[i] === "}" && --depth === 0) { end = i + 1; break; }
}
const completeRouteChange = source.slice(start, end);

function routeFocus(active, { hasHeading = true, routeTitle = "Alerts" } = {}) {
  const heading = { textContent: "Alerts", attributes: {}, focusCalls: 0, setAttribute(key, value) { this.attributes[key] = value; }, focus() { this.focusCalls++; } };
  const main = { attributes: {}, focusCalls: 0, focusOptions: null, querySelector: () => hasHeading ? heading : null, setAttribute(key, value) { this.attributes[key] = value; }, focus(options) { this.focusCalls++; this.focusOptions = options; } };
  const scrolls = [], announcements = [];
  const api = runInNewContext(`let focusRouteOnMount = ${active}; ${completeRouteChange}; ({ run: completeRouteChange, active: () => focusRouteOnMount })`, {
    shell: { mainEl: main },
    announce: (message) => announcements.push(message),
    globalThis: { scrollTo: (options) => scrolls.push(options) },
  });
  api.run(routeTitle);
  return { heading, main, scrolls, announcements, active: api.active() };
}

test("route change focuses and announces the h1", () => {
  const result = routeFocus(true);
  assert.equal(result.heading.attributes.tabindex, "-1");
  assert.equal(result.heading.focusCalls, 1);
  assert.deepEqual(result.announcements, ["Alerts"]);
  assert.equal(result.scrolls.length, 1);
});

test("refresh route completion does not move focus", () => {
  const result = routeFocus(false);
  assert.equal(result.heading.focusCalls, 0);
  assert.equal(result.scrolls.length, 0);
});

test("an async route without an h1 keeps focus in main and announces its nav title", () => {
  const result = routeFocus(true, { hasHeading: false, routeTitle: "Rollout" });
  assert.equal(result.main.attributes.tabindex, "-1");
  assert.equal(result.main.focusCalls, 1);
  assert.equal(result.main.focusOptions.focusVisible, false);
  assert.deepEqual(result.announcements, ["Rollout"]);
});
