// Tests for core/display-currency.js (cached display-currency loader) and
// the KRW/USD rendering used by the overview "Est. cost" column and the
// host "Cost" card (SPEC-v0.6 §3).
import { test } from "node:test";
import assert from "node:assert/strict";
import { isFresh, loadDisplayCurrency, resetDisplayCurrencyCache, CACHE_TTL_MS } from "../assets/js/core/display-currency.js";
import { formatDisplayAmount } from "../assets/js/core/currency.js";

test("isFresh honours the TTL and rejects never-fetched / future timestamps", () => {
  assert.equal(isFresh(0, 1000), false);
  assert.equal(isFresh(1000, 1000 + CACHE_TTL_MS - 1), true);
  assert.equal(isFresh(1000, 1000 + CACHE_TTL_MS), false);
  assert.equal(isFresh(5000, 1000), false);
});

test("loadDisplayCurrency caches within the TTL and refetches after it", async () => {
  resetDisplayCurrencyCache();
  let calls = 0;
  let now = 1_000_000;
  const deps = {
    now: () => now,
    fetchBilling: async () => {
      calls++;
      return { display_currency: { currency: "KRW", krw_per_usd: 1385.5, rate_updated_at: 1790726400 } };
    },
  };
  const a = await loadDisplayCurrency(undefined, deps);
  now += 1000;
  const b = await loadDisplayCurrency(undefined, deps);
  assert.equal(calls, 1);
  assert.deepEqual(a, b);
  now += CACHE_TTL_MS;
  await loadDisplayCurrency(undefined, deps);
  assert.equal(calls, 2);
});

test("loadDisplayCurrency falls back to null (plain USD) when billing is unavailable", async () => {
  resetDisplayCurrencyCache();
  const v = await loadDisplayCurrency(undefined, { now: () => 1, fetchBilling: async () => { throw new Error("HTTP 404"); } });
  assert.equal(v, null);
  assert.equal(formatDisplayAmount(12.5, v, { taxNote: false }).startsWith("$12.50"), true);
});

test("loadDisplayCurrency propagates aborts so navigation cancels cleanly", async () => {
  resetDisplayCurrencyCache();
  const abort = Object.assign(new Error("aborted"), { name: "AbortError" });
  await assert.rejects(loadDisplayCurrency(undefined, { now: () => 1, fetchBilling: async () => { throw abort; } }), { name: "AbortError" });
});

test("KRW display shows won, the USD original and the rate note without a tax suffix", () => {
  const text = formatDisplayAmount(891.2, { currency: "KRW", krw_per_usd: 1385.5, rate_updated_at: 1790726400 }, { taxNote: false });
  assert.match(text, /^₩1,234,758 \(≈ \$891\.20 · rate 1385\.5 \/ entered 2026-09-30\)$/);
});
