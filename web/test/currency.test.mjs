// currency.test.mjs — unit tests for assets/js/core/currency.js,
// mirroring internal/billing/currency_test.go's cases (round-half-away-
// -from-zero cent/won rounding, USD passthrough, KRW conversion).
import { test } from "node:test";
import assert from "node:assert/strict";
import { roundCents, roundWon, convertForDisplay, formatUSD, formatDisplayAmount } from "../assets/js/core/currency.js";

test("roundCents: rounds half away from zero", () => {
  assert.equal(roundCents(1.005), 1.01);
  assert.equal(roundCents(1.004), 1.0);
  assert.equal(roundCents(0), 0);
});

test("roundCents: negative amounts round symmetrically", () => {
  assert.equal(roundCents(-1.005), -1.01);
});

test("roundWon: rounds to nearest whole won", () => {
  assert.equal(roundWon(1234567.4), 1234567);
  assert.equal(roundWon(1234567.5), 1234568);
});

test("roundWon: negative amounts round symmetrically", () => {
  assert.equal(roundWon(-0.5), -1);
});

test("convertForDisplay: USD passthrough rounds to cents", () => {
  const result = convertForDisplay(12.3456, "USD");
  assert.equal(result.currency, "USD");
  assert.equal(result.value, 12.35);
  assert.equal(result.usd, 12.35);
  assert.equal(result.krwPerUSD, 0);
});

test("convertForDisplay: KRW multiplies by the manual rate and rounds to won", () => {
  const result = convertForDisplay(891.2, "KRW", 1385.5, 1735500000);
  assert.equal(result.currency, "KRW");
  assert.equal(result.usd, 891.2);
  assert.equal(result.value, Math.round(891.2 * 1385.5));
  assert.equal(result.krwPerUSD, 1385.5);
  assert.equal(result.rateUpdatedAt, 1735500000);
});

test("convertForDisplay: unrecognized currency falls back to USD", () => {
  const result = convertForDisplay(5, "EUR");
  assert.equal(result.currency, "USD");
  assert.equal(result.value, 5);
});

test("formatUSD: renders two decimals with currency code and tax note", () => {
  assert.equal(formatUSD(100, "USD"), "100.00 USD (excl. tax)");
});

test("formatUSD: defaults to USD when no currency given", () => {
  assert.match(formatUSD(0), /USD/);
});

test("formatUSD: non-finite input renders as zero", () => {
  assert.match(formatUSD(NaN, "USD"), /^0\.00 USD/);
});

test("formatDisplayAmount: USD settings render a $-prefixed value", () => {
  const text = formatDisplayAmount(100, { currency: "USD" });
  assert.match(text, /^\$100\.00/);
  assert.match(text, /excl\. tax/);
});

test("formatDisplayAmount: KRW settings render won + USD original + rate + date", () => {
  const text = formatDisplayAmount(891.2, { currency: "KRW", krw_per_usd: 1385.5, rate_updated_at: 1780128000 });
  assert.match(text, /₩/);
  assert.match(text, /\$891\.20/);
  assert.match(text, /1385\.5/);
  assert.match(text, /2026-05-30/);
});

test("formatDisplayAmount: missing display currency settings default to USD", () => {
  const text = formatDisplayAmount(50, null);
  assert.match(text, /^\$50\.00/);
});
