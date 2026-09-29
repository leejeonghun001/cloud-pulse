// toast.js — bottom-right toast notifications (aria-live polite,
// auto-dismiss, optional action button). A single toast region is
// created lazily on first use and reused for the page's lifetime.
import { icon } from "./icons.js";

/** @type {HTMLElement|null} */
let regionEl = null;

function ensureRegion() {
  if (regionEl && regionEl.isConnected) return regionEl;
  regionEl = document.createElement("div");
  regionEl.className = "cp-toast-region";
  regionEl.setAttribute("role", "status");
  regionEl.setAttribute("aria-live", "polite");
  document.body.append(regionEl);
  return regionEl;
}

/**
 * showToast renders a toast in the bottom-right region.
 * @param {Object} opts
 * @param {string} opts.message
 * @param {"info"|"success"|"error"} [opts.variant]
 * @param {number} [opts.duration] ms before auto-dismiss; 0 disables
 *   auto-dismiss (the toast then only closes via its close button or
 *   action).
 * @param {{label: string, onClick: () => void}} [opts.action]
 * @returns {() => void} dismiss function
 */
export function showToast({ message, variant = "info", duration = 5000, action }) {
  const region = ensureRegion();

  const toast = document.createElement("div");
  toast.className = `cp-toast${variant === "error" ? " cp-toast-error" : variant === "success" ? " cp-toast-success" : ""}`;

  const body = document.createElement("div");
  body.className = "cp-toast-body";
  const text = document.createElement("p");
  text.textContent = message;
  body.append(text);

  if (action) {
    const actionBtn = document.createElement("button");
    actionBtn.type = "button";
    actionBtn.className = "cp-toast-action";
    actionBtn.textContent = action.label;
    actionBtn.addEventListener("click", () => {
      action.onClick();
      dismiss();
    });
    body.append(actionBtn);
  }
  toast.append(body);

  const closeBtn = document.createElement("button");
  closeBtn.type = "button";
  closeBtn.className = "cp-toast-close";
  closeBtn.setAttribute("aria-label", "Dismiss notification");
  closeBtn.append(icon("x"));
  closeBtn.addEventListener("click", () => dismiss());
  toast.append(closeBtn);

  region.append(toast);

  let timer = null;
  function dismiss() {
    if (timer) clearTimeout(timer);
    toast.remove();
  }
  if (duration > 0) {
    timer = setTimeout(dismiss, duration);
  }
  return dismiss;
}

/** clearAllToasts removes every currently shown toast (used in tests /
 * page teardown). */
export function clearAllToasts() {
  if (regionEl) {
    while (regionEl.firstChild) regionEl.removeChild(regionEl.firstChild);
  }
}
