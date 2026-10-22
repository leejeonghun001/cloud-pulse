// disclosure.js — a collapsible section ("> Title") built on the native
// <details>/<summary> pair, so keyboard toggling and the screen-reader
// expanded state come for free. Used to keep long guidance and
// per-provider detail out of the way until asked for.
import { el } from "./components.js";
import { icon } from "./icons.js";
import { getItem, setItem } from "../core/store.js";

const STORAGE_PREFIX = "cp_disclosure_";

/**
 * disclosure builds a collapsible section. With an id, the user's
 * open/closed choice is remembered (localStorage), so a page that
 * re-renders on its refresh timer keeps what the user opened.
 * @param {Object} opts
 * @param {string|Node} opts.summary the always-visible line
 * @param {Node[]} [opts.children] the collapsed content
 * @param {boolean} [opts.open] initial state when nothing is remembered
 * @param {string} [opts.id] stable id to remember the state under
 * @param {string} [opts.className] extra class on the <details>
 * @returns {{node: HTMLDetailsElement, body: HTMLElement, setSummary: (text: string) => void, setOpen: (open: boolean) => void}}
 */
export function disclosure({ summary, children = [], open = false, id, className = "" }) {
  const node = /** @type {HTMLDetailsElement} */ (el("details", { class: `cp-disclosure ${className}`.trim() }));
  const remembered = id ? getItem(STORAGE_PREFIX + id) : undefined;
  if (remembered === "1" || (remembered === undefined && open)) node.setAttribute("open", "");

  const summaryEl = el("summary", { class: "cp-disclosure-summary" });
  const chevron = el("span", { class: "cp-disclosure-chevron", attrs: { "aria-hidden": "true" } });
  chevron.append(icon("chevronRight"));
  const label = el("span", { class: "cp-disclosure-label" });
  if (typeof summary === "string") label.textContent = summary;
  else if (summary) label.append(summary);
  summaryEl.append(chevron, label);

  const body = el("div", { class: "cp-disclosure-body", children });
  node.append(summaryEl, body);
  if (id) node.addEventListener("toggle", () => setItem(STORAGE_PREFIX + id, node.open ? "1" : "0"));

  return {
    node,
    body,
    setSummary(text) { label.textContent = text; },
    setOpen(value) {
      if (value) node.setAttribute("open", "");
      else node.removeAttribute("open");
    },
  };
}
