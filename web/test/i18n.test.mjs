// i18n.test.mjs — unit tests for assets/js/core/i18n.js's pure t()
// lookup/fallback/plural logic and the localStorage-backed
// get/setLang preference, mirroring theme.test.mjs's fake-localStorage
// pattern.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  setCatalogs,
  getLang,
  getLangPreference,
  setLang,
  detectAutoLang,
  htmlLang,
  t,
  loadCatalogs,
  ensureCatalog,
  SUPPORTED_LANGUAGES,
} from "../assets/js/core/i18n.js";

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

async function withFakeLocalStorageAsync(fn) {
  const store = new Map();
  const original = globalThis.localStorage;
  globalThis.localStorage = {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
  };
  try {
    return await fn(store);
  } finally {
    globalThis.localStorage = original;
  }
}

function withFakeNavigatorLanguage(lang, fn) {
  const original = Object.getOwnPropertyDescriptor(globalThis, "navigator");
  Object.defineProperty(globalThis, "navigator", {
    value: { language: lang },
    configurable: true,
  });
  try {
    fn();
  } finally {
    if (original) Object.defineProperty(globalThis, "navigator", original);
    else delete globalThis.navigator;
  }
}

const SAMPLE_CATALOGS = {
  en: {
    common: { hello: "Hello, {name}!", cancel: "Cancel" },
    nav: { host_count: { one: "{count} host", other: "{count} hosts" } },
  },
  ko: {
    common: { hello: "안녕하세요, {name}님!" },
    nav: { host_count: { one: "호스트 {count}개", other: "호스트 {count}개" } },
  },
};

test("SUPPORTED_LANGUAGES lists en and ko", () => {
  assert.deepEqual([...SUPPORTED_LANGUAGES].sort(), ["en", "ko"]);
});

test("getLangPreference defaults to auto when unset", () => {
  withFakeLocalStorage(() => {
    assert.equal(getLangPreference(), "auto");
  });
});

test("setLang/getLangPreference round-trip valid values", () => {
  withFakeLocalStorage(() => {
    setLang("ko");
    assert.equal(getLangPreference(), "ko");
    setLang("en");
    assert.equal(getLangPreference(), "en");
    setLang("auto");
    assert.equal(getLangPreference(), "auto");
  });
});

test("getLangPreference ignores an invalid stored value", () => {
  withFakeLocalStorage((store) => {
    store.set("cp_lang", "not-a-real-lang");
    assert.equal(getLangPreference(), "auto");
  });
});

test("detectAutoLang recognizes Korean variants", () => {
  withFakeNavigatorLanguage("ko-KR", () => {
    assert.equal(detectAutoLang(), "ko");
  });
  withFakeNavigatorLanguage("ko", () => {
    assert.equal(detectAutoLang(), "ko");
  });
});

test("detectAutoLang falls back to English for non-Korean locales", () => {
  withFakeNavigatorLanguage("fr-FR", () => {
    assert.equal(detectAutoLang(), "en");
  });
  withFakeNavigatorLanguage("de", () => {
    assert.equal(detectAutoLang(), "en");
  });
});

test("getLang resolves auto via navigator.language", () => {
  withFakeLocalStorage(() => {
    withFakeNavigatorLanguage("ko-KR", () => {
      setLang("auto");
      assert.equal(getLang(), "ko");
    });
  });
});

test("getLang returns the concrete preference when set", () => {
  withFakeLocalStorage(() => {
    withFakeNavigatorLanguage("ko-KR", () => {
      setLang("en");
      assert.equal(getLang(), "en");
    });
  });
});

test("htmlLang returns ko only when the resolved language is ko", () => {
  withFakeLocalStorage(() => {
    setLang("ko");
    assert.equal(htmlLang(), "ko");
    setLang("en");
    assert.equal(htmlLang(), "en");
  });
});

test("t() resolves a plain key with a placeholder substituted", () => {
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("en");
    assert.equal(t("common.hello", { name: "Ada" }), "Hello, Ada!");
    setLang("ko");
    assert.equal(t("common.hello", { name: "Ada" }), "안녕하세요, Ada님!");
  });
});

test("t() falls back to English when the key is missing in the active language", () => {
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("ko");
    // "common.cancel" only exists in the English sample catalog above.
    assert.equal(t("common.cancel"), "Cancel");
  });
});

test("t() returns the bare key when missing from every catalog", () => {
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("en");
    assert.equal(t("does.not.exist"), "does.not.exist");
  });
});

test("t() leaves an unmatched placeholder untouched", () => {
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("en");
    assert.equal(t("common.hello", {}), "Hello, {name}!");
  });
});

test("t() selects the plural sub-key from a numeric count param", () => {
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("en");
    assert.equal(t("nav.host_count", { count: 1 }), "1 host");
    assert.equal(t("nav.host_count", { count: 5 }), "5 hosts");
    assert.equal(t("nav.host_count", { count: 0 }), "0 hosts");
  });
});

test("t() plural form also works in Korean (identical one/other text)", () => {
  withFakeLocalStorage(() => {
    setCatalogs(SAMPLE_CATALOGS);
    setLang("ko");
    assert.equal(t("nav.host_count", { count: 1 }), "호스트 1개");
    assert.equal(t("nav.host_count", { count: 5 }), "호스트 5개");
  });
});

test("loadCatalogs fetches only the active catalog and ensureCatalog lazily loads a switch target", async () => {
  const originalFetch = globalThis.fetch;
  const requests = [];
  globalThis.fetch = async (path) => {
    requests.push(path);
    return { ok: true, json: async () => path.endsWith("ko.json") ? { common: { hello: "안녕하세요" } } : { common: { hello: "Hello" } } };
  };
  try {
    await withFakeLocalStorageAsync(async () => {
      setCatalogs({ en: {}, ko: {} });
      setLang("ko");
      await loadCatalogs();
      assert.deepEqual(requests, ["/assets/i18n/ko.json"]);
      await ensureCatalog("en");
      assert.deepEqual(requests, ["/assets/i18n/ko.json", "/assets/i18n/en.json"]);
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
});
