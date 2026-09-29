// auth.test.mjs — unit tests for assets/js/core/auth.js's pure helpers
// (session storage uses globalThis.localStorage, stubbed here; the
// route-string helpers are pure).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  isLoginRoute,
  buildLoginRedirect,
  parseLoginNext,
  getSessionToken,
  setSessionToken,
  clearSessionToken,
  removeLegacyToken,
} from "../assets/js/core/auth.js";

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

test("isLoginRoute matches #/login with or without query", () => {
  assert.equal(isLoginRoute("#/login"), true);
  assert.equal(isLoginRoute("#/login?next=%23%2Fsettings"), true);
  assert.equal(isLoginRoute("#/"), false);
  assert.equal(isLoginRoute("#/settings"), false);
});

test("buildLoginRedirect wraps a non-login route with next=", () => {
  assert.equal(buildLoginRedirect("#/settings"), `#/login?next=${encodeURIComponent("#/settings")}`);
});

test("buildLoginRedirect defaults to bare #/login for empty/login hash", () => {
  assert.equal(buildLoginRedirect(""), "#/login");
  assert.equal(buildLoginRedirect("#/login"), "#/login");
});

test("parseLoginNext extracts next=, defaulting to #/", () => {
  assert.equal(parseLoginNext("#/login"), "#/");
  assert.equal(parseLoginNext(`#/login?next=${encodeURIComponent("#/settings")}`), "#/settings");
  assert.equal(parseLoginNext("#/login?next="), "#/");
});

test("parseLoginNext never redirects back into the login page itself", () => {
  assert.equal(parseLoginNext(`#/login?next=${encodeURIComponent("#/login")}`), "#/");
});

test("session token storage round-trips via localStorage", () => {
  withFakeLocalStorage(() => {
    assert.equal(getSessionToken(), "");
    setSessionToken("abc123");
    assert.equal(getSessionToken(), "abc123");
    clearSessionToken();
    assert.equal(getSessionToken(), "");
  });
});

test("removeLegacyToken deletes the pre-v0.4 cp_ui_token key", () => {
  withFakeLocalStorage((store) => {
    store.set("cp_ui_token", "old-static-token");
    removeLegacyToken();
    assert.equal(store.has("cp_ui_token"), false);
  });
});
