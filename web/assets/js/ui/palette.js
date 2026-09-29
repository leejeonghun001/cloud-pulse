// palette.js — command palette (Ctrl/Cmd+K or "/"): fuzzy-filterable
// list of hosts, pages, settings sections, and actions (toggle theme,
// sign out, add agent). Arrow keys + Enter to select, Esc to close.
import { el } from "./components.js";
import { icon } from "./icons.js";

/**
 * @typedef {Object} PaletteItem
 * @property {string} id
 * @property {string} label
 * @property {string} [group]
 * @property {string} [iconName]
 * @property {string} [hint] small trailing text (e.g. a hostname's status)
 * @property {() => void} onSelect
 */

/**
 * fuzzyScore returns a match score for query against text (higher is
 * better), or -1 for no match. A simple subsequence matcher: every
 * character of the (lowercased) query must appear in order in text;
 * consecutive/early matches score higher. Pure function, easily
 * unit-tested.
 * @param {string} query
 * @param {string} text
 * @returns {number}
 */
export function fuzzyScore(query, text) {
  const q = query.toLowerCase();
  const t = text.toLowerCase();
  if (q === "") return 0;
  if (q === t) return 1000;
  if (t.startsWith(q)) return 500 + (100 - Math.min(t.length, 100));

  let qi = 0;
  let score = 0;
  let streak = 0;
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] === q[qi]) {
      qi++;
      streak++;
      score += streak;
    } else {
      streak = 0;
    }
  }
  return qi === q.length ? score : -1;
}

/**
 * filterItems ranks items by fuzzy match against query, returning only
 * matches sorted best-first. Empty query returns items unchanged
 * (original order, grouped).
 * @param {PaletteItem[]} items
 * @param {string} query
 * @returns {PaletteItem[]}
 */
export function filterItems(items, query) {
  const q = query.trim();
  if (!q) return items;
  return items
    .map((item) => ({ item, score: fuzzyScore(q, item.label) }))
    .filter((x) => x.score >= 0)
    .sort((a, b) => b.score - a.score)
    .map((x) => x.item);
}

/**
 * createPalette builds the palette <dialog>, wired for filtering,
 * arrow-key navigation, and Enter/click selection. getItems() is
 * called fresh each time the palette opens so its contents (hosts,
 * current theme label, etc.) are always current.
 * @param {() => PaletteItem[]} getItems
 * @returns {{dialog: HTMLDialogElement, open: () => void, close: () => void}}
 */
export function createPalette(getItems) {
  const dialog = /** @type {HTMLDialogElement} */ (document.createElement("dialog"));
  dialog.className = "cp-palette";
  dialog.setAttribute("aria-label", "Command palette");

  const inputRow = el("div", { class: "cp-palette-input-row" });
  inputRow.append(icon("search"));
  const input = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-palette-input",
      attrs: { type: "text", placeholder: "Search hosts, pages, settings, actions…", "aria-label": "Command palette search" },
    })
  );
  inputRow.append(input);
  dialog.append(inputRow);

  const list = el("div", { class: "cp-palette-list", attrs: { role: "listbox" } });
  dialog.append(list);

  let items = [];
  let filtered = [];
  let selectedIndex = 0;
  let previouslyFocused = null;

  function render() {
    while (list.firstChild) list.removeChild(list.firstChild);
    filtered = filterItems(items, input.value);

    if (filtered.length === 0) {
      list.append(el("p", { class: "cp-palette-empty", text: "No matches." }));
      return;
    }

    let lastGroup = null;
    filtered.forEach((item, i) => {
      if (item.group && item.group !== lastGroup) {
        list.append(el("div", { class: "cp-palette-group-label", text: item.group }));
        lastGroup = item.group;
      }
      const row = el("div", {
        class: "cp-palette-item",
        attrs: { role: "option", "aria-selected": i === selectedIndex ? "true" : "false", id: `cp-palette-item-${i}` },
      });
      if (item.iconName) row.append(icon(item.iconName));
      row.append(el("span", { text: item.label, attrs: { style: "flex:1" } }));
      if (item.hint) row.append(el("span", { class: "cp-muted-small", text: item.hint }));
      row.addEventListener("mouseenter", () => {
        selectedIndex = i;
        updateSelection();
      });
      row.addEventListener("click", () => select(i));
      list.append(row);
    });
    updateSelection();
  }

  function updateSelection() {
    const rows = list.querySelectorAll('[role="option"]');
    rows.forEach((row, i) => {
      row.setAttribute("aria-selected", i === selectedIndex ? "true" : "false");
    });
    input.setAttribute("aria-activedescendant", `cp-palette-item-${selectedIndex}`);
    rows[selectedIndex]?.scrollIntoView({ block: "nearest" });
  }

  function select(index) {
    const item = filtered[index];
    if (!item) return;
    close();
    item.onSelect();
  }

  input.addEventListener("input", () => {
    selectedIndex = 0;
    render();
  });

  input.addEventListener("keydown", (ev) => {
    if (ev.key === "ArrowDown") {
      ev.preventDefault();
      selectedIndex = Math.min(selectedIndex + 1, Math.max(filtered.length - 1, 0));
      updateSelection();
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      selectedIndex = Math.max(selectedIndex - 1, 0);
      updateSelection();
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      select(selectedIndex);
    } else if (ev.key === "Escape") {
      close();
    }
  });

  dialog.addEventListener("close", () => {
    if (previouslyFocused instanceof HTMLElement && previouslyFocused.isConnected) {
      previouslyFocused.focus();
    }
    previouslyFocused = null;
  });

  function open() {
    previouslyFocused = document.activeElement;
    items = getItems();
    selectedIndex = 0;
    input.value = "";
    if (!dialog.isConnected) document.body.append(dialog);
    render();
    if (typeof dialog.showModal === "function") dialog.showModal();
    input.focus();
  }

  function close() {
    if (dialog.open) dialog.close();
  }

  return { dialog, open, close };
}
