// check-i18n-lib.test.mjs — unit tests for scripts/check-i18n-lib.mjs's
// pure hard-coded-string detector, allow-list/pending-list policy, and
// catalog parity helpers, mirroring ui-audit-lib.test.mjs's fixture
// style (inline source snippets as fixtures rather than real files).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  findHardcodedStrings,
  buildFindingKey,
  relPath,
  parseAllowList,
  parsePendingList,
  classifyFindings,
  evaluatePendingPolicy,
  flattenCatalog,
  diffCatalogKeys,
  extractPlaceholders,
  diffPlaceholders,
  findTranslationKeyUsages,
} from "../../scripts/check-i18n-lib.mjs";

test("findHardcodedStrings: detects el() text: property", () => {
  const src = `el("span", { class: "x", text: "Hello world" });`;
  const found = findHardcodedStrings(src, "f.js");
  assert.equal(found.length, 1);
  assert.equal(found[0].kind, "key:text");
});

test("findHardcodedStrings: detects title/aria-label/placeholder/alt/label/message/description", () => {
  const src = [
    `el("a", { attrs: { title: "Some title" } });`,
    `el("button", { attrs: { "aria-label": "Close dialog" } });`,
    `el("input", { attrs: { placeholder: "Type here" } });`,
    `el("img", { attrs: { alt: "A photo" } });`,
    `dropdownItem({ label: "Sign out" });`,
    `showToast({ message: "Saved successfully" });`,
    `createDialog({ description: "Are you sure?" });`,
  ].join("\n");
  const found = findHardcodedStrings(src, "f.js");
  const kinds = found.map((f) => f.kind).sort();
  assert.deepEqual(kinds, [
    "key:alt",
    "key:aria-label",
    "key:description",
    "key:label",
    "key:message",
    "key:placeholder",
    "key:title",
  ]);
});

test("findHardcodedStrings: detects textContent/innerText assignment", () => {
  const src = `errorEl.textContent = "Something failed";\nfoo.innerText = "Bar baz";`;
  const found = findHardcodedStrings(src, "f.js");
  assert.equal(found.length, 2);
  assert.deepEqual(found.map((f) => f.kind), ["assign:textContent", "assign:innerText"]);
});

test("findHardcodedStrings: detects confirm()/alert() calls", () => {
  const src = `if (confirm("Delete this host?")) { alert("Deleted."); }`;
  const found = findHardcodedStrings(src, "f.js");
  assert.equal(found.length, 2);
  assert.deepEqual(found.map((f) => f.kind), ["call:confirm", "call:alert"]);
});

test("findHardcodedStrings: ignores strings inside // line comments", () => {
  const src = `// text: "Not real UI text"\nconst x = 1;`;
  assert.equal(findHardcodedStrings(src, "f.js").length, 0);
});

test("findHardcodedStrings: ignores strings inside /* block comments */ single-line", () => {
  const src = `/* text: "Not real UI text" */\nconst x = 1;`;
  assert.equal(findHardcodedStrings(src, "f.js").length, 0);
});

test("findHardcodedStrings: ignores strings inside a multi-line block comment", () => {
  const src = [
    "/*",
    ' * text: "Not real UI text either"',
    " */",
    'el("span", { text: "Real text" });',
  ].join("\n");
  const found = findHardcodedStrings(src, "f.js");
  assert.equal(found.length, 1);
  assert.equal(found[0].line, 4);
});

test("findHardcodedStrings: skips empty string literals", () => {
  const src = `el("span", { text: "" });`;
  assert.equal(findHardcodedStrings(src, "f.js").length, 0);
});

test("findHardcodedStrings: skips letterless literals (symbols/digits only)", () => {
  const src = `el("span", { text: "—" }); el("span", { text: "123" });`;
  assert.equal(findHardcodedStrings(src, "f.js").length, 0);
});

test("findHardcodedStrings: does not flag a call to t() itself", () => {
  const src = `el("span", { text: t("common.cancel") });`;
  assert.equal(findHardcodedStrings(src, "f.js").length, 0);
});

test("findHardcodedStrings: reports correct file/line in Finding", () => {
  const src = `const a = 1;\nel("span", { text: "Second line text" });`;
  const found = findHardcodedStrings(src, "web/assets/js/pages/x.js");
  assert.equal(found.length, 1);
  assert.equal(found[0].file, "web/assets/js/pages/x.js");
  assert.equal(found[0].line, 2);
});

test("buildFindingKey/relPath: stable composition, normalizes backslashes", () => {
  const finding = { file: "web\\assets\\js\\x.js", line: 5, column: 1, kind: "key:text", snippet: "" };
  assert.equal(buildFindingKey(finding), "web/assets/js/x.js:5:key:text");
  assert.equal(relPath("a\\b\\c.js"), "a/b/c.js");
});

test("parseAllowList: accepts a bare array and an {entries} wrapper", () => {
  assert.deepEqual(parseAllowList([{ key: "a", reason: "r" }]), [{ key: "a", reason: "r" }]);
  assert.deepEqual(parseAllowList({ entries: [{ key: "a", reason: "r" }] }), [{ key: "a", reason: "r" }]);
  assert.deepEqual(parseAllowList(null), []);
  assert.deepEqual(parseAllowList(undefined), []);
});

test("parseAllowList: throws on a missing or empty reason", () => {
  assert.throws(() => parseAllowList([{ key: "a", reason: "" }]), /reason/);
  assert.throws(() => parseAllowList([{ key: "a" }]), /reason/);
});

test("parseAllowList: throws on a missing key", () => {
  assert.throws(() => parseAllowList([{ reason: "r" }]), /key/);
});

test("parsePendingList: accepts a bare array and a {files} wrapper, normalizes paths", () => {
  assert.deepEqual(parsePendingList(["a\\b.js"]), ["a/b.js"]);
  assert.deepEqual(parsePendingList({ files: ["c.js"] }), ["c.js"]);
  assert.deepEqual(parsePendingList(null), []);
});

test("parsePendingList: throws on a non-string entry", () => {
  assert.throws(() => parsePendingList([1]), /pending-list/);
});

test("classifyFindings: splits allow-listed vs. violation findings", () => {
  const findings = [
    { file: "a.js", line: 1, column: 1, kind: "key:text", snippet: "x" },
    { file: "b.js", line: 2, column: 1, kind: "key:text", snippet: "y" },
  ];
  const allowList = [{ key: "a.js:1:key:text", reason: "ok" }];
  const { violations, allowed } = classifyFindings(findings, allowList);
  assert.equal(violations.length, 1);
  assert.equal(violations[0].file, "b.js");
  assert.equal(allowed.length, 1);
  assert.equal(allowed[0].file, "a.js");
});

test("evaluatePendingPolicy: pending files with findings are excluded from violations", () => {
  const findingsByFile = new Map([
    ["pending.js", [{ file: "pending.js", line: 1, column: 1, kind: "key:text", snippet: "x" }]],
    ["normal.js", [{ file: "normal.js", line: 1, column: 1, kind: "key:text", snippet: "y" }]],
  ]);
  const { nonPendingViolations, staleCleanPendingFiles } = evaluatePendingPolicy(findingsByFile, ["pending.js"]);
  assert.equal(nonPendingViolations.length, 1);
  assert.equal(nonPendingViolations[0].file, "normal.js");
  assert.deepEqual(staleCleanPendingFiles, []);
});

test("evaluatePendingPolicy: a pending file with zero findings is flagged stale", () => {
  const findingsByFile = new Map([["clean.js", []]]);
  const { nonPendingViolations, staleCleanPendingFiles } = evaluatePendingPolicy(findingsByFile, ["clean.js"]);
  assert.deepEqual(nonPendingViolations, []);
  assert.deepEqual(staleCleanPendingFiles, ["clean.js"]);
});

test("flattenCatalog: flattens nested namespaces into dot-separated keys", () => {
  const flat = flattenCatalog({ common: { cancel: "Cancel" }, nav: { host_count: { one: "1", other: "N" } } });
  assert.equal(flat.get("common.cancel"), "Cancel");
  assert.deepEqual(flat.get("nav.host_count"), { one: "1", other: "N" });
});

test("diffCatalogKeys: finds keys present in only one side", () => {
  const a = flattenCatalog({ x: "1", y: "2" });
  const b = flattenCatalog({ x: "1", z: "3" });
  assert.deepEqual(diffCatalogKeys(a, b), { onlyInA: ["y"], onlyInB: ["z"] });
});

test("extractPlaceholders: extracts from a plain string and unions across a plural pair", () => {
  assert.deepEqual(extractPlaceholders("Hello {name}!"), ["name"]);
  assert.deepEqual(extractPlaceholders({ one: "{count} host", other: "{count} hosts ({total})" }), ["count", "total"]);
});

test("diffPlaceholders: flags a key whose placeholder set differs between catalogs", () => {
  const a = flattenCatalog({ greet: "Hi {name}" });
  const b = flattenCatalog({ greet: "Hi {person}" });
  assert.deepEqual(diffPlaceholders(a, b), ["greet"]);
});

test("diffPlaceholders: matching placeholder sets produce no mismatches", () => {
  const a = flattenCatalog({ greet: "Hi {name}", other: "no placeholders" });
  const b = flattenCatalog({ greet: "안녕 {name}", other: "플레이스홀더 없음" });
  assert.deepEqual(diffPlaceholders(a, b), []);
});

test("findTranslationKeyUsages: extracts literal t('key') call sites", () => {
  const src = `t("common.cancel"); t('nav.overview', { x: 1 }); t(dynamicKey);`;
  assert.deepEqual(findTranslationKeyUsages(src), ["common.cancel", "nav.overview"]);
});
