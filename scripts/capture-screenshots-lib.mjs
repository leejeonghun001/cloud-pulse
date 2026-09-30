// capture-screenshots-lib.mjs — pure logic for scripts/capture-screenshots.mjs:
// word-bounded marker substitution and protected-identifier verification.
//
// Deliberately zero DOM/browser/child_process dependencies so this can be
// covered by `node --test` directly (see web/test/capture-screenshots.test.mjs),
// mirroring scripts/ui-audit-lib.mjs's split from scripts/ui-audit.mjs.
//
// No third-party dependencies.

/**
 * Characters that count as "inside a token" for word-boundary purposes.
 * Hostnames legitimately contain '-' (and sometimes '_'), so both are
 * treated as token characters — a marker must NOT match when it is
 * merely a substring of a longer run of these characters (e.g. hostname
 * marker "abc" must not match inside "leeabchun001", "abc-1", or
 * "abc.local"'s "abclocal" if such a thing existed — "abc.local" IS a
 * boundary match on the "abc" side because "." is not a token char, but
 * "abc-1" is NOT a match because "-" is defined as a token char here so
 * "abc-1" is one token "abc-1", not "abc" + "-1").
 */
const TOKEN_CHAR = /[A-Za-z0-9_-]/;

/**
 * isWordBoundaryMatch checks whether `text.slice(index, index + needle.length)`
 * (assumed already verified to equal `needle`) is a whole-token occurrence:
 * the character immediately before and immediately after (if any) must NOT
 * be a token character.
 * @param {string} text
 * @param {number} index
 * @param {string} needle
 * @returns {boolean}
 */
function isWordBoundaryMatch(text, index, needle) {
  const before = index > 0 ? text[index - 1] : "";
  const after = index + needle.length < text.length ? text[index + needle.length] : "";
  if (before && TOKEN_CHAR.test(before)) return false;
  if (after && TOKEN_CHAR.test(after)) return false;
  return true;
}

/**
 * findWordBoundedOccurrences returns the start indices of every
 * whole-token occurrence of `needle` in `text`.
 * @param {string} text
 * @param {string} needle
 * @returns {number[]}
 */
export function findWordBoundedOccurrences(text, needle) {
  if (!needle) return [];
  const hits = [];
  let from = 0;
  while (true) {
    const idx = text.indexOf(needle, from);
    if (idx === -1) break;
    if (isWordBoundaryMatch(text, idx, needle)) hits.push(idx);
    from = idx + 1; // advance by 1, not needle.length, to not miss overlapping runs
  }
  return hits;
}

/**
 * isWithinAny returns true if [start, start+len) overlaps any [s,e) range
 * in `ranges`.
 * @param {number} start
 * @param {number} len
 * @param {Array<[number,number]>} ranges
 */
function isWithinAny(start, len, ranges) {
  const end = start + len;
  for (const [s, e] of ranges) {
    if (start < e && end > s) return true;
  }
  return false;
}

/**
 * URL_PATTERN matches http(s) URLs pointing at github.com or
 * raw.githubusercontent.com — substitution must never touch any
 * character inside such a URL, regardless of marker/protect-list
 * content, since these are structurally significant (install commands).
 */
/**
 * PROTECTED_URL_PATTERN_SOURCE is the source string (not a RegExp
 * object) for the github.com/raw.githubusercontent.com URL matcher —
 * exported separately so capture-screenshots.mjs can reconstruct the
 * exact same pattern inside an in-page eval script (a RegExp object
 * itself is not serializable via .toString() the way a function is).
 */
export const PROTECTED_URL_PATTERN_SOURCE = "https?:\\/\\/(?:raw\\.githubusercontent\\.com|github\\.com)\\/[^\\s\"'<>)]*";

const PROTECTED_URL_PATTERN = new RegExp(PROTECTED_URL_PATTERN_SOURCE, "g");

/**
 * findProtectedRanges returns the [start,end) ranges in `text` that must
 * never be modified by substitution: (a) any github.com/
 * raw.githubusercontent.com URL, and (b) any occurrence of a
 * protect-listed identifier string (e.g. the repo owner/name, the Go
 * module path) — matched as a plain substring (protected identifiers are
 * project constants, not user input, so no word-boundary requirement is
 * applied here; protecting a superset of occurrences is the safe
 * direction).
 * @param {string} text
 * @param {string[]} protectedIdentifiers
 * @returns {Array<[number,number]>}
 */
export function findProtectedRanges(text, protectedIdentifiers) {
  const ranges = [];
  for (const m of text.matchAll(PROTECTED_URL_PATTERN)) {
    ranges.push([m.index, m.index + m[0].length]);
  }
  for (const id of protectedIdentifiers || []) {
    if (!id) continue;
    let from = 0;
    while (true) {
      const idx = text.indexOf(id, from);
      if (idx === -1) break;
      ranges.push([idx, idx + id.length]);
      from = idx + id.length;
    }
  }
  return ranges;
}

/**
 * applyWordBoundedSubstitution replaces every whole-token, non-protected
 * occurrence of `real` with `demo` in `text`.
 * @param {string} text
 * @param {string} real
 * @param {string} demo
 * @param {string[]} protectedIdentifiers
 * @returns {{text: string, replacedCount: number}}
 */
export function applyWordBoundedSubstitution(text, real, demo, protectedIdentifiers) {
  if (!real) return { text, replacedCount: 0 };
  const protectedRanges = findProtectedRanges(text, protectedIdentifiers);
  const hits = findWordBoundedOccurrences(text, real).filter(
    (idx) => !isWithinAny(idx, real.length, protectedRanges),
  );
  if (hits.length === 0) return { text, replacedCount: 0 };

  let out = "";
  let cursor = 0;
  for (const idx of hits) {
    out += text.slice(cursor, idx) + demo;
    cursor = idx + real.length;
  }
  out += text.slice(cursor);
  return { text: out, replacedCount: hits.length };
}

/**
 * IPV4_WHOLE builds a regex matching a specific dotted-quad IPv4 address
 * only when NOT immediately adjacent to another digit or '.' on either
 * side (so "10.1.2.3" does not match inside "10.1.2.30" or
 * "110.1.2.3"). IPv6 and hostname/word markers use the generic
 * token-boundary logic above instead, since ':' (IPv6) is not a token
 * char and would already be a boundary; IPv4 needs its own check because
 * '.' is deliberately NOT a token character (so hostnames like
 * "abc.local" can match on the "abc" side) but a bare numeric IP DOES
 * need '.' and digits on both sides treated as extending the match.
 */
function isIpv4BoundaryMatch(text, index, needle) {
  const before = index > 0 ? text[index - 1] : "";
  const after = index + needle.length < text.length ? text[index + needle.length] : "";
  const numOrDot = /[0-9.]/;
  if (before && numOrDot.test(before)) return false;
  if (after && numOrDot.test(after)) return false;
  return true;
}

/**
 * applyIpv4Substitution replaces every occurrence of the exact IPv4
 * address `real` in `text` with `demo`, requiring that the match not be
 * a prefix/suffix of a longer dotted-decimal run (so "10.1.2.3" never
 * matches inside "10.1.2.30").
 * @param {string} text
 * @param {string} real
 * @param {string} demo
 * @param {string[]} protectedIdentifiers
 * @returns {{text: string, replacedCount: number}}
 */
export function applyIpv4Substitution(text, real, demo, protectedIdentifiers) {
  if (!real) return { text, replacedCount: 0 };
  const protectedRanges = findProtectedRanges(text, protectedIdentifiers);
  const hits = [];
  let from = 0;
  while (true) {
    const idx = text.indexOf(real, from);
    if (idx === -1) break;
    if (isIpv4BoundaryMatch(text, idx, real) && !isWithinAny(idx, real.length, protectedRanges)) {
      hits.push(idx);
    }
    from = idx + 1;
  }
  if (hits.length === 0) return { text, replacedCount: 0 };

  let out = "";
  let cursor = 0;
  for (const idx of hits) {
    out += text.slice(cursor, idx) + demo;
    cursor = idx + real.length;
  }
  out += text.slice(cursor);
  return { text: out, replacedCount: hits.length };
}

/**
 * applyAllSubstitutions runs a full ordered list of [real, demo, kind]
 * substitutions (kind: "word" for hostnames/generic markers, "ipv4" for
 * dotted-quad addresses) over `text`, protecting `protectedIdentifiers`
 * and any github.com/raw.githubusercontent.com URL throughout.
 * @param {string} text
 * @param {Array<{real: string, demo: string, kind?: "word"|"ipv4"}>} substitutions
 * @param {string[]} protectedIdentifiers
 * @returns {{text: string, replacedCount: number}}
 */
export function applyAllSubstitutions(text, substitutions, protectedIdentifiers) {
  let out = text;
  let total = 0;
  for (const sub of substitutions) {
    const fn = sub.kind === "ipv4" ? applyIpv4Substitution : applyWordBoundedSubstitution;
    const { text: next, replacedCount } = fn(out, sub.real, sub.demo, protectedIdentifiers);
    out = next;
    total += replacedCount;
  }
  return { text: out, replacedCount: total };
}

/**
 * assertProtectedIdentifiersPreserved verifies that every protected
 * identifier that was present (as a plain substring) in `before` is
 * still present verbatim in `after`. Throws an Error naming the first
 * violation found, so callers can fail the capture run hard rather than
 * silently ship a corrupted screenshot.
 * @param {string} before
 * @param {string} after
 * @param {string[]} protectedIdentifiers
 */
export function assertProtectedIdentifiersPreserved(before, after, protectedIdentifiers) {
  for (const id of protectedIdentifiers || []) {
    if (!id) continue;
    const wasPresent = before.indexOf(id) !== -1;
    if (!wasPresent) continue;
    const stillPresent = after.indexOf(id) !== -1;
    if (!stillPresent) {
      throw new Error(
        `protected identifier "${id}" was present before substitution but is missing after — aborting`,
      );
    }
  }
}
