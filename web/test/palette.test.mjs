// palette.test.mjs — unit tests for assets/js/ui/palette.js's pure
// fuzzyScore/filterItems helpers (DOM-building parts of palette.js are
// exercised via the visual/manual check, not node --test).
import { test } from "node:test";
import assert from "node:assert/strict";
import { fuzzyScore, filterItems } from "../assets/js/ui/palette.js";

test("fuzzyScore: exact match scores highest", () => {
  const exact = fuzzyScore("settings", "settings");
  const prefix = fuzzyScore("set", "settings");
  const subsequence = fuzzyScore("stg", "settings");
  assert.ok(exact > prefix);
  assert.ok(prefix > subsequence);
});

test("fuzzyScore: empty query matches everything with score 0", () => {
  assert.equal(fuzzyScore("", "anything"), 0);
});

test("fuzzyScore: -1 when query characters aren't a subsequence", () => {
  assert.equal(fuzzyScore("xyz", "settings"), -1);
});

test("fuzzyScore: case-insensitive", () => {
  assert.ok(fuzzyScore("SET", "settings") >= 0);
});

test("filterItems: empty query returns items unchanged", () => {
  const items = [{ label: "a" }, { label: "b" }];
  assert.deepEqual(filterItems(items, ""), items);
});

test("filterItems: filters out non-matches and sorts best-first", () => {
  const items = [
    { id: "1", label: "Settings" },
    { id: "2", label: "Sign out" },
    { id: "3", label: "Overview" },
  ];
  const result = filterItems(items, "s");
  assert.ok(result.length >= 2);
  assert.ok(result.every((r) => r.label.toLowerCase().includes("s")));
});

test("filterItems: prefix match ranks above subsequence match", () => {
  const items = [
    { id: "sub", label: "xoverviewx" },
    { id: "pre", label: "overview page" },
  ];
  const result = filterItems(items, "overview");
  assert.equal(result[0].id, "pre");
});
