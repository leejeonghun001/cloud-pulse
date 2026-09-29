// router.test.mjs — unit tests for assets/js/core/router.js's route
// resolution and auth-guard dispatch logic. Uses a minimal fake for
// globalThis.location/addEventListener so this stays a pure node --test
// (no jsdom/browser required).
import { test } from "node:test";
import assert from "node:assert/strict";
import { Router } from "../assets/js/core/router.js";

function withFakeGlobals(initialHash, fn) {
  const original = { location: globalThis.location, addEventListener: globalThis.addEventListener };
  const state = { hash: initialHash };
  globalThis.location = new Proxy(state, {
    get(target, prop) {
      if (prop === "hash") return target.hash;
      return undefined;
    },
    set(target, prop, value) {
      if (prop === "hash") target.hash = value;
      return true;
    },
  });
  globalThis.addEventListener = () => {};
  try {
    fn(state);
  } finally {
    globalThis.location = original.location;
    globalThis.addEventListener = original.addEventListener;
  }
}

test("resolve matches a route pattern and extracts query params", () => {
  const router = new Router({ isAuthenticated: () => true });
  router.add(/^#\/host\/([^/?]+)$/, () => {});
  const resolved = router.resolve("#/host/abc123?foo=bar");
  assert.ok(resolved);
  assert.equal(resolved.match[1], "abc123");
  assert.equal(resolved.query.get("foo"), "bar");
});

test("resolve returns null for an unmatched hash", () => {
  const router = new Router({ isAuthenticated: () => true });
  router.add(/^#\/settings$/, () => {});
  assert.equal(router.resolve("#/nope"), null);
});

test("dispatch redirects an unauthenticated guarded route to login with next=", () => {
  withFakeGlobals("#/settings", (state) => {
    const router = new Router({ isAuthenticated: () => false });
    let overviewCalled = false;
    router.add(/^#\/login(?:\?.*)?$/, () => {}, { public: true });
    router.add(/^#\/settings$/, () => {
      overviewCalled = true;
    });
    router.dispatch("#/settings");
    assert.equal(overviewCalled, false);
    assert.equal(state.hash, `#/login?next=${encodeURIComponent("#/settings")}`);
  });
});

test("dispatch runs the handler directly for a public route regardless of auth", () => {
  withFakeGlobals("#/login", () => {
    const router = new Router({ isAuthenticated: () => false });
    let called = false;
    router.add(/^#\/login(?:\?.*)?$/, () => {
      called = true;
    }, { public: true });
    router.dispatch("#/login");
    assert.equal(called, true);
  });
});

test("dispatch bounces an authenticated visit to #/login back to next", () => {
  withFakeGlobals("#/login?next=%23%2Fsettings", (state) => {
    const router = new Router({ isAuthenticated: () => true });
    router.add(/^#\/login(?:\?.*)?$/, () => {}, { public: true });
    router.dispatch("#/login?next=%23%2Fsettings");
    assert.equal(state.hash, "#/settings");
  });
});

test("dispatch runs the matched handler for an authenticated guarded route", () => {
  withFakeGlobals("#/", () => {
    const router = new Router({ isAuthenticated: () => true });
    let called = false;
    router.add(/^#\/$/, () => {
      called = true;
    });
    router.dispatch("#/");
    assert.equal(called, true);
  });
});

test("dispatch falls back to the notFound handler for an unmatched authenticated route", () => {
  withFakeGlobals("#/", () => {
    const router = new Router({ isAuthenticated: () => true });
    let notFoundCalled = false;
    router.setNotFound(() => {
      notFoundCalled = true;
    });
    router.dispatch("#/does-not-exist");
    assert.equal(notFoundCalled, true);
  });
});

test("navigate re-dispatches directly when the target hash is already current", () => {
  withFakeGlobals("#/", (state) => {
    const router = new Router({ isAuthenticated: () => true });
    let calls = 0;
    router.add(/^#\/$/, () => {
      calls++;
    });
    router.navigate("#/");
    assert.equal(calls, 1);
    assert.equal(state.hash, "#/");
  });
});
