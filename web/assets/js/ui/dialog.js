// dialog.js — thin helpers around native <dialog> for consistent
// open/close behavior: focus trap (native <dialog> already traps Tab
// within itself per the HTML spec when shown via showModal()), focus
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
 * @returns {{dialog: HTMLDialogElement, body: HTMLElement, open: () => void, close: (returnValue?: string) => void}}
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

  if (nonDismissable) {
    dialog.addEventListener("cancel", (ev) => ev.preventDefault());
  }

  dialog.addEventListener("close", () => {
    if (previouslyFocused instanceof HTMLElement && previouslyFocused.isConnected) {
      previouslyFocused.focus();
    }
    previouslyFocused = null;
  });

  function open() {
    previouslyFocused = document.activeElement;
    if (!dialog.isConnected) document.body.append(dialog);
    if (typeof dialog.showModal === "function") dialog.showModal();
  }

  function close(returnValue) {
    if (dialog.open) dialog.close(returnValue);
  }

  return { dialog, body, open, close };
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
