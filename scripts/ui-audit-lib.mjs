// ui-audit-lib.mjs — pure logic for scripts/ui-audit.mjs: color parsing
// (including oklch()/color-mix() fallback handling via getComputedStyle's
// resolved rgb()/rgba() output), WCAG contrast-ratio math, interactive
// target-size rules, and report aggregation against an allow-list.
//
// Deliberately zero DOM/browser dependencies so this can be covered by
// `node --test` directly (see web/test/ui-audit-*.test.mjs) — the browser
// automation in scripts/ui-audit.mjs only ever calls into here with plain
// strings/numbers already extracted from getComputedStyle.
//
// No third-party dependencies (repo-wide convention: no new Go modules;
// the same "no new deps" spirit applies to Node tooling here — only
// built-ins are used).

/**
 * @typedef {{r:number,g:number,b:number,a:number}} RGBA
 */

/**
 * parseColor parses a CSS color string as returned by a browser's
 * getComputedStyle (always resolved to rgb()/rgba() for solid colors in
 * every modern engine, including Firefox, even when the authored CSS used
 * oklch()/color-mix()/lab()/etc. — the browser has already done the
 * conversion by the time JS reads the computed value). Also accepts hex
 * and the "transparent" keyword for robustness against manual test input.
 * @param {string} input
 * @returns {RGBA|null} null if unparseable.
 */
export function parseColor(input) {
  if (typeof input !== "string") return null;
  const s = input.trim().toLowerCase();
  if (s === "") return null;
  if (s === "transparent") return { r: 0, g: 0, b: 0, a: 0 };
  if (s === "currentcolor") return null;

  let m = s.match(/^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+%?)\s*)?\)$/);
  if (m) {
    return {
      r: clamp255(Number(m[1])),
      g: clamp255(Number(m[2])),
      b: clamp255(Number(m[3])),
      a: m[4] === undefined ? 1 : parseAlpha(m[4]),
    };
  }
  // Modern space-separated syntax: rgb(r g b / a)
  m = s.match(/^rgba?\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*(?:\/\s*([\d.]+%?)\s*)?\)$/);
  if (m) {
    return {
      r: clamp255(Number(m[1])),
      g: clamp255(Number(m[2])),
      b: clamp255(Number(m[3])),
      a: m[4] === undefined ? 1 : parseAlpha(m[4]),
    };
  }
  m = s.match(/^#([0-9a-f]{3,8})$/);
  if (m) return parseHex(m[1]);

  // oklch()/color-mix()/lab()/etc: a real browser's getComputedStyle never
  // returns these verbatim for a resolved style (it always serializes to
  // rgb()/rgba()), so reaching this branch means the caller passed an
  // authored value directly (e.g. a unit test, or a stylesheet source
  // string) rather than a computed one. Treat as unparseable rather than
  // guessing — callers should skip the check rather than silently produce
  // a wrong contrast ratio.
  return null;
}

function parseAlpha(raw) {
  if (raw.endsWith("%")) return clamp01(Number(raw.slice(0, -1)) / 100);
  return clamp01(Number(raw));
}

function parseHex(hex) {
  let h = hex;
  if (h.length === 3 || h.length === 4) {
    h = h.split("").map((c) => c + c).join("");
  }
  if (h.length !== 6 && h.length !== 8) return null;
  const r = parseInt(h.slice(0, 2), 16);
  const g = parseInt(h.slice(2, 4), 16);
  const b = parseInt(h.slice(4, 6), 16);
  const a = h.length === 8 ? parseInt(h.slice(6, 8), 16) / 255 : 1;
  if ([r, g, b].some((v) => Number.isNaN(v))) return null;
  return { r, g, b, a: clamp01(a) };
}

function clamp255(v) {
  return Math.min(255, Math.max(0, v));
}
function clamp01(v) {
  return Math.min(1, Math.max(0, v));
}

/**
 * compositeOver alpha-composites fg (source) over bg (destination),
 * both RGBA, "source-over" — the same operation a browser performs when
 * painting a semi-transparent element over its ancestors' backgrounds.
 * @param {RGBA} fg
 * @param {RGBA} bg
 * @returns {RGBA} always fully opaque (a === 1) when bg is opaque.
 */
export function compositeOver(fg, bg) {
  const a = fg.a + bg.a * (1 - fg.a);
  if (a <= 0) return { r: 0, g: 0, b: 0, a: 0 };
  const mix = (fc, bc) => (fc * fg.a + bc * bg.a * (1 - fg.a)) / a;
  return { r: mix(fg.r, bg.r), g: mix(fg.g, bg.g), b: mix(fg.b, bg.b), a };
}

/**
 * resolveCompositedBackground walks an ancestor chain of background colors
 * (nearest-first, e.g. [element, parent, grandparent, ..., <html>]) and
 * returns the final opaque RGB the browser would actually paint, by
 * compositing from the outermost opaque layer inward. Layers that fail to
 * parse (returns null from parseColor, e.g. currentColor or an
 * unparseable oklch()) are skipped rather than aborting the whole walk.
 *
 * If no layer in the chain is ever opaque (a===1) — e.g. every ancestor up
 * to <html> is transparent, which in practice means the true background
 * is a body image/gradient the DOM's background-color chain can't
 * represent — returns null so the caller can skip the contrast check
 * rather than silently compositing onto a made-up base color.
 * @param {Array<string|null|undefined>} backgroundsNearestFirst
 * @returns {RGBA|null}
 */
export function resolveCompositedBackground(backgroundsNearestFirst) {
  const layers = [];
  for (const raw of backgroundsNearestFirst) {
    const c = parseColor(raw ?? "");
    if (c) layers.push(c);
  }
  if (layers.length === 0) return null;
  // Composite from outermost (last pushed / furthest ancestor) to
  // innermost so nearer layers paint over farther ones, matching how a
  // browser actually renders the stack.
  let acc = null;
  for (let i = layers.length - 1; i >= 0; i--) {
    acc = acc === null ? layers[i] : compositeOver(layers[i], acc);
  }
  if (!acc || acc.a < 0.999) return null; // never reached full opacity: unknown true background
  return acc;
}

/**
 * relativeLuminance computes the WCAG relative luminance of an sRGB
 * color (0-255 channels), per WCAG 2.x's definition.
 * @param {{r:number,g:number,b:number}} rgb
 * @returns {number} 0 (black) .. 1 (white)
 */
export function relativeLuminance({ r, g, b }) {
  const chan = (c) => {
    const v = c / 255;
    return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  };
  const R = chan(r);
  const G = chan(g);
  const B = chan(b);
  return 0.2126 * R + 0.7152 * G + 0.0722 * B;
}

/**
 * contrastRatio computes the WCAG contrast ratio between two opaque sRGB
 * colors: (L1 + 0.05) / (L2 + 0.05) with L1 the lighter of the two.
 * @param {{r:number,g:number,b:number}} a
 * @param {{r:number,g:number,b:number}} b
 * @returns {number} 1 (no contrast) .. 21 (black on white)
 */
export function contrastRatio(a, b) {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  const lighter = Math.max(la, lb);
  const darker = Math.min(la, lb);
  return (lighter + 0.05) / (darker + 0.05);
}

/**
 * requiredContrastRatio returns the WCAG 2.x AA minimum contrast ratio
 * for a piece of text, per the "large text" exception (>=18pt, or
 * >=14pt/18.66px and bold) and the separate "non-text UI component"
 * 3:1 rule (borders/icons that convey information, not this function's
 * default case).
 * @param {{fontSizePx:number, bold:boolean, isUIComponent?: boolean}} opts
 * @returns {number} 3 or 4.5
 */
export function requiredContrastRatio({ fontSizePx, bold, isUIComponent }) {
  if (isUIComponent) return 3;
  const isLarge = fontSizePx >= 24 || (bold && fontSizePx >= 18.66);
  return isLarge ? 3 : 4.5;
}

/**
 * evaluateContrast is the single entry point scripts/ui-audit.mjs calls
 * per visible text node: given the resolved foreground color, the
 * ancestor background chain, and font metrics, returns a verdict.
 * @param {{
 *   foreground: string,
 *   backgroundsNearestFirst: string[],
 *   fontSizePx: number,
 *   bold: boolean,
 * }} input
 * @returns {{skipped:true,reason:string}|{skipped:false,ratio:number,required:number,pass:boolean}}
 */
export function evaluateContrast({ foreground, backgroundsNearestFirst, fontSizePx, bold }) {
  const fg = parseColor(foreground);
  if (!fg || fg.a === 0) return { skipped: true, reason: "foreground unparseable or fully transparent" };
  const bg = resolveCompositedBackground(backgroundsNearestFirst);
  if (!bg) return { skipped: true, reason: "no opaque ancestor background found (transparent-over-image or unresolved color)" };
  const composedFg = fg.a < 1 ? compositeOver(fg, bg) : fg;
  const ratio = contrastRatio(composedFg, bg);
  const required = requiredContrastRatio({ fontSizePx, bold });
  return { skipped: false, ratio: round2(ratio), required, pass: ratio + 1e-9 >= required };
}

function round2(n) {
  return Math.round(n * 100) / 100;
}

/**
 * evaluateTargetSize applies WCAG 2.2 AA 2.5.8's minimum 24x24 CSS-pixel
 * target-size rule, honoring the "inline exception" (a target that is
 * part of a sentence/inline text flow, e.g. an inline link inside a
 * paragraph, is exempt regardless of its box size).
 * @param {{widthPx:number, heightPx:number, isInline:boolean}} input
 * @returns {{pass:true}|{pass:false,widthPx:number,heightPx:number}}
 */
export function evaluateTargetSize({ widthPx, heightPx, isInline }) {
  if (isInline) return { pass: true };
  const MIN = 24;
  if (widthPx >= MIN && heightPx >= MIN) return { pass: true };
  return { pass: false, widthPx: round2(widthPx), heightPx: round2(heightPx) };
}

/**
 * violationKey builds the stable, allow-list-matchable key for one
 * violation: `<checkId>:<routeKey>` where routeKey already encodes
 * viewport/theme (the caller constructs it, see buildRouteKey below) —
 * kept deliberately coarse (not per-DOM-node) since a single CSS rule fix
 * typically resolves many instances at once inside one route/viewport/
 * theme combination, and an allow-list author reasons about the
 * page/breakpoint/theme, not individual pixel coordinates.
 * @param {string} checkId e.g. "contrast", "overflow", "target-size", "js-error", "csp", "focus"
 * @param {string} routeKey e.g. "#/settings/billing@360x800@dark"
 * @returns {string}
 */
export function violationKey(checkId, routeKey) {
  return `${checkId}:${routeKey}`;
}

/**
 * buildRouteKey builds the canonical route/viewport/theme identity string
 * used both as a report grouping key and as an allow-list match key.
 * @param {{route:string, viewport:string, theme:string}} input
 * @returns {string}
 */
export function buildRouteKey({ route, viewport, theme }) {
  return `${route}@${viewport}@${theme}`;
}

/**
 * parseViewportSpec parses one "WxH" token (e.g. "390x844") from the
 * --viewports CLI flag.
 * @param {string} spec
 * @returns {{width:number,height:number,label:string}}
 * @throws {Error} on a malformed spec.
 */
export function parseViewportSpec(spec) {
  const m = /^(\d+)x(\d+)$/.exec(spec.trim());
  if (!m) throw new Error(`invalid viewport spec "${spec}" (expected WxH, e.g. 390x844)`);
  const width = Number(m[1]);
  const height = Number(m[2]);
  if (width <= 0 || height <= 0) throw new Error(`invalid viewport spec "${spec}": width/height must be positive`);
  return { width, height, label: `${width}x${height}` };
}

/**
 * isAllowed checks whether a violation key is present (and has a
 * non-empty reason) in a parsed allow-list. The allow file format is a
 * JSON array of {key, reason} objects — see scripts/ui-audit.allow.json's
 * own header comment (as a top-level "_comment" field, since JSON has no
 * real comments) for the authoring convention.
 * @param {Array<{key:string,reason:string}>} allowList
 * @param {string} key
 * @returns {boolean}
 */
export function isAllowed(allowList, key) {
  return allowList.some((e) => e && e.key === key && typeof e.reason === "string" && e.reason.trim().length > 0);
}

/**
 * parseAllowList validates and normalizes the raw parsed JSON of an
 * allow-list file into an array of {key, reason} entries, throwing a
 * descriptive error for any entry missing a reason — this is what
 * enforces SPEC-v0.7 §5's "every allow entry needs a reason" rule at the
 * point the file is loaded, not just when it's consulted.
 * @param {unknown} raw parsed JSON value
 * @returns {Array<{key:string,reason:string}>}
 */
export function parseAllowList(raw) {
  if (raw === undefined || raw === null) return [];
  const arr = Array.isArray(raw) ? raw : Array.isArray(raw.entries) ? raw.entries : null;
  if (!arr) throw new Error("allow-list must be a JSON array, or an object with an \"entries\" array");
  const out = [];
  for (const [i, entry] of arr.entries()) {
    if (!entry || typeof entry !== "object") throw new Error(`allow-list entry #${i} is not an object`);
    if (typeof entry.key !== "string" || entry.key.length === 0) {
      throw new Error(`allow-list entry #${i} is missing a non-empty "key"`);
    }
    if (typeof entry.reason !== "string" || entry.reason.trim().length === 0) {
      throw new Error(`allow-list entry #${i} (key "${entry.key}") is missing a non-empty "reason"`);
    }
    out.push({ key: entry.key, reason: entry.reason });
  }
  return out;
}

/**
 * aggregateReport folds a flat list of raw findings (each already tagged
 * with a violationKey) against an allow-list into the final report shape
 * written as report.json: every finding, split into `violations`
 * (not allowed — these cause a non-zero exit) and `allowed` (matched an
 * allow entry, informational only), plus a `summary` count block.
 * @param {Array<{key:string,checkId:string,routeKey:string,detail:unknown}>} findings
 * @param {Array<{key:string,reason:string}>} allowList
 * @returns {{
 *   summary: {total:number, violations:number, allowed:number, byCheck: Record<string, number>},
 *   violations: Array<{key:string,checkId:string,routeKey:string,detail:unknown}>,
 *   allowed: Array<{key:string,checkId:string,routeKey:string,detail:unknown,reason:string}>,
 * }}
 */
export function aggregateReport(findings, allowList) {
  const violations = [];
  const allowed = [];
  const byCheck = {};
  for (const f of findings) {
    byCheck[f.checkId] = (byCheck[f.checkId] || 0) + 1;
    const entry = allowList.find((e) => e.key === f.key);
    if (entry) {
      allowed.push({ ...f, reason: entry.reason });
    } else {
      violations.push(f);
    }
  }
  return {
    summary: {
      total: findings.length,
      violations: violations.length,
      allowed: allowed.length,
      byCheck,
    },
    violations,
    allowed,
  };
}
