// Regression coverage for badge colors that are rendered with .cp-badge's
// 10px white text. Keep this source-level so it is deterministic and does not
// require a browser or a Tailwind installation.
import { readFileSync } from "node:fs";
import { test } from "node:test";
import assert from "node:assert/strict";
import { contrastRatio, parseColor } from "../../scripts/ui-audit-lib.mjs";

const baseCSS = readFileSync(new URL("../src/base.css", import.meta.url), "utf8");

test("update badge uses an opaque WCAG-AA background for white 10px text", () => {
  const rule = baseCSS.match(/\.cp-badge-update\s*\{([\s\S]*?)\n  \}/);
  assert.ok(rule, "update badge rule must exist");
  assert.doesNotMatch(rule[1], /bg-cp-warning\/90/);

  const color = rule[1].match(/background-color:\s*(#[0-9a-f]{6});/i)?.[1];
  assert.ok(color, "update badge must define an opaque hex background color");
  assert.ok(
    contrastRatio(parseColor("#ffffff"), parseColor(color)) >= 4.5,
    `white text must have WCAG AA contrast on ${color}`,
  );
});
