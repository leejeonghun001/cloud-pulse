// Guards WCAG 2.2 AA 2.5.8 (24x24 minimum target) fixes that the UI audit
// found: standalone host-name links in the monthly-traffic rows used to be
// 20 px tall on every viewport.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, "..", "assets", "app.css"), "utf8");

test("monthly-traffic host links have a 24px minimum target height", () => {
  const m = css.match(/\.cp-egress-row-head a\{([^}]*)\}/g) || [];
  const rules = m.join(" ");
  assert.match(rules, /min-height:calc\(var\(--spacing\) \* 6\)|min-height:1\.5rem|min-height:24px/, "expected min-height >= 24px on .cp-egress-row-head a");
  assert.match(rules, /display:inline-flex/);
});
