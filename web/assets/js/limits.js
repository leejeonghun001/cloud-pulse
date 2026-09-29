// limits.js — pure helpers for the settings page's traffic-limits editor:
// GiB<->bytes parsing/validation and agent-token masking. No DOM access
// here so these are covered by node --test without a browser
// environment.

/** GIB is 1 GiB in bytes (2^30), the unit used by CP_EGRESS_LIMIT_GB and
 * the settings page's limit inputs. */
export const GIB = 1073741824;

/**
 * @typedef {Object} ParsedLimit
 * @property {boolean} ok
 * @property {number|null} [bytes] present when ok is true: null means "no
 *   override" (blank input), 0 means "explicitly unlimited"
 * @property {string} [error] present when ok is false
 */

/**
 * parseLimitGiB parses a settings-page limit input value (a decimal
 * string in GiB) into a byte count suitable for the PUT .../limits body.
 * An empty/whitespace-only string means "no override" (bytes: null,
 * clears any hub override). "0" means "explicitly unlimited" (bytes: 0).
 * Negative, non-finite, or non-numeric input is rejected. Fractional
 * GiB values are allowed and rounded to the nearest byte.
 * @param {string} raw
 * @returns {ParsedLimit}
 */
export function parseLimitGiB(raw) {
  const trimmed = String(raw ?? "").trim();
  if (trimmed === "") return { ok: true, bytes: null };

  const n = Number(trimmed);
  if (!Number.isFinite(n)) return { ok: false, error: "must be a number" };
  if (n < 0) return { ok: false, error: "must not be negative" };

  const bytes = Math.round(n * GIB);
  if (!Number.isSafeInteger(bytes)) return { ok: false, error: "value is too large" };

  return { ok: true, bytes };
}

/**
 * formatLimitGiB renders a byte count (or null/undefined) as a decimal
 * GiB string suitable for pre-filling a limit input, e.g.
 * formatLimitGiB(1610612736) -> "1.5". null/undefined (no override)
 * renders "" (blank input); 0 (explicitly unlimited) renders "0".
 * @param {number|null|undefined} bytes
 * @returns {string}
 */
export function formatLimitGiB(bytes) {
  if (bytes === null || bytes === undefined) return "";
  const n = Number(bytes);
  if (!Number.isFinite(n) || n < 0) return "";
  if (n === 0) return "0";
  const gib = n / GIB;
  // Trim trailing zeros/decimal point from a fixed-precision string
  // without resorting to imprecise repeated division.
  return trimTrailingZeros(gib.toFixed(4));
}

/**
 * trimTrailingZeros strips trailing zeros (and a trailing "." if left
 * bare) from a decimal string produced by toFixed().
 * @param {string} s
 * @returns {string}
 */
function trimTrailingZeros(s) {
  if (!s.includes(".")) return s;
  return s.replace(/0+$/, "").replace(/\.$/, "");
}

/**
 * maskToken renders a token as its first 4 + "…" + last 4 characters,
 * mirroring the hub's AgentTokenHint masking, for client-side display
 * before/without a full reveal. Tokens shorter than 9 characters render
 * as a fully-masked placeholder.
 * @param {string} token
 * @returns {string}
 */
export function maskToken(token) {
  const t = String(token ?? "");
  if (t.length < 9) return "••••";
  return `${t.slice(0, 4)}…${t.slice(-4)}`;
}
