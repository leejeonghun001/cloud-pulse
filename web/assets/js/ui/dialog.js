// dialog.js — thin helpers around native <dialog> for consistent
// open/close behavior: native showModal() makes background content inert;
// our boundary-only Tab handler keeps sequential focus inside the modal
// instead of allowing traversal into browser chrome. Also provides focus
// restore to the previously focused element on close, and Escape
// handling that respects a "non-dismissable" flag (the forced
// change-password modal must not be closable via Escape or backdrop
// click).

/**
 * createDialog builds a <dialog> element with the standard cloud-pulse
 * chrome (title, optional description, body slot, actions slot) and
 * open/close helpers.
 * @param {Object} opts
 * @param {string} opts.titleId id for the dialog's title (aria-labelledby target)
 * @param {string} opts.title
 * @param {string} [opts.description]
 * @param {boolean} [opts.wide] use the wider max-width variant
 * @param {boolean} [opts.nonDismissable] when true, Escape and
 *   backdrop clicks (via the "cancel" event) are prevented — used for
 *   the forced change-password modal.
 * @returns {{dialog: HTMLDialogElement, body: HTMLElement, open: (options?: {restoreFocusTo?: Element|null, shouldRestoreFocus?: () => boolean}) => void, close: (returnValue?: string) => void}}
 */
export function createDialog({ titleId, title, description, wide = false, nonDismissable = false }) {
  const dialog = /** @type {HTMLDialogElement} */ (document.createElement("dialog"));
  dialog.className = `cp-dialog${wide ? " cp-dialog-wide" : ""}`;
  dialog.setAttribute("aria-labelledby", titleId);

  const header = document.createElement("div");
  header.className = "cp-dialog-header";
  const titleEl = document.createElement("h2");
  titleEl.className = "cp-dialog-title";
  titleEl.id = titleId;
  titleEl.textContent = title;
  header.append(titleEl);
  dialog.append(header);

  if (description) {
    const desc = document.createElement("p");
    desc.className = "cp-dialog-desc";
    desc.textContent = description;
    dialog.append(desc);
  }

  const body = document.createElement("div");
  body.className = "cp-dialog-body";
  dialog.append(body);

  /** @type {Element|null} */
  let previouslyFocused = null;
  let shouldRestoreFocus = () => true;

  if (nonDismissable) {
    dialog.addEventListener("cancel", (ev) => ev.preventDefault());
  }

  dialog.addEventListener("keydown", (event) => {
    if (event.defaultPrevented || event.key !== "Tab" || event.ctrlKey || event.altKey || event.metaKey
      || !dialog.matches(":modal") || event.target.closest("dialog") !== dialog
      || document.activeElement?.closest("dialog") !== dialog) return;
    // The active native modal owns this event. A nested dialog's event must
    // never be handled by its parent. Read the current DOM, not an open-time
    // list: reveal, platform tabs and manual-copy fields change tab stops.
    const stops = Array.from(dialog.querySelectorAll("a[href], area[href], button, input, select, textarea, summary, iframe, object, audio[controls], video[controls], [tabindex], [contenteditable]"))
      .filter((node) => (node.tabIndex >= 0 || (node.isContentEditable && !node.hasAttribute("tabindex") && !node.parentElement?.isContentEditable))
        && !node.matches(":disabled") && !node.closest("[hidden], [inert]")
        && node.getClientRects().length > 0 && !["hidden", "collapse"].includes(getComputedStyle(node).visibility))
      .filter((node, _, candidates) => {
        // Only the checked radio (or first enabled radio when none is checked)
        // participates in a named group's native sequential focus order.
        if (!node.matches('input[type="radio"]') || !node.name) return true;
        const group = candidates.filter((other) => other.matches('input[type="radio"]') && other.name === node.name && other.form === node.form);
        return node === (group.find((other) => other.checked) || group[0]);
      })
      .sort((a, b) => (a.tabIndex > 0 ? a.tabIndex : Infinity) - (b.tabIndex > 0 ? b.tabIndex : Infinity));
    if (!stops.length) {
      // <dialog> is natively programmatically focusable. Do not add tabindex
      // (which HTML forbids on dialog) or invent an invisible focus control.
      event.preventDefault();
      dialog.focus({ preventScroll: true });
    } else if (document.activeElement === stops[event.shiftKey ? 0 : stops.length - 1]) {
      event.preventDefault();
      // Long dialogs must scroll the wrapped destination into view.
      stops[event.shiftKey ? stops.length - 1 : 0].focus();
    }
  });

  dialog.addEventListener("close", () => {
    if (shouldRestoreFocus() && previouslyFocused instanceof HTMLElement && previouslyFocused.isConnected
      && !previouslyFocused.matches(":disabled") && !previouslyFocused.closest("[hidden], [inert]")) {
      previouslyFocused.focus({ preventScroll: true });
    }
    previouslyFocused = null;
    shouldRestoreFocus = () => true;
  });

  function open(options = {}) {
    if (dialog.open) return;
    previouslyFocused = options.restoreFocusTo === undefined ? document.activeElement : options.restoreFocusTo;
    shouldRestoreFocus = options.shouldRestoreFocus || (() => true);
    if (!dialog.isConnected) document.body.append(dialog);
    if (typeof dialog.showModal === "function") dialog.showModal();
  }

  function close(returnValue) {
    if (dialog.open) dialog.close(returnValue);
  }

  return { dialog, body, open, close };
}

/**
 * createShortcutsDialog renders the small reference dialog used by the
 * shell's `?` shortcut and command-palette action. It deliberately has no
 * close button: native Escape is the familiar dismissal affordance and keeps
 * the reference compact.
 * @param {import("../core/i18n.js").t} translate
 * @returns {{dialog: HTMLDialogElement, body: HTMLElement, open: (options?: {restoreFocusTo?: Element|null, shouldRestoreFocus?: () => boolean}) => void, close: (returnValue?: string) => void}}
 */
export function createShortcutsDialog(translate) {
  const dialogHandle = createDialog({
    titleId: "cp-shortcuts-title",
    title: translate("shortcuts.title"),
    description: translate("shortcuts.description"),
  });
  dialogHandle.dialog.classList.add("cp-shortcuts-dialog");
  const list = document.createElement("dl");
  list.className = "cp-shortcuts-list";
  const rows = [
    ["shortcuts.palette", "shortcuts.palette_keys"],
    ["shortcuts.go_to", "shortcuts.go_to_keys"],
    ["shortcuts.dismiss", "shortcuts.dismiss_keys"],
  ];
  for (const [labelKey, keysKey] of rows) {
    const row = document.createElement("div");
    row.className = "cp-shortcuts-row";
    const label = document.createElement("dt");
    label.textContent = translate(labelKey);
    const keys = document.createElement("dd");
    keys.className = "cp-kbd";
    keys.textContent = translate(keysKey);
    row.append(label, keys);
    list.append(row);
  }
  dialogHandle.body.append(list);
  return dialogHandle;
}

/**
 * buildDialogActions builds the standard bottom-right action button row.
 * @param {HTMLElement[]} buttons
 * @returns {HTMLElement}
 */
export function buildDialogActions(buttons) {
  const row = document.createElement("div");
  row.className = "cp-dialog-actions";
  row.append(...buttons);
  return row;
}
