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
