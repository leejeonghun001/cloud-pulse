// password-policy.test.mjs — unit tests for
// assets/js/core/password-policy.js.
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  MIN_LENGTH,
  evaluateRequirements,
  isPasswordValid,
  scoreStrength,
  strengthLevel,
  strengthLabel,
} from "../assets/js/core/password-policy.js";

test("evaluateRequirements: all unmet for empty password", () => {
  const reqs = evaluateRequirements("");
  assert.equal(reqs.find((r) => r.id === "length").met, false);
  assert.equal(reqs.find((r) => r.id === "not-blank").met, false);
});

test("evaluateRequirements: length requirement respects MIN_LENGTH", () => {
  const short = evaluateRequirements("a".repeat(MIN_LENGTH - 1));
  const exact = evaluateRequirements("a".repeat(MIN_LENGTH));
  assert.equal(short.find((r) => r.id === "length").met, false);
  assert.equal(exact.find((r) => r.id === "length").met, true);
});

test("evaluateRequirements: rejects the literal default password", () => {
  const reqs = evaluateRequirements("changeme");
  assert.equal(reqs.find((r) => r.id === "not-default").met, false);
});

test("evaluateRequirements: rejects a password equal to the current one", () => {
  const reqs = evaluateRequirements("samepassword", "samepassword");
  assert.equal(reqs.find((r) => r.id === "not-current").met, false);
});

test("evaluateRequirements: not-current passes when no current password is known", () => {
  const reqs = evaluateRequirements("newpassword123", "");
  assert.equal(reqs.find((r) => r.id === "not-current").met, true);
});

test("evaluateRequirements: whitespace-only password fails not-blank", () => {
  const reqs = evaluateRequirements("        ");
  assert.equal(reqs.find((r) => r.id === "not-blank").met, false);
});

test("isPasswordValid: true only when every requirement passes", () => {
  assert.equal(isPasswordValid("aValidNewPassword1!", "oldpassword"), true);
  assert.equal(isPasswordValid("short", "oldpassword"), false);
  assert.equal(isPasswordValid("changeme", "oldpassword"), false);
  assert.equal(isPasswordValid("oldpassword", "oldpassword"), false);
});

test("scoreStrength: increases with length and character-class variety", () => {
  const empty = scoreStrength("");
  const weak = scoreStrength("aaaaaaaa");
  const strong = scoreStrength("aB3!aB3!aB3!xyz9");
  assert.equal(empty, 0);
  assert.ok(weak < strong);
  assert.ok(strong <= 4);
});

test("scoreStrength: stays within [0,4]", () => {
  for (const pw of ["", "a", "aA1!", "aA1!aA1!aA1!aA1!aA1!aA1!"]) {
    const score = scoreStrength(pw);
    assert.ok(score >= 0 && score <= 4, `score for ${JSON.stringify(pw)} was ${score}`);
  }
});

test("strengthLevel maps scores to level names", () => {
  assert.equal(strengthLevel(0), "empty");
  assert.equal(strengthLevel(1), "weak");
  assert.equal(strengthLevel(2), "fair");
  assert.equal(strengthLevel(3), "good");
  assert.equal(strengthLevel(4), "strong");
});

test("strengthLabel returns a human label for each non-empty level", () => {
  assert.equal(strengthLabel("weak"), "Weak");
  assert.equal(strengthLabel("fair"), "Fair");
  assert.equal(strengthLabel("good"), "Good");
  assert.equal(strengthLabel("strong"), "Strong");
  assert.equal(strengthLabel("empty"), "");
});
