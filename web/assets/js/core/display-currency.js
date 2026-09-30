// display-currency.js — cached access to the hub's billing display
// currency (SPEC-v0.6 §3) for pages other than Costs.
//
// The overview "Est. cost" column and the host "Cost" card must honour the
// same USD/KRW display setting as the Costs page. GET /api/v1/billing
// already carries `display_currency` for any signed-in user, so this module
// reuses it and caches the result for a short TTL to avoid one extra
// request per 15-second refresh.

import { getBilling } from "./api.js";

/** CACHE_TTL_MS bounds how stale a cached setting may be. */
export const CACHE_TTL_MS = 60_000;

/** @type {{value: object|null, fetchedAt: number}} */
const cache = { value: null, fetchedAt: 0 };

/**
 * isFresh reports whether a cache entry fetched at fetchedAt is still
 * usable at now (pure, exported for tests).
 * @param {number} fetchedAt epoch ms of the last successful fetch (0 = never)
 * @param {number} now epoch ms
 * @param {number} [ttl]
 * @returns {boolean}
 */
export function isFresh(fetchedAt, now, ttl = CACHE_TTL_MS) {
  return fetchedAt > 0 && now - fetchedAt >= 0 && now - fetchedAt < ttl;
}

/**
 * loadDisplayCurrency returns the hub's display-currency settings, served
 * from the cache while fresh. Any fetch failure (billing unavailable, older
 * hub) resolves to null so callers fall back to plain USD rendering.
 * @param {AbortSignal} [signal]
 * @param {{now?: () => number, fetchBilling?: (signal?: AbortSignal) => Promise<any>}} [deps] test hooks
 * @returns {Promise<object|null>}
 */
export async function loadDisplayCurrency(signal, deps = {}) {
  const now = deps.now ? deps.now() : Date.now();
  if (isFresh(cache.fetchedAt, now)) return cache.value;
  const fetchBilling = deps.fetchBilling || getBilling;
  try {
    const billing = await fetchBilling(signal);
    cache.value = billing?.display_currency || null;
    cache.fetchedAt = now;
    return cache.value;
  } catch (err) {
    if (err && err.name === "AbortError") throw err;
    return cache.value;
  }
}

/** resetDisplayCurrencyCache clears the cache (tests, and after the setting changes). */
export function resetDisplayCurrencyCache() {
  cache.value = null;
  cache.fetchedAt = 0;
}
