// table.js — sortable-header table primitives shared by the overview
// (hosts table) and other list pages. Sort state is a pure data
// structure so it can be persisted (localStorage) and unit-tested
// without a browser.
import { el } from "./components.js";
import { icon } from "./icons.js";

/**
 * @typedef {Object} SortState
 * @property {string} key
 * @property {"asc"|"desc"} direction
 */

/**
 * nextSortState computes the new sort state when a column header with
 * key `key` is activated, given the current state (or null): the same
 * column toggles direction; a different column starts at "asc".
 * @param {SortState|null} current
 * @param {string} key
 * @returns {SortState}
 */
export function nextSortState(current, key) {
  if (current && current.key === key) {
    return { key, direction: current.direction === "asc" ? "desc" : "asc" };
  }
  return { key, direction: "asc" };
}

/**
 * sortRows sorts a copy of rows by the given sort state using a
 * per-key value accessor map; a missing accessor leaves rows unsorted
 * (returns a shallow copy in original order).
 * @param {Array} rows
 * @param {SortState|null} sort
 * @param {Object<string, (row: any) => (string|number)>} accessors
 * @returns {Array}
 */
export function sortRows(rows, sort, accessors) {
  const copy = rows.slice();
  if (!sort || !accessors[sort.key]) return copy;
  const accessor = accessors[sort.key];
  const dir = sort.direction === "desc" ? -1 : 1;
  copy.sort((a, b) => {
    const va = accessor(a);
    const vb = accessor(b);
    if (va < vb) return -1 * dir;
    if (va > vb) return 1 * dir;
    return 0;
  });
  return copy;
}

/**
 * sortableHeader builds a <th> with a sort toggle button, aria-sort,
 * and a chevron icon reflecting the current direction (or none when
 * this column isn't the active sort key).
 * @param {Object} opts
 * @param {string} opts.label
 * @param {string} opts.sortKey
 * @param {SortState|null} opts.currentSort
 * @param {(key: string) => void} opts.onSort
 * @returns {HTMLElement}
 */
export function sortableHeader({ label, sortKey, currentSort, onSort }) {
  const active = currentSort?.key === sortKey;
  const ariaSort = active ? (currentSort.direction === "asc" ? "ascending" : "descending") : "none";

  const th = el("th", { attrs: { "aria-sort": ariaSort } });
  const btn = el("button", {
    class: "cp-btn-ghost cp-btn cp-btn-sm",
    attrs: { type: "button" },
  });
  btn.append(el("span", { text: label }));
  if (active) {
    btn.append(icon(currentSort.direction === "asc" ? "chevronUp" : "chevronDown"));
  }
  btn.addEventListener("click", () => onSort(sortKey));
  th.append(btn);
  return th;
}
