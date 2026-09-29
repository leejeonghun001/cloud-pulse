// icons.test.mjs — sanity checks for assets/js/ui/icons.js's data-only
// exports (iconNames/hasIcon). The DOM-building icon() function itself
// requires document.createElementNS and is exercised by the visual
// check, not node --test.
import { test } from "node:test";
import assert from "node:assert/strict";
import { hasIcon, iconNames } from "../assets/js/ui/icons.js";

test("iconNames returns a non-empty list", () => {
  const names = iconNames();
  assert.ok(names.length > 30, `expected a substantial icon set, got ${names.length}`);
});

test("hasIcon recognizes known icons used by the shell/pages", () => {
  for (const name of ["activity", "search", "settings", "user", "logOut", "sun", "moon", "monitor", "eye", "eyeOff", "x", "plus"]) {
    assert.equal(hasIcon(name), true, `expected icon ${name} to exist`);
  }
});

test("hasIcon returns false for an unknown name", () => {
  assert.equal(hasIcon("definitely-not-an-icon"), false);
});
