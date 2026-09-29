// dropdown.js — small popover menu anchored to a trigger button (user
// menu, columns dropdown). Closes on outside click, Escape, or an item
// click; only one dropdown is open at a time (opening a new one closes
// any other cloud-pulse dropdown already open).
import { el } from "./components.js";

/** @type {(() => void)|null} */
let openCloser = null;

/**
 * createDropdown wires trigger to toggle a menu built by buildItems()
 * (called fresh each time it opens, so item state — e.g. checkbox
 * checked-ness — is always current).
 * @param {Object} opts
 * @param {HTMLElement} opts.trigger
 * @param {() => HTMLElement} opts.buildMenu returns the menu content
 *   element (without the outer .cp-dropdown-menu wrapper — this
 *   function applies that).
 * @param {"left"|"right"} [opts.align]
 * @returns {{open: () => void, close: () => void, isOpen: () => boolean}}
 */
export function createDropdown({ trigger, buildMenu, align = "right" }) {
  const wrap = el("div", { class: "cp-dropdown" });
  trigger.parentElement?.insertBefore(wrap, trigger);
  wrap.append(trigger);

  /** @type {HTMLElement|null} */
  let menu = null;

  function close() {
    if (menu) {
      menu.remove();
      menu = null;
    }
    trigger.setAttribute("aria-expanded", "false");
    document.removeEventListener("mousedown", onOutside, true);
    document.removeEventListener("keydown", onKeydown, true);
    if (openCloser === close) openCloser = null;
  }

  function onOutside(ev) {
    if (menu && !wrap.contains(ev.target)) close();
  }

  function onKeydown(ev) {
    if (ev.key === "Escape") {
      ev.stopPropagation();
      close();
      trigger.focus();
    }
  }

  function open() {
    if (openCloser && openCloser !== close) openCloser();
    if (menu) return;
    menu = el("div", { class: `cp-dropdown-menu${align === "left" ? " cp-dropdown-menu-left" : ""}`, attrs: { role: "menu" } });
    menu.append(buildMenu());
    wrap.append(menu);
    trigger.setAttribute("aria-expanded", "true");
    document.addEventListener("mousedown", onOutside, true);
    document.addEventListener("keydown", onKeydown, true);
    openCloser = close;
  }

  trigger.setAttribute("aria-haspopup", "menu");
  trigger.setAttribute("aria-expanded", "false");
  trigger.addEventListener("click", (ev) => {
    ev.stopPropagation();
    if (menu) close();
    else open();
  });

  return { open, close, isOpen: () => Boolean(menu) };
}

/**
 * dropdownItem builds a single menu item button.
 * @param {Object} opts
 * @param {string} opts.label
 * @param {() => void} opts.onClick
 * @param {import("./icons.js").icon} [opts.icon] pre-built icon element
 * @returns {HTMLButtonElement}
 */
export function dropdownItem({ label, onClick, icon: iconEl }) {
  const btn = /** @type {HTMLButtonElement} */ (
    el("button", { class: "cp-dropdown-item", attrs: { type: "button", role: "menuitem" } })
  );
  if (iconEl) btn.append(iconEl);
  btn.append(el("span", { text: label }));
  btn.addEventListener("click", onClick);
  return btn;
}

/** dropdownSeparator builds a thin divider line between menu item groups. */
export function dropdownSeparator() {
  return el("div", { class: "cp-dropdown-separator", attrs: { role: "separator" } });
}
