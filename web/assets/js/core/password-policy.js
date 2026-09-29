// password-policy.js — pure password-checklist/strength helpers shared
// by the login page's forced change-password modal and (in a future
// stage) the settings Security section's change-password form. No DOM
// access here so it's covered by node --test without a browser.
// Mirrors the hub's policy (SPEC-v0.4 §1): 8..256 chars (bytes <= 1024),
// not all whitespace, != "changeme", != current password.

/** MIN_LENGTH / MAX_LENGTH mirror the hub's byte-length policy bounds
 * (the hub checks bytes, not chars; ASCII passwords make these
 * equivalent, which covers the vast majority of real input — a
 * multi-byte password could pass the client-side char check while
 * still being rejected server-side by the byte check, but the client
 * check is a UX nicety, not the security boundary). */
export const MIN_LENGTH = 8;
export const MAX_LENGTH = 256;

/**
 * @typedef {Object} PasswordRequirement
 * @property {string} id
 * @property {string} label
 * @property {boolean} met
 */

/**
 * evaluateRequirements checks a candidate new password against every
 * policy rule, given the current password (for the "!= current" rule)
 * and returns each rule's met/unmet state for rendering a checklist.
 * @param {string} password
 * @param {string} [currentPassword]
 * @returns {PasswordRequirement[]}
 */
export function evaluateRequirements(password, currentPassword = "") {
  const p = String(password ?? "");
  const trimmedNonEmpty = p.trim().length > 0;
  return [
    { id: "length", label: `At least ${MIN_LENGTH} characters`, met: p.length >= MIN_LENGTH && p.length <= MAX_LENGTH },
    { id: "not-blank", label: "Not just whitespace", met: trimmedNonEmpty },
    { id: "not-default", label: 'Not the default password ("changeme")', met: p !== "changeme" },
    {
      id: "not-current",
      label: "Different from your current password",
      met: !currentPassword || p !== currentPassword,
    },
  ];
}

/**
 * isPasswordValid reports whether every requirement is met.
 * @param {string} password
 * @param {string} [currentPassword]
 * @returns {boolean}
 */
export function isPasswordValid(password, currentPassword = "") {
  return evaluateRequirements(password, currentPassword).every((r) => r.met);
}

/**
 * @typedef {"empty"|"weak"|"fair"|"good"|"strong"} StrengthLevel
 */

/**
 * scoreStrength computes a coarse 0-4 strength score from length and
 * character-class variety (lowercase/uppercase/digit/symbol), purely
 * for a visual meter — not a substitute for the policy checklist above,
 * which is what's actually enforced.
 * @param {string} password
 * @returns {number} 0..4
 */
export function scoreStrength(password) {
  const p = String(password ?? "");
  if (p.length === 0) return 0;

  let classes = 0;
  if (/[a-z]/.test(p)) classes++;
  if (/[A-Z]/.test(p)) classes++;
  if (/[0-9]/.test(p)) classes++;
  if (/[^a-zA-Z0-9]/.test(p)) classes++;

  let lengthScore = 0;
  if (p.length >= 8) lengthScore = 1;
  if (p.length >= 12) lengthScore = 2;
  if (p.length >= 16) lengthScore = 3;

  const combined = Math.round((lengthScore + classes) / 2);
  return Math.max(0, Math.min(4, combined));
}

/**
 * strengthLevel maps a 0-4 score to a discrete level name used for the
 * meter's color/label.
 * @param {number} score
 * @returns {StrengthLevel}
 */
export function strengthLevel(score) {
  if (score <= 0) return "empty";
  if (score === 1) return "weak";
  if (score === 2) return "fair";
  if (score === 3) return "good";
  return "strong";
}

/**
 * strengthLabel returns the human label for a strength level.
 * @param {StrengthLevel} level
 * @returns {string}
 */
export function strengthLabel(level) {
  switch (level) {
    case "weak":
      return "Weak";
    case "fair":
      return "Fair";
    case "good":
      return "Good";
    case "strong":
      return "Strong";
    default:
      return "";
  }
}
