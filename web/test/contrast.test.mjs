import { test } from "node:test";
import assert from "node:assert/strict";
import { checkContrast, assertContrast } from "../../scripts/check-contrast.mjs";

test("text and badge token pairs meet WCAG AA in both themes", () => {
  assert.doesNotThrow(() => assertContrast());
  const results = checkContrast();
  assert.equal(results.length, 20);
  assert.ok(results.every(({ ratio, minimum }) => ratio >= minimum));
});
