// Resolve actual opaque declarations; rgb(var(--hex)) must never pass by
// merely extracting the referenced hex and measuring that instead.
import { readFileSync } from "node:fs";
import { test } from "node:test";
import assert from "node:assert/strict";
import { contrastRatio, parseColor } from "../../scripts/ui-audit-lib.mjs";

const srcDir = new URL("../src/", import.meta.url);
const read = (path) => readFileSync(new URL(path, srcDir), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const tokensCSS = read("tokens.css");
const theme = tokensCSS.match(/@theme\s*\{([^{}]*)\}/)?.[1];
assert.ok(theme, "tokens.css needs a real @theme block");

function rule(css, selector) {
  const found = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find((match) => match[1].trim().split(",").map((part) => part.trim()).includes(selector));
  assert.ok(found, `${selector} needs an actual rule`);
  return found[2];
}
function background(css, selector) {
  const body = rule(css, selector);
  const token = body.match(/background-color:\s*var\((--color-[\w-]+)\)\s*;/)?.[1];
  assert.ok(token, `${selector} must use an opaque complete-color token, not rgb(hex) or an alpha fill`);
  const value = theme.match(new RegExp(`${token}:\\s*(#[0-9a-f]{6})\\s*;`, "i"))?.[1];
  assert.ok(value, `${token} must resolve to an opaque hex in @theme`);
  assert.ok(contrastRatio(parseColor("#ffffff"), parseColor(value)) >= 4.5, `${selector}: inverse text must clear AA on ${value}`);
  return token;
}

test("solid provider and metadata badges use actual valid AA backgrounds", () => {
  const css = read("components/status.css");
  for (const name of ["aws", "oci", "other", "r2", "hub", "update"]) background(css, `.cp-badge-${name}`);
  assert.match(rule(css, ".cp-badge"), /text-\[rgb\(var\(--text-inverse\)\)\]/);
});

test("danger button, bell badge and solid status pill share the same AA alarm fill", () => {
  const token = "--color-cp-badge-danger";
  assert.equal(background(read("base.css"), ".cp-btn-danger"), token);
  assert.equal(background(read("pages/alerts.css"), ".cp-navbar-bell-badge"), token);
  assert.equal(background(read("components/status.css"), ".cp-status-pill-solid"), token);
});
