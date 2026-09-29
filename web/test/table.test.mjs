// table.test.mjs — unit tests for assets/js/ui/table.js's pure
// nextSortState/sortRows helpers.
import { test } from "node:test";
import assert from "node:assert/strict";
import { nextSortState, sortRows } from "../assets/js/ui/table.js";

test("nextSortState: starts ascending for a new column", () => {
  assert.deepEqual(nextSortState(null, "cpu"), { key: "cpu", direction: "asc" });
});

test("nextSortState: toggles direction on the same column", () => {
  assert.deepEqual(nextSortState({ key: "cpu", direction: "asc" }, "cpu"), { key: "cpu", direction: "desc" });
  assert.deepEqual(nextSortState({ key: "cpu", direction: "desc" }, "cpu"), { key: "cpu", direction: "asc" });
});

test("nextSortState: switching columns resets to ascending", () => {
  assert.deepEqual(nextSortState({ key: "cpu", direction: "desc" }, "mem"), { key: "mem", direction: "asc" });
});

test("sortRows: ascending numeric sort", () => {
  const rows = [{ v: 3 }, { v: 1 }, { v: 2 }];
  const sorted = sortRows(rows, { key: "v", direction: "asc" }, { v: (r) => r.v });
  assert.deepEqual(sorted.map((r) => r.v), [1, 2, 3]);
});

test("sortRows: descending sort", () => {
  const rows = [{ v: 3 }, { v: 1 }, { v: 2 }];
  const sorted = sortRows(rows, { key: "v", direction: "desc" }, { v: (r) => r.v });
  assert.deepEqual(sorted.map((r) => r.v), [3, 2, 1]);
});

test("sortRows: does not mutate the input array", () => {
  const rows = [{ v: 3 }, { v: 1 }];
  const original = rows.slice();
  sortRows(rows, { key: "v", direction: "asc" }, { v: (r) => r.v });
  assert.deepEqual(rows, original);
});

test("sortRows: returns a shallow copy unsorted when sort/accessor is missing", () => {
  const rows = [{ v: 3 }, { v: 1 }];
  assert.deepEqual(sortRows(rows, null, { v: (r) => r.v }), rows);
  assert.deepEqual(sortRows(rows, { key: "missing", direction: "asc" }, { v: (r) => r.v }), rows);
});

test("sortRows: string values sort lexicographically", () => {
  const rows = [{ name: "b" }, { name: "a" }, { name: "c" }];
  const sorted = sortRows(rows, { key: "name", direction: "asc" }, { name: (r) => r.name });
  assert.deepEqual(sorted.map((r) => r.name), ["a", "b", "c"]);
});
