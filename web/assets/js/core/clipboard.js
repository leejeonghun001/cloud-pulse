// clipboard.js — small shared clipboard helper used by route-independent
// shell actions as well as Settings. Keeping it outside the Settings route
// lets the settings page remain a lazy chunk.

/**
 * Copy text with a selection fallback for denied/unavailable Clipboard API.
 * @param {string} text
 * @param {HTMLInputElement|HTMLTextAreaElement} [selectEl]
 * @returns {Promise<"copied"|"select-fallback">}
 */
export async function copyToClipboard(text, selectEl) {
  try {
    if (globalThis.navigator?.clipboard?.writeText) {
      await globalThis.navigator.clipboard.writeText(text);
      return "copied";
    }
  } catch {
    // Fall through to the selection fallback (permission denied, etc.).
  }
  if (selectEl) {
    selectEl.removeAttribute("hidden");
    selectEl.focus();
    selectEl.select();
  }
  return "select-fallback";
}
