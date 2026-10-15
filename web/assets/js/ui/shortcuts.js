// shortcuts.js — app-wide keyboard navigation. This stays separate from the
// shell so the event decisions are small, deterministic, and unit-testable.

/**
 * Returns true when keystrokes belong to an editable control rather than the
 * app shell. `closest` also covers editable descendants such as a rich-text
 * editor's nested span.
 * @param {Element|null} target
 * @returns {boolean}
 */
export function isTypingTarget(target) {
  if (!target) return false;
  if (target.matches?.("input, textarea, select")) return true;
  return Boolean(target.isContentEditable || target.closest?.("[contenteditable=''], [contenteditable='true']"));
}

/**
 * Maps the second key in a "g then …" sequence to its route. `m` is Compare
 * rather than the ambiguous first letter of another page.
 */
export const GO_TO_ROUTES = Object.freeze({
  o: "#/",
  a: "#/alerts",
  c: "#/costs",
  r: "#/rollout",
  m: "#/compare",
  s: "#/settings",
});

export const GO_PREFIX_TIMEOUT_MS = 1500;

function isDialogTarget(target) {
  return Boolean(target?.closest?.("dialog"));
}

function hasOpenModalDialog(documentRoot) {
  const dialogs = documentRoot?.querySelectorAll?.("dialog[open]") || [];
  return [...dialogs].some((dialog) => dialog.matches?.(":modal"));
}

/**
 * Applies bare-key shortcut behavior. Ctrl/Cmd+K remains a conventional
 * exception: browsers reserve it and users expect it to open the palette.
 * Other shortcuts never run while the user is editing or holding a modifier.
 * @param {KeyboardEvent} event
 * @param {Element|null} activeElement
 * @returns {"palette"|"shortcuts"|"go-prefix"|string|null}
 */
export function shortcutAction(event, activeElement, documentRoot = globalThis.document) {
  const key = event.key.toLowerCase();
  if (event.defaultPrevented || event.isComposing || isDialogTarget(event.target || activeElement) || hasOpenModalDialog(documentRoot)) return null;
  // Physical K (layout/IME independent) or the key labelled K (Dvorak etc.).
  if ((event.code === "KeyK" || key === "k") && (event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey) return "palette";
  if (isTypingTarget(activeElement) || event.ctrlKey || event.metaKey || event.altKey) return null;
  if (event.key === "/") return "palette";
  if (event.key === "?") return "shortcuts";
  if (event.shiftKey) return null;
  if (event.code === "KeyG") return "go-prefix";
  const routeKey = {
    KeyO: "o",
    KeyA: "a",
    KeyC: "c",
    KeyR: "r",
    KeyM: "m",
    KeyS: "s",
  }[event.code];
  return routeKey ? GO_TO_ROUTES[routeKey] : null;
}

/**
 * Registers navigation, palette, and help shortcuts. A sequence is consumed
 * only after a bare `g`, and naturally expires on the next unrelated key.
 * @param {{openPalette: () => void, openShortcuts: () => void, navigate: (route: string) => void}} handlers
 * @returns {() => void}
 */
export function registerShellShortcuts({ openPalette, openShortcuts, navigate }) {
  let awaitingGoTarget = false;
  let goPrefixTimer = null;
  const clearGoPrefix = () => {
    awaitingGoTarget = false;
    if (goPrefixTimer !== null) {
      clearTimeout(goPrefixTimer);
      goPrefixTimer = null;
    }
  };
  const onKeydown = (event) => {
    const action = shortcutAction(event, document.activeElement);
    if (action === "palette") {
      clearGoPrefix();
      event.preventDefault();
      openPalette();
      return;
    }
    if (!action) {
      clearGoPrefix();
      return;
    }
    if (action === "shortcuts") {
      clearGoPrefix();
      event.preventDefault();
      openShortcuts();
      return;
    }
    if (action === "go-prefix") {
      clearGoPrefix();
      awaitingGoTarget = true;
      goPrefixTimer = setTimeout(clearGoPrefix, GO_PREFIX_TIMEOUT_MS);
      event.preventDefault();
      return;
    }
    if (awaitingGoTarget) {
      clearGoPrefix();
      event.preventDefault();
      navigate(action);
    }
  };
  document.addEventListener("keydown", onKeydown);
  return () => {
    clearGoPrefix();
    document.removeEventListener("keydown", onKeydown);
  };
}
