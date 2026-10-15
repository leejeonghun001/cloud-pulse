import { test } from "node:test";
import assert from "node:assert/strict";
import { scrollActiveSettingsTab } from "../assets/js/pages/settings/index.js";

test("active settings tab is centered into the mobile scroll row on mount", () => {
  const calls = [];
  scrollActiveSettingsTab({ scrollIntoView: (options) => calls.push(options) });
  assert.deepEqual(calls, [{ inline: "center", block: "nearest" }]);
});
