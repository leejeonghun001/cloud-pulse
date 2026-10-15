// api-errors.test.mjs — unit tests for core/api.js's localizeApiError,
// which maps a server-provided models.APIError.code onto the web
// dashboard's `errors.*` i18n catalog (SPEC-v0.8 §3-3), falling back to
// the server's own English message for an empty/unrecognized code.
// Uses a tiny local DOM fake (see billing-page.test.mjs) purely so
// core/api.js's module-level import of ui/toast.js doesn't throw in a
// plain Node test environment; localizeApiError itself never touches
// the DOM.
import { test } from "node:test";
import assert from "node:assert/strict";
import { setCatalogs, setLang } from "../assets/js/core/i18n.js";

function fakeDocument() {
  return {
    createElement() {
      return { className: "", style: {}, append() {}, setAttribute() {}, remove() {} };
    },
    body: { append() {} },
  };
}

function withFakeLocalStorage(fn) {
  const store = new Map();
  const original = globalThis.localStorage;
  globalThis.localStorage = {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
  };
  try {
    return fn();
  } finally {
    globalThis.localStorage = original;
  }
}

const SAMPLE_CATALOGS = {
  en: { errors: { generic: "Something went wrong.", host_not_found: "That host wasn't found." } },
  ko: { errors: { generic: "문제가 발생했습니다.", host_not_found: "해당 호스트를 찾을 수 없습니다." } },
};

async function loadApi() {
  const previousDocument = globalThis.document;
  globalThis.document = fakeDocument();
  try {
    return await import("../assets/js/core/api.js");
  } finally {
    if (previousDocument === undefined) delete globalThis.document;
    else globalThis.document = previousDocument;
  }
}

test("localizeApiError resolves a known code via the errors.* catalog (en)", async () => {
  const { ApiError, localizeApiError } = await loadApi();
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("en");
    const err = new ApiError("host not found", 404, "host_not_found");
    assert.equal(localizeApiError(err), "That host wasn't found.");
  });
});

test("localizeApiError resolves a known code via the errors.* catalog (ko)", async () => {
  const { ApiError, localizeApiError } = await loadApi();
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("ko");
    const err = new ApiError("host not found", 404, "host_not_found");
    assert.equal(localizeApiError(err), "해당 호스트를 찾을 수 없습니다.");
  });
});

test("localizeApiError falls back to the server message for an unrecognized code", async () => {
  const { ApiError, localizeApiError } = await loadApi();
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("en");
    const err = new ApiError("some brand-new server message", 400, "some_future_code");
    assert.equal(localizeApiError(err), "some brand-new server message");
  });
});

test("localizeApiError falls back to the server message when code is empty", async () => {
  const { ApiError, localizeApiError } = await loadApi();
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("en");
    const err = new ApiError("plain message, no code", 500);
    assert.equal(localizeApiError(err), "plain message, no code");
  });
});

test("localizeApiError falls back to the generic catalog key for a non-ApiError", async () => {
  const { localizeApiError } = await loadApi();
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("en");
    assert.equal(localizeApiError(new TypeError("boom")), "Something went wrong.");
  });
});

function hostsResponse(id) {
  return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ hosts: [{ host: { id } }] }) };
}

test("palette never joins an in-flight page request carrying its AbortSignal", async () => {
  const { getHosts, getRecentHosts, getRecentOrPendingHosts, clearHostsCache } = await loadApi();
  const previousFetch = globalThis.fetch;
  try {
    clearHostsCache();
    const pending = [];
    globalThis.fetch = (_path, init) => new Promise((resolve) => pending.push({ resolve, signal: init.signal }));
    const pageController = new AbortController();
    const pageRequest = getHosts(pageController.signal);
    assert.equal(getRecentOrPendingHosts(30000), null);
    const paletteRequest = getHosts();
    assert.equal(pending.length, 2);
    assert.equal(pending[0].signal, pageController.signal);
    assert.equal(pending[1].signal, undefined);
    pending[1].resolve(hostsResponse("palette"));
    pending[0].resolve(hostsResponse("page"));
    await Promise.all([pageRequest, paletteRequest]);
    assert.equal(getRecentHosts(30000).hosts[0].host.id, "palette");
  } finally {
    globalThis.fetch = previousFetch;
  }
});

test("host cache clears on session loss and late older responses cannot overwrite newer data", async () => {
  const { getHosts, getRecentHosts, clearHostsCache } = await loadApi();
  const previousFetch = globalThis.fetch;
  try {
    clearHostsCache();
    const pending = [];
    globalThis.fetch = () => new Promise((resolve) => pending.push(resolve));
    const older = getHosts();
    const newer = getHosts();
    pending[1](hostsResponse("new"));
    await newer;
    pending[0](hostsResponse("old"));
    await older;
    assert.equal(getRecentHosts(30000).hosts[0].host.id, "new");
    clearHostsCache();
    assert.equal(getRecentHosts(30000), null);
  } finally {
    globalThis.fetch = previousFetch;
  }
});

test("a 401 clears cached hosts before the unauthorized redirect", async () => {
  const { getHosts, getRecentHosts, clearHostsCache } = await loadApi();
  const previousFetch = globalThis.fetch;
  try {
    clearHostsCache();
    globalThis.fetch = async () => hostsResponse("before-401");
    await getHosts();
    assert.ok(getRecentHosts(30000));
    globalThis.fetch = async () => ({
      ok: false,
      status: 401,
      headers: { get: () => null },
      json: async () => ({ error: "expired", code: "session_expired" }),
    });
    await assert.rejects(getHosts());
    assert.equal(getRecentHosts(30000), null);
  } finally {
    globalThis.fetch = previousFetch;
  }
});
