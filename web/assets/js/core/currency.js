// currency.js — pure display-currency conversion helpers mirroring
// internal/billing/currency.go (SPEC-v0.6 §3): USD is always the
// stored/calculated amount; KRW is a display-only conversion using a
// manually entered rate. No DOM access here so this is fully covered
// by `node --test` without a browser.

/**
 * roundCents rounds a USD amount to the nearest cent using
 * round-half-away-from-zero, with a small epsilon to counter float64
 * representation error (e.g. 1.005 is actually stored as
 * 1.00499999999999989...). Mirrors internal/billing/network.go's
 * roundCents exactly.
 * @param {number} usd
 * @returns {number}
 */
export function roundCents(usd) {
  if (usd < 0) return -roundCents(-usd);
  const epsilon = 1e-9;
  return Math.trunc(usd * 100 + 0.5 + epsilon) / 100;
}

/**
 * roundWon rounds a KRW amount to the nearest whole won using the same
 * round-half-away-from-zero rule as roundCents, at a zero-decimal
 * scale. Mirrors internal/billing/currency.go's roundWon exactly.
 * @param {number} krw
 * @returns {number}
 */
export function roundWon(krw) {
  if (krw < 0) return -roundWon(-krw);
  const epsilon = 1e-9;
  return Math.trunc(krw + 0.5 + epsilon);
}

/**
 * convertForDisplay converts a fixed USD amount into a hub-wide display
 * currency, mirroring internal/billing/currency.go's ConvertForDisplay.
 * An unrecognized currency value falls back to USD-passthrough.
 * @param {number} usd
 * @param {"USD"|"KRW"|string} currency
 * @param {number} [krwPerUSD]
 * @param {number} [rateUpdatedAt] unix seconds
 * @returns {{usd: number, currency: string, value: number, krwPerUSD: number, rateUpdatedAt: number}}
 */
export function convertForDisplay(usd, currency, krwPerUSD = 0, rateUpdatedAt = 0) {
  const roundedUSD = roundCents(usd);
  if (currency === "KRW") {
    return {
      usd: roundedUSD,
      currency: "KRW",
      value: roundWon(usd * krwPerUSD),
      krwPerUSD,
      rateUpdatedAt,
    };
  }
  return { usd: roundedUSD, currency: "USD", value: roundedUSD, krwPerUSD: 0, rateUpdatedAt: 0 };
}

/**
 * formatUSD renders a fixed USD (or other CLI-reported, e.g. from an
 * OCI/AWS response that already carries its own currency code) amount
 * with a currency code suffix — used for the raw cloud-CLI provider
 * cards, which SPEC-v0.6 §3 says show whatever currency the CLI itself
 * reported unconverted.
 * @param {number} amount
 * @param {string} [currency] defaults to "USD" when the CLI hasn't
 *   reported one yet (e.g. a never-successful snapshot).
 * @param {{taxNote?: boolean}} [opts] set taxNote:false to omit the
 *   "(excl. tax)" suffix — used where the caller already shows that
 *   note once for the whole card/table instead of repeating it on
 *   every individual number (SPEC-v0.6 §2).
 * @returns {string}
 */
export function formatUSD(amount, currency, opts = {}) {
  const n = Number(amount);
  const value = Number.isFinite(n) ? n : 0;
  const code = currency || "USD";
  const taxNote = opts.taxNote === false ? "" : " (excl. tax)";
  return `${value.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })} ${code}${taxNote}`;
}

/**
 * formatDisplayAmount renders a fixed USD amount converted into the
 * hub's display currency settings, with the "excl. tax" suffix — for
 * KRW — the USD original + exchange rate + entry date parenthetical
 * shown in SPEC-v0.6 §3's example: "₩1,234,567 (≈ $891.20 · rate
 * 1,385.5 as of 2026-09-30)".
 * @param {number} usd
 * @param {{currency?: string, krw_per_usd?: number, rate_updated_at?: number}|null|undefined} displayCurrency
 * @param {{taxNote?: boolean}} [opts] set taxNote:false to omit the
 *   "(excl. tax)" suffix — used in a table body where the column
 *   header or card footer already states it once for every row
 *   (SPEC-v0.6 §2), instead of repeating it on every cell.
 * @returns {string}
 */
export function formatDisplayAmount(usd, displayCurrency, opts = {}) {
  const currency = displayCurrency?.currency || "USD";
  const krwPerUSD = displayCurrency?.krw_per_usd || 0;
  const rateUpdatedAt = displayCurrency?.rate_updated_at || 0;
  const converted = convertForDisplay(usd, currency, krwPerUSD, rateUpdatedAt);
  const taxNote = opts.taxNote === false ? "" : " (excl. tax)";

  if (converted.currency !== "KRW") {
    return `$${converted.value.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}${taxNote}`;
  }

  const won = `₩${converted.value.toLocaleString()}`;
  const usdPart = `$${converted.usd.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  const dateStr = rateUpdatedAt ? new Date(rateUpdatedAt * 1000).toISOString().slice(0, 10) : "unknown";
  return `${won} (≈ ${usdPart} · rate ${krwPerUSD} / entered ${dateStr})${taxNote}`;
}
