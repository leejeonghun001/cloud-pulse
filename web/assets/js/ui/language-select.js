// language-select.js — the "Auto / English / 한국어" language selector
// shown in Settings → General and on the login page (SPEC-v0.8 §3-3).
// A single shared builder so both call sites can't drift in behavior:
// reading/writing localStorage "cp_lang" and applying the change is
// identical in both places (re-render if the caller can cheaply do so,
// otherwise reload — see onChange below and each caller's usage).
import { selectField } from "./select.js";
import { t, getLangPreference, setLang, htmlLang } from "../core/i18n.js";

/**
 * languageSelector builds the labeled language <select>. Calling
 * setLang persists the choice immediately; the caller decides whether
 * to re-render the current page in place or reload (this module makes
 * no assumption about which is cheaper for a given page).
 * @param {Object} [opts]
 * @param {(pref: import('../core/i18n.js').LangPreference) => void} [opts.onChange]
 * @returns {{node: HTMLElement, select: HTMLSelectElement}}
 */
export function languageSelector({ onChange } = {}) {
  const { node, select } = selectField({
    id: "cp-language-select",
    label: t("common.language"),
    options: [
      { value: "auto", label: t("common.language_auto") },
      { value: "en", label: t("common.language_en") },
      { value: "ko", label: t("common.language_ko") },
    ],
    value: getLangPreference(),
  });

  select.addEventListener("change", () => {
    const pref = /** @type {import('../core/i18n.js').LangPreference} */ (select.value);
    setLang(pref);
    document.documentElement.lang = htmlLang();
    if (onChange) onChange(pref);
  });

  return { node, select };
}
