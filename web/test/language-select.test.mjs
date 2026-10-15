import { test } from "node:test";
import assert from "node:assert/strict";
import { applyLanguagePreference } from "../assets/js/ui/language-select.js";

test("language preference is persisted only after its destination catalog loads", async () => {
  const persisted = [];
  const loaded = [];
  const changed = await applyLanguagePreference("ko", {
    ensure: async (lang) => { loaded.push(lang); },
    persist: (pref) => persisted.push(pref),
  });
  assert.equal(changed, true);
  assert.deepEqual(loaded, ["ko"]);
  assert.deepEqual(persisted, ["ko"]);
});

test("a failed language catalog keeps the prior preference and reports an error", async () => {
  const persisted = [];
  let failures = 0;
  const changed = await applyLanguagePreference("ko", {
    ensure: async () => { throw new Error("catalog unavailable"); },
    persist: (pref) => persisted.push(pref),
    onFailure: () => { failures++; },
  });
  assert.equal(changed, false);
  assert.deepEqual(persisted, []);
  assert.equal(failures, 1);
});
