// password-field.js — reusable password input with show/hide toggle,
// plus the requirement checklist + strength meter UI, built from the
// pure helpers in core/password-policy.js. Used by the login page's
// forced change-password modal and (by a later stage) the settings
// Security section.
import { el } from "./components.js";
import { icon } from "./icons.js";
import { disclosure } from "./disclosure.js";
import { evaluateRequirements, scoreStrength, strengthLevel, strengthLabel } from "../core/password-policy.js";
import { t } from "../core/i18n.js";

/**
 * passwordField builds a labeled password input with a show/hide
 * toggle button.
 * @param {Object} opts
 * @param {string} opts.id
 * @param {string} opts.label
 * @param {string} [opts.autocomplete]
 * @param {boolean} [opts.required]
 * @returns {{node: HTMLElement, input: HTMLInputElement, toggleBtn: HTMLButtonElement}}
 */
export function passwordField({ id, label, autocomplete = "current-password", required = true }) {
  const field = el("div", { class: "cp-field" });
  field.append(el("label", { class: "cp-label", attrs: { for: id }, text: label }));

  const group = el("div", { class: "cp-input-group" });
  const input = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input",
      attrs: {
        id,
        name: id,
        type: "password",
        autocomplete,
        ...(required ? { required: "" } : {}),
      },
    })
  );
  const toggleBtn = /** @type {HTMLButtonElement} */ (
    el("button", {
      class: "cp-input-group-btn",
      attrs: { type: "button", "aria-label": t("password.show_password"), "aria-pressed": "false" },
    })
  );
  toggleBtn.append(icon("eye"));

  toggleBtn.addEventListener("click", () => {
    const showing = input.type === "text";
    input.type = showing ? "password" : "text";
    toggleBtn.setAttribute("aria-pressed", showing ? "false" : "true");
    toggleBtn.setAttribute("aria-label", showing ? t("password.show_password") : t("password.hide_password"));
    clearIcon(toggleBtn);
    toggleBtn.append(icon(showing ? "eye" : "eyeOff"));
  });

  group.append(input, toggleBtn);
  field.append(group);
  return { node: field, input, toggleBtn };
}

function clearIcon(btn) {
  while (btn.firstChild) btn.removeChild(btn.firstChild);
}

/**
 * passwordChecklist builds the live requirement checklist, collapsed
 * behind a one-line summary ("Password rules · 2 of 4 met") so the form
 * stays short; the list itself is one click away. Call
 * update(password, currentPassword) whenever the password input
 * changes, and reveal() to open it (e.g. after a rejected submit).
 * @returns {{node: HTMLElement, update: (password: string, currentPassword?: string) => boolean, reveal: () => void}}
 */
export function passwordChecklist() {
  const list = el("ul", { class: "cp-pw-checklist", attrs: { role: "list" } });
  const box = disclosure({ summary: "", children: [list], className: "cp-pw-checklist-disclosure" });

  /** @type {Map<string, HTMLElement>} */
  const itemEls = new Map();

  function update(password, currentPassword = "") {
    const reqs = evaluateRequirements(password, currentPassword);
    for (const req of reqs) {
      let item = itemEls.get(req.id);
      if (!item) {
        item = el("li", { class: "cp-pw-checklist-item" });
        const iconHost = el("span", { class: "cp-pw-checklist-icon" });
        item.append(iconHost, el("span", { text: req.label }));
        itemEls.set(req.id, item);
        list.append(item);
      }
      item.dataset.met = String(req.met);
      const iconHost = item.firstChild;
      while (iconHost.firstChild) iconHost.removeChild(iconHost.firstChild);
      iconHost.append(icon(req.met ? "circleCheck" : "circleX"));
    }
    const met = reqs.filter((r) => r.met).length;
    box.setSummary(met === reqs.length ? t("password.requirements_all_met") : t("password.requirements_summary", { met, total: reqs.length }));
    return met === reqs.length;
  }

  return { node: box.node, update, reveal: () => box.setOpen(true) };
}

/**
 * passwordStrengthMeter builds a 4-segment strength meter + label. Call
 * update(password) whenever the password input changes.
 * @returns {{node: HTMLElement, update: (password: string) => void}}
 */
export function passwordStrengthMeter() {
  const wrap = el("div", { class: "cp-pw-strength" });
  const track = el("div", { class: "cp-pw-strength-track", attrs: { "aria-hidden": "true" } });
  const segs = [0, 1, 2, 3].map(() => el("div", { class: "cp-pw-strength-seg" }));
  segs.forEach((s) => track.append(s));
  const label = el("span", { class: "cp-pw-strength-label", attrs: { role: "status", "aria-live": "polite" } });
  wrap.append(track, label);

  function update(password) {
    const score = scoreStrength(password);
    const level = strengthLevel(score);
    segs.forEach((seg, i) => {
      const active = i < score;
      seg.dataset.active = String(active);
      if (active) {
        seg.dataset.level = level;
      } else {
        delete seg.dataset.level;
      }
    });
    label.textContent = password ? strengthLabel(level) : "";
  }

  return { node: wrap, update };
}
