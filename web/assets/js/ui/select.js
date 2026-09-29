// select.js — thin wrapper for a native <select> styled to match the
// design system, with a small chevron. Kept native (not a custom
// listbox) for full keyboard/screen-reader behavior for free.
import { el } from "./components.js";
import { icon } from "./icons.js";

/**
 * selectField builds a labeled native select.
 * @param {Object} opts
 * @param {string} opts.id
 * @param {string} [opts.label] visually-hidden if omitted but still
 *   required for a11y — pass ariaLabel instead to skip a visible label.
 * @param {string} [opts.ariaLabel]
 * @param {{value: string, label: string}[]} opts.options
 * @param {string} [opts.value] initially selected value
 * @returns {{node: HTMLElement, select: HTMLSelectElement}}
 */
export function selectField({ id, label, ariaLabel, options, value }) {
  const wrap = el("div", { class: "cp-field" });
  if (label) {
    wrap.append(el("label", { class: "cp-label", attrs: { for: id }, text: label }));
  }

  const group = el("div", { class: "cp-input-group" });
  const select = /** @type {HTMLSelectElement} */ (
    el("select", {
      class: "cp-select",
      attrs: { id, name: id, ...(ariaLabel ? { "aria-label": ariaLabel } : {}) },
    })
  );
  for (const opt of options) {
    const optionEl = el("option", { text: opt.label, attrs: { value: opt.value } });
    select.append(optionEl);
  }
  if (value !== undefined) select.value = value;

  const chevron = el("span", { class: "cp-input-group-btn", attrs: { "aria-hidden": "true", style: "pointer-events:none" } });
  chevron.append(icon("chevronDown"));

  group.append(select, chevron);
  wrap.append(group);
  return { node: wrap, select };
}
