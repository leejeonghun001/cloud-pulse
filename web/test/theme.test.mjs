// theme.test.mjs — unit tests for assets/js/core/theme.js's pure
// resolveIsDark/cycleTheme helpers and the localStorage-backed
// get/setStoredTheme.
import { test } from "node:test";
import assert from "node:assert/strict";
import { getStoredTheme, setStoredTheme, resolveIsDark, cycleTheme } from "../assets/js/core/theme.js";

function withFakeLocalStorage(fn) {
  const store = new Map();
  const original = globalThis.localStorage;
  globalThis.localStorage = {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
  };
  try {
    fn(store);
  } finally {
    globalThis.localStorage = original;
  }
}

test("getStoredTheme defaults to system when unset", () => {
  withFakeLocalStorage(() => {
    assert.equal(getStoredTheme(), "system");
  });
});

test("setStoredTheme/getStoredTheme round-trip valid values", () => {
  withFakeLocalStorage(() => {
    setStoredTheme("dark");
    assert.equal(getStoredTheme(), "dark");
    setStoredTheme("light");
    assert.equal(getStoredTheme(), "light");
  });
});

test("getStoredTheme ignores an invalid stored value", () => {
  withFakeLocalStorage((store) => {
    store.set("cp_theme", "not-a-real-theme");
    assert.equal(getStoredTheme(), "system");
  });
});

test("resolveIsDark: dark/light are explicit", () => {
  assert.equal(resolveIsDark("dark"), true);
  assert.equal(resolveIsDark("light"), false);
});

test("resolveIsDark: system uses the injected systemPrefersDark override", () => {
  assert.equal(resolveIsDark("system", true), true);
  assert.equal(resolveIsDark("system", false), false);
});

test("cycleTheme: system -> light -> dark -> system", () => {
  assert.equal(cycleTheme("system"), "light");
  assert.equal(cycleTheme("light"), "dark");
  assert.equal(cycleTheme("dark"), "system");
});
