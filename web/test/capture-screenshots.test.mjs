// capture-screenshots.test.mjs — unit tests for the pure marker-
// substitution helpers in scripts/capture-screenshots-lib.mjs (word-
// bounded hostname/word markers, whole-address IPv4 markers, and the
// protected-identifier preservation assertion). See that module's
// header for the rationale.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  findWordBoundedOccurrences,
  applyWordBoundedSubstitution,
  applyIpv4Substitution,
  applyAllSubstitutions,
  assertProtectedIdentifiersPreserved,
} from "../../scripts/capture-screenshots-lib.mjs";

test("word marker does not match inside a longer identifier (owner-name collision)", () => {
  const { text, replacedCount } = applyWordBoundedSubstitution(
    "leeabchun001/x",
    "abc",
    "demo-hub",
    ["leeabchun001"],
  );
  assert.equal(text, "leeabchun001/x");
  assert.equal(replacedCount, 0);
});

test("word marker matches when followed by a non-token character ('.')", () => {
  const { text, replacedCount } = applyWordBoundedSubstitution("abc.local", "abc", "demo-hub", []);
  assert.equal(text, "demo-hub.local");
  assert.equal(replacedCount, 1);
});

test("word marker matches as a standalone word surrounded by spaces", () => {
  const { text, replacedCount } = applyWordBoundedSubstitution("host abc up", "abc", "demo-hub", []);
  assert.equal(text, "host demo-hub up");
  assert.equal(replacedCount, 1);
});

test("word marker does NOT match when hyphen-joined to more token characters ('-' is a token char)", () => {
  const { text, replacedCount } = applyWordBoundedSubstitution("abc-1", "abc", "demo-hub", []);
  assert.equal(text, "abc-1");
  assert.equal(replacedCount, 0);
});

test("findWordBoundedOccurrences finds only the standalone occurrence, not the embedded one", () => {
  const hits = findWordBoundedOccurrences("leeabchun001/x host abc up", "abc");
  // "leeabchun001" -> no match; "host abc up" -> one match.
  assert.equal(hits.length, 1);
  assert.equal("leeabchun001/x host abc up".slice(hits[0], hits[0] + 3), "abc");
});

test("IPv4 marker matches the exact address but not as a prefix of a longer one", () => {
  const exact = applyIpv4Substitution("agent at 10.1.2.3 reporting", "10.1.2.3", "192.0.2.10", []);
  assert.equal(exact.text, "agent at 192.0.2.10 reporting");
  assert.equal(exact.replacedCount, 1);

  const longer = applyIpv4Substitution("agent at 10.1.2.30 reporting", "10.1.2.3", "192.0.2.10", []);
  assert.equal(longer.text, "agent at 10.1.2.30 reporting");
  assert.equal(longer.replacedCount, 0);
});

test("protected github.com URL is never substituted even when it contains the marker", () => {
  const text =
    "curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh | sudo bash -s -- --hub-url http://abc:8090";
  const { text: out, replacedCount } = applyWordBoundedSubstitution(text, "abc", "demo-hub", [
    "leejeonghun001",
    "leejeonghun001/cloud-pulse",
  ]);
  assert.match(
    out,
    /https:\/\/raw\.githubusercontent\.com\/leejeonghun001\/cloud-pulse\/main\/scripts\/install-agent\.sh/,
  );
  assert.match(out, /--hub-url http:\/\/demo-hub:8090/);
  assert.equal(replacedCount, 1);
});

test("protected identifier substring is left untouched by an unrelated marker substitution", () => {
  // Regression for the exact reported defect: hostname marker is a
  // prefix of the owner name; the owner name itself must never be
  // mangled even though it textually contains the marker as a substring.
  const text = "install from github.com/leejeonghun001/cloud-pulse using host leejeonghun-vm";
  const protectedIdentifiers = ["leejeonghun001", "leejeonghun001/cloud-pulse"];
  const { text: out } = applyWordBoundedSubstitution(text, "leejeonghun", "demo-hub", protectedIdentifiers);
  assert.match(out, /github\.com\/leejeonghun001\/cloud-pulse/);
  // "leejeonghun-vm" is a distinct whole token (hyphen is a token char,
  // so "leejeonghun-vm" is ONE token, not a boundary match) — the
  // marker must NOT match inside it either.
  assert.match(out, /leejeonghun-vm/);
});

test("assertProtectedIdentifiersPreserved passes when nothing was lost", () => {
  const before = "see github.com/leejeonghun001/cloud-pulse for more";
  const after = "see github.com/leejeonghun001/cloud-pulse for more (demo-hub)";
  assert.doesNotThrow(() =>
    assertProtectedIdentifiersPreserved(before, after, ["leejeonghun001", "leejeonghun001/cloud-pulse"]),
  );
});

test("assertProtectedIdentifiersPreserved throws when a protected identifier is lost", () => {
  const before = "see github.com/leejeonghun001/cloud-pulse for more";
  const after = "see github.com/demo-hub001/cloud-pulse for more";
  assert.throws(
    () => assertProtectedIdentifiersPreserved(before, after, ["leejeonghun001"]),
    /leejeonghun001/,
  );
});

test("applyAllSubstitutions runs word and ipv4 substitutions together and protects identifiers", () => {
  const text =
    "host leejeonghun-vm (10.1.2.3) — repo at github.com/leejeonghun001/cloud-pulse, ip 10.1.2.30 unrelated";
  const protectedIdentifiers = ["leejeonghun001", "leejeonghun001/cloud-pulse"];
  const { text: out, replacedCount } = applyAllSubstitutions(
    text,
    [
      { real: "leejeonghun-vm", demo: "demo-hub", kind: "word" },
      { real: "10.1.2.3", demo: "192.0.2.10", kind: "ipv4" },
    ],
    protectedIdentifiers,
  );
  assert.match(out, /host demo-hub/);
  assert.match(out, /192\.0\.2\.10/);
  assert.match(out, /10\.1\.2\.30 unrelated/); // longer IP untouched
  assert.match(out, /github\.com\/leejeonghun001\/cloud-pulse/); // protected untouched
  assert.equal(replacedCount, 2);
  assertProtectedIdentifiersPreserved(text, out, protectedIdentifiers);
});
