// shortcuts.js — global keyboard shortcut dispatch (Ctrl/Cmd+K opens
// the command palette; "/" also opens it when focus isn't inside a
// text input/textarea/select/contenteditable). Kept separate from the
// palette module itself so other shortcuts (future) can register here
// without palette.js needing to own global keydown wiring.

/**
 * isTypingTarget reports whether el is a form control or editable
 * region where a bare "/" keystroke should be treated as normal text
 * input rather than a shortcut.
 * @param {Element|null} el
 * @returns {boolean}
 */
export function isTypingTarget(el) {
  if (!el) return false;
  const tag = el.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  if (/** @type {HTMLElement} */ (el).isContentEditable) return true;
  return false;
}

/**
 * isOpenPaletteShortcut reports whether a keydown event should open the
 * command palette: Ctrl+K / Cmd+K always, or a bare "/" when focus
 * isn't inside a typing target.
 * @param {KeyboardEvent} ev
 * @param {Element|null} activeElement
 * @returns {boolean}
 */
export function isOpenPaletteShortcut(ev, activeElement) {
  if (ev.key.toLowerCase() === "k" && (ev.ctrlKey || ev.metaKey) && !ev.shiftKey && !ev.altKey) {
    return true;
  }
  if (ev.key === "/" && !ev.ctrlKey && !ev.metaKey && !ev.altKey && !isTypingTarget(activeElement)) {
    return true;
  }
  return false;
}

/**
 * registerGlobalShortcuts wires a single document-level keydown
 * listener that calls onOpenPalette() when the open-palette shortcut is
 * pressed (preventing the browser default, e.g. Ctrl+K's "search
 * toolbar" in some browsers). Returns an unsubscribe function.
 * @param {() => void} onOpenPalette
 * @returns {() => void}
 */
export function registerGlobalShortcuts(onOpenPalette) {
  /** @param {KeyboardEvent} ev */
  const handler = (ev) => {
    if (isOpenPaletteShortcut(ev, document.activeElement)) {
      ev.preventDefault();
      onOpenPalette();
    }
  };
  document.addEventListener("keydown", handler);
  return () => document.removeEventListener("keydown", handler);
}
