// check-i18n-lib.mjs — pure detection/aggregation logic for
// scripts/check-i18n.mjs's hard-coded-UI-string lint (SPEC-v0.8 §3-3).
// Deliberately regex/line-based rather than a full JS parser (repo-wide
// "no new deps" convention rules out pulling in a real AST library for
// Node tooling), scoped to the small set of patterns actually used
// across web/assets/js/** — see findHardcodedStrings' doc comment for
// the exact list, derived from auditing web/assets/js/ui's helpers
// (el(), showToast, createDialog, dropdownItem, selectField) rather
// than guessed at.
//
// No third-party dependencies, matching ui-audit-lib.mjs's convention.

/**
 * @typedef {{file:string, line:number, column:number, kind:string, snippet:string}} Finding
 */

// STRING_LITERAL matches a single- or double-quoted JS string literal
// (backtick template literals are intentionally excluded — see
// findHardcodedStrings' doc comment on why interpolated templates
// aren't flagged the same way). Handles escaped quotes.
const STRING_LITERAL = /"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/;

// TEMPLATE_LITERAL matches a single-line backtick template literal. It
// is flagged when its static text (outside `${...}`) contains a letter,
// e.g. `Delete ${name}?` — interpolation does not make text
// translation-free.
const TEMPLATE_LITERAL = /`(?:[^`\\]|\\.)*`/;

// TEXT_VALUE is any value form findHardcodedStrings inspects.
const TEXT_VALUE = new RegExp(`${STRING_LITERAL.source}|${TEMPLATE_LITERAL.source}`);

// PROPERTY_PATTERNS: an object-literal property assignment whose value
// is a plain string literal, keyed by the property name that makes it
// user-visible text in this codebase's UI helper conventions (see
// web/assets/js/ui/components.js's el(), toast.js's showToast,
// dialog.js's createDialog, dropdown.js's dropdownItem, select.js's
// selectField). `text:`, `label:`, `title:`, `message:`, `description:`
// as object-literal keys; `textContent =`, `.innerText =` as direct
// assignments; `aria-label:`/`placeholder:`/`alt:` as attrs-object
// keys (both nested under `attrs: {...}` and, for <img>-style raw
// attribute objects some pages build directly, as top-level keys) are
// covered by the same PROPERTY_PATTERNS list since the source text
// alone can't always distinguish "inside attrs" from "top-level" —
// scanning is intentionally conservative (a few false positives that
// get allow-listed are fine; missing a real one is not).
const PROPERTY_KEYS = ["text", "label", "title", "message", "description", "alt", "aria-label", "placeholder", "group"];

// ASSIGNMENT_PATTERNS: a direct DOM-property assignment to a string
// literal.
const ASSIGNMENT_PROPS = ["textContent", "innerText"];

// CALL_PATTERNS: calls whose first string-literal argument is
// user-visible text.
const CALL_NAMES = ["confirm", "alert"];

/**
 * findHardcodedStrings scans one JS source file's text for hard-coded
 * user-visible string literals, per this file's header comment.
 * Skips:
 *  - lines inside a `//` line comment or `/* ... *\/` block comment
 *    (best-effort: block comments are tracked across lines by a
 *    running "inBlockComment" flag, which is exact for well-formed
 *    source — this tool only ever runs against this repo's own JS).
 *  - a string literal that is empty ("" or '') — never user-visible
 *    text worth flagging or translating.
 *  - a string literal that contains no letters (e.g. a bare separator,
 *    a CSS class name made entirely of symbols) — heuristic to avoid
 *    flagging structural strings like `""`/`"-"`, tightened by
 *    requiring at least one Unicode letter.
 * @param {string} source
 * @param {string} filePath used only to populate Finding.file
 * @returns {Finding[]}
 */
export function findHardcodedStrings(source, filePath) {
  const findings = [];
  const lines = source.split("\n");
  let inBlockComment = false;

  const keyAlternatives = `(?:"(${PROPERTY_KEYS.map(escapeForRegex).join("|")})"|'(${PROPERTY_KEYS.map(escapeForRegex).join("|")})'|(${PROPERTY_KEYS.map(escapeForRegex).join("|")}))`;
  const propKeyPattern = new RegExp(`(?:^|[{,(\\s])${keyAlternatives}\\s*:\\s*(${TEXT_VALUE.source})`, "g");
  // `text: cond ? "A" : "B"` — both branches are user-visible.
  const ternaryPattern = new RegExp(
    `(?:^|[{,(\\s])${keyAlternatives}\\s*:\\s*[^,?:{}]+\\?\\s*(${TEXT_VALUE.source}|[^:?]+?)\\s*:\\s*(${TEXT_VALUE.source}|[^,}]+)`,
    "g",
  );
  const assignPattern = new RegExp(`\\.(${ASSIGNMENT_PROPS.join("|")})\\s*=\\s*(${TEXT_VALUE.source})`, "g");
  const callPattern = new RegExp(`\\b(${CALL_NAMES.join("|")})\\s*\\(\\s*(${TEXT_VALUE.source})`, "g");

  for (let i = 0; i < lines.length; i++) {
    let line = lines[i];

    // Strip content already inside a block comment carried over from
    // a previous line, then look for this line's own comment markers.
    if (inBlockComment) {
      const end = line.indexOf("*/");
      if (end === -1) continue;
      line = line.slice(end + 2);
      inBlockComment = false;
    }
    // Remove // line comments (naive: doesn't special-case // inside a
    // string; acceptable for this codebase's style, which doesn't put
    // "//" inside user-visible string literals).
    const lineCommentIdx = line.indexOf("//");
    let blockStart = line.indexOf("/*");
    if (blockStart !== -1 && (lineCommentIdx === -1 || blockStart < lineCommentIdx)) {
      const blockEnd = line.indexOf("*/", blockStart + 2);
      if (blockEnd === -1) {
        line = line.slice(0, blockStart);
        inBlockComment = true;
      } else {
        line = line.slice(0, blockStart) + line.slice(blockEnd + 2);
      }
    } else if (lineCommentIdx !== -1) {
      line = line.slice(0, lineCommentIdx);
    }

    findMatches(propKeyPattern, line, i, filePath, findings, (m) => ({ kind: `key:${m[1] || m[2] || m[3]}`, value: m[4] }));
    findMatches(ternaryPattern, line, i, filePath, findings, (m) => ({ kind: `ternary:${m[1] || m[2] || m[3]}`, value: [m[4], m[5]] }));
    findMatches(assignPattern, line, i, filePath, findings, (m) => ({ kind: `assign:${m[1]}`, value: m[2] }));
    findMatches(callPattern, line, i, filePath, findings, (m) => ({ kind: `call:${m[1]}`, value: m[2] }));
  }

  return findings;
}

function findMatches(pattern, line, lineIndex, filePath, out, describe) {
  pattern.lastIndex = 0;
  let m;
  while ((m = pattern.exec(line)) !== null) {
    const { kind, value } = describe(m);
    const values = Array.isArray(value) ? value : [value];
    if (!values.some(isFlaggable)) continue;
    out.push({
      file: filePath,
      line: lineIndex + 1,
      column: m.index + 1,
      kind,
      snippet: line.trim().slice(0, 160),
    });
  }
}

/**
 * isFlaggable applies the "skip empty / skip letterless" heuristics
 * documented on findHardcodedStrings to a raw quoted literal (still
 * including its surrounding quotes).
 * @param {string} quoted
 * @returns {boolean}
 */
function isFlaggable(quoted) {
  if (!/^["'`]/.test(quoted)) return false; // an expression branch, e.g. t("key")
  let inner = quoted.trim().slice(1, -1);
  if (quoted.startsWith("`")) inner = inner.replace(/\$\{[^}]*\}/g, "");
  if (inner.trim() === "") return false;
  return /\p{L}/u.test(inner);
}

function escapeForRegex(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * relPath normalizes a file path to forward slashes for stable,
 * platform-independent allow-list keys (matching CODING_CONVENTIONS.md's
 * "use path, not filepath, for POSIX unit paths" spirit, applied here
 * to a Node tool rather than Go).
 * @param {string} p
 * @returns {string}
 */
export function relPath(p) {
  return p.split("\\").join("/");
}

/**
 * buildFindingKey builds the stable allow-list match key for one
 * finding: file, line, and kind (not the snippet text, which can shift
 * with unrelated nearby edits).
 * @param {Finding} finding
 * @returns {string}
 */
export function buildFindingKey(finding) {
  return `${relPath(finding.file)}:${finding.line}:${finding.kind}`;
}

/**
 * parseAllowList validates and normalizes the check-i18n allow-list
 * JSON shape: either a bare array or an {entries: [...]} wrapper (for a
 * leading _comment field), matching ui-audit-lib.mjs's parseAllowList
 * convention. Every entry MUST have a non-empty `key` and a non-empty
 * `reason` (SPEC-v0.8 §3-3 / Part B rules: "mandatory reason per
 * entry").
 * @param {any} raw
 * @returns {Array<{key:string, reason:string}>}
 * @throws {Error} on a malformed entry or top-level shape.
 */
export function parseAllowList(raw) {
  if (raw === null || raw === undefined) return [];
  const arr = Array.isArray(raw) ? raw : raw.entries;
  if (!Array.isArray(arr)) {
    throw new Error("check-i18n allow-list must be an array or an {entries: [...]} object");
  }
  for (const entry of arr) {
    if (!entry || typeof entry.key !== "string" || entry.key.length === 0) {
      throw new Error(`check-i18n allow-list entry missing a non-empty "key": ${JSON.stringify(entry)}`);
    }
    if (typeof entry.reason !== "string" || entry.reason.trim().length === 0) {
      throw new Error(`check-i18n allow-list entry ${JSON.stringify(entry.key)} missing a non-empty "reason"`);
    }
  }
  return arr;
}

/**
 * parsePendingList validates the check-i18n pending-file list JSON
 * shape: either a bare array of file paths or a {files: [...]}
 * wrapper.
 * @param {any} raw
 * @returns {string[]}
 * @throws {Error} on a malformed shape.
 */
export function parsePendingList(raw) {
  if (raw === null || raw === undefined) return [];
  const arr = Array.isArray(raw) ? raw : raw.files;
  if (!Array.isArray(arr) || arr.some((f) => typeof f !== "string")) {
    throw new Error("check-i18n pending-list must be an array of strings or a {files: [...]} object");
  }
  return arr.map(relPath);
}

/**
 * classifyFindings splits per-file findings into allowed vs. violation
 * buckets against allowList, matching ui-audit-lib.mjs's
 * aggregateReport style (a finding whose key is allow-listed with a
 * reason is "allowed" and reported informationally, not a failure).
 * @param {Finding[]} findings
 * @param {Array<{key:string, reason:string}>} allowList
 * @returns {{violations: Finding[], allowed: Finding[]}}
 */
export function classifyFindings(findings, allowList) {
  const allowedKeys = new Set(allowList.map((e) => e.key));
  const violations = [];
  const allowed = [];
  for (const f of findings) {
    if (allowedKeys.has(buildFindingKey(f))) allowed.push(f);
    else violations.push(f);
  }
  return { violations, allowed };
}

/**
 * evaluatePendingPolicy applies the pending-list enforcement rule
 * (SPEC-v0.8 §3-3 / Part B rules): a file listed as "pending" is
 * exempt from failing on findings (it hasn't been converted to t()
 * yet), but a pending file that turns out to have ZERO findings must
 * be removed from the list — otherwise the pending list would only
 * ever grow and nothing would force cleanup once a file is actually
 * converted.
 * @param {Map<string, Finding[]>} findingsByFile file path -> findings
 *   already filtered to violations-only (allow-listed findings don't
 *   count toward "has findings" either way, since they're accepted).
 * @param {string[]} pendingFiles normalized (relPath'd) pending list
 * @returns {{
 *   nonPendingViolations: Finding[],
 *   staleCleanPendingFiles: string[],
 * }}
 */
export function evaluatePendingPolicy(findingsByFile, pendingFiles) {
  const pendingSet = new Set(pendingFiles);
  const nonPendingViolations = [];
  const staleCleanPendingFiles = [];

  for (const [file, findings] of findingsByFile) {
    const isPending = pendingSet.has(relPath(file));
    if (isPending) {
      if (findings.length === 0) staleCleanPendingFiles.push(relPath(file));
      continue;
    }
    nonPendingViolations.push(...findings);
  }

  return { nonPendingViolations, staleCleanPendingFiles };
}

/**
 * flattenCatalogKeys flattens a nested i18n catalog JSON object (see
 * web/assets/js/core/i18n.js's header comment for the shape) into a
 * Set of dot-separated keys, exactly mirroring that module's own
 * flatten() so parity checks compare like-for-like. A leaf may be a
 * plain string or a {one, other} plural object; both count as a
 * single key for parity purposes (the placeholder check below inspects
 * plural leaves specially).
 * @param {Object} tree
 * @returns {Map<string, string|{one:string,other:string}>}
 */
export function flattenCatalog(tree, prefix = "") {
  const out = new Map();
  for (const [k, v] of Object.entries(tree)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (typeof v === "string") {
      out.set(key, v);
    } else if (v && typeof v === "object" && (typeof v.one === "string" || typeof v.other === "string")) {
      out.set(key, v);
    } else if (v && typeof v === "object") {
      for (const [ck, cv] of flattenCatalog(v, key)) out.set(ck, cv);
    }
  }
  return out;
}

/**
 * diffCatalogKeys compares two flattened catalogs (see flattenCatalog)
 * and returns the keys present in only one side.
 * @param {Map<string, any>} a
 * @param {Map<string, any>} b
 * @returns {{onlyInA: string[], onlyInB: string[]}}
 */
export function diffCatalogKeys(a, b) {
  const onlyInA = [...a.keys()].filter((k) => !b.has(k)).sort();
  const onlyInB = [...b.keys()].filter((k) => !a.has(k)).sort();
  return { onlyInA, onlyInB };
}

/**
 * extractPlaceholders extracts the sorted, de-duplicated set of
 * {name} placeholders from a catalog leaf (a plain string, or a
 * {one, other} plural object — in which case both sub-strings'
 * placeholders are unioned, since a translator must keep the same
 * variables available in both plural forms).
 * @param {string|{one?:string,other?:string}} leaf
 * @returns {string[]}
 */
export function extractPlaceholders(leaf) {
  const texts = typeof leaf === "string" ? [leaf] : [leaf.one, leaf.other].filter((s) => typeof s === "string");
  const seen = new Set();
  for (const text of texts) {
    for (const m of text.matchAll(/\{([a-zA-Z0-9_]+)\}/g)) seen.add(m[1]);
  }
  return [...seen].sort();
}

/**
 * diffPlaceholders compares every shared key between two flattened
 * catalogs and returns the keys whose placeholder sets differ.
 * @param {Map<string, any>} a
 * @param {Map<string, any>} b
 * @returns {string[]} sorted list of mismatched keys
 */
export function diffPlaceholders(a, b) {
  const mismatched = [];
  for (const [key, leafA] of a) {
    if (!b.has(key)) continue;
    const phA = extractPlaceholders(leafA);
    const phB = extractPlaceholders(b.get(key));
    if (phA.length !== phB.length || phA.some((p, i) => p !== phB[i])) {
      mismatched.push(key);
    }
  }
  return mismatched.sort();
}

// TFN_CALL matches a `t("literal.key"` or `t('literal.key'` call —
// only a literal first argument is checked (a dynamically-built key,
// e.g. `t(variable)`, can't be statically verified against the
// catalog by this tool and is intentionally not flagged either way).
const TFN_CALL = /\bt\(\s*(['"])([a-zA-Z0-9_.]+)\1/g;

/**
 * findTranslationKeyUsages scans source text for `t('literal.key')`
 * call sites and returns every literal key referenced.
 * @param {string} source
 * @returns {string[]}
 */
export function findTranslationKeyUsages(source) {
  const keys = [];
  let m;
  const re = new RegExp(TFN_CALL);
  while ((m = re.exec(source)) !== null) {
    keys.push(m[2]);
  }
  return keys;
}
