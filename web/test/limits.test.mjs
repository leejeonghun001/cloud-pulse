// limits.test.mjs — unit tests for assets/js/limits.js (settings page
// GiB<->bytes parsing/validation and token masking helpers).
import { test } from "node:test";
import assert from "node:assert/strict";
import { GIB, parseLimitGiB, formatLimitGiB, maskToken } from "../assets/js/core/limits.js";

test("parseLimitGiB: blank means no override", () => {
  assert.deepEqual(parseLimitGiB(""), { ok: true, bytes: null });
  assert.deepEqual(parseLimitGiB("   "), { ok: true, bytes: null });
});

test("parseLimitGiB: 0 means explicitly unlimited", () => {
  assert.deepEqual(parseLimitGiB("0"), { ok: true, bytes: 0 });
});

test("parseLimitGiB: integer GiB converts exactly", () => {
  assert.deepEqual(parseLimitGiB("1"), { ok: true, bytes: GIB });
  assert.deepEqual(parseLimitGiB("100"), { ok: true, bytes: 100 * GIB });
});

test("parseLimitGiB: fractional GiB rounds to nearest byte", () => {
  const result = parseLimitGiB("1.5");
  assert.equal(result.ok, true);
  assert.equal(result.bytes, Math.round(1.5 * GIB));
});

test("parseLimitGiB: rejects negative values", () => {
  const result = parseLimitGiB("-1");
  assert.equal(result.ok, false);
  assert.match(result.error, /negative/);
});

test("parseLimitGiB: rejects non-numeric input", () => {
  const result = parseLimitGiB("abc");
  assert.equal(result.ok, false);
  assert.match(result.error, /number/);
});

test("parseLimitGiB: rejects NaN/Infinity", () => {
  assert.equal(parseLimitGiB("NaN").ok, false);
  assert.equal(parseLimitGiB("Infinity").ok, false);
});

test("parseLimitGiB: rejects values too large to be a safe integer", () => {
  const result = parseLimitGiB("999999999999999999999");
  assert.equal(result.ok, false);
  assert.match(result.error, /too large/);
});

test("formatLimitGiB: null/undefined render blank (no override)", () => {
  assert.equal(formatLimitGiB(null), "");
  assert.equal(formatLimitGiB(undefined), "");
});

test("formatLimitGiB: 0 renders '0' (explicitly unlimited)", () => {
  assert.equal(formatLimitGiB(0), "0");
});

test("formatLimitGiB: whole GiB renders without trailing decimals", () => {
  assert.equal(formatLimitGiB(GIB), "1");
  assert.equal(formatLimitGiB(100 * GIB), "100");
});

test("formatLimitGiB: fractional GiB trims trailing zeros", () => {
  assert.equal(formatLimitGiB(1.5 * GIB), "1.5");
  assert.equal(formatLimitGiB(2147483648), "2");
});

test("formatLimitGiB: negative/non-finite render blank", () => {
  assert.equal(formatLimitGiB(-5), "");
  assert.equal(formatLimitGiB(NaN), "");
});

test("parseLimitGiB and formatLimitGiB round-trip", () => {
  for (const gib of ["0", "1", "1.5", "100", "0.25", "10240"]) {
    const parsed = parseLimitGiB(gib);
    assert.equal(parsed.ok, true);
    assert.equal(formatLimitGiB(parsed.bytes), gib.replace(/^0+(?=\d)/, ""));
  }
});

test("maskToken masks all but first/last 4 characters", () => {
  assert.equal(maskToken("abcd1234efgh5678"), "abcd…5678");
});

test("maskToken fully masks short tokens", () => {
  assert.equal(maskToken("short"), "••••");
  assert.equal(maskToken(""), "••••");
});
