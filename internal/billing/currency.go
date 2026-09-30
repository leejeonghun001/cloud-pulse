package billing

import "fmt"

// ConvertedAmount is the result of converting a fixed USD amount into a
// hub-wide display currency (SPEC-v0.6 §3). USD is always the
// underlying stored/calculated value; KRW (when selected) is a
// display-only conversion using a manually entered rate.
type ConvertedAmount struct {
	// USD is the original, unconverted amount.
	USD float64 `json:"usd"`
	// Currency is the display currency this amount was converted to
	// ("USD" or "KRW").
	Currency string `json:"currency"`
	// Value is USD converted to Currency and rounded per that
	// currency's convention (cents for USD, whole won for KRW). Equal
	// to USD, unrounded beyond USD's own cent rounding, when Currency
	// is "USD".
	Value float64 `json:"value"`
	// KRWPerUSD is the exchange rate used, 0 when Currency is "USD".
	KRWPerUSD float64 `json:"krw_per_usd,omitempty"`
	// RateUpdatedAt is the unix-seconds time KRWPerUSD was entered, 0
	// when Currency is "USD".
	RateUpdatedAt int64 `json:"rate_updated_at,omitempty"`
}

// ErrRateRequired is returned by ValidateDisplayCurrencySettings when
// KRW is selected but no positive exchange rate has been provided —
// surfaced by the hub as 400 "rate_required" per SPEC-v0.6 §3.
var ErrRateRequired = fmt.Errorf("billing: krw_per_usd is required when display currency is KRW")

// ValidateDisplayCurrencySettings validates s per SPEC-v0.6 §3's "표시
// 통화" rules: currency must be USD or KRW, and selecting KRW requires
// a positive (non-zero, non-negative) krw_per_usd. Returns
// ErrRateRequired for the missing-rate case, or a plain error for an
// unrecognized currency value.
func ValidateDisplayCurrencySettings(currency string, krwPerUSD float64) error {
	switch currency {
	case "USD":
		return nil
	case "KRW":
		if krwPerUSD <= 0 {
			return ErrRateRequired
		}
		return nil
	default:
		return fmt.Errorf("billing: unsupported display currency %q", currency)
	}
}

// ConvertForDisplay converts usd into the hub's display currency.
// currency must already be validated (ValidateDisplayCurrencySettings);
// an unrecognized currency value falls back to USD-passthrough rather
// than panicking, since this is a pure display helper, not the source
// of truth for validation.
//
//   - USD: Value = usd, rounded to the nearest cent.
//   - KRW: Value = round(usd * krwPerUSD) to the nearest whole won (KRW
//     has no subdivision in typical display use — see SPEC-v0.6 §3's
//     example "₩1,234,567").
func ConvertForDisplay(usd float64, currency string, krwPerUSD float64, rateUpdatedAt int64) ConvertedAmount {
	out := ConvertedAmount{USD: roundCents(usd), Currency: currency}
	switch currency {
	case "KRW":
		out.Value = roundWon(usd * krwPerUSD)
		out.KRWPerUSD = krwPerUSD
		out.RateUpdatedAt = rateUpdatedAt
	default:
		out.Currency = "USD"
		out.Value = out.USD
	}
	return out
}

// roundWon rounds a KRW amount to the nearest whole won using
// round-half-away-from-zero, mirroring roundCents' rule at a
// zero-decimal scale (with the same epsilon nudge for float64
// representation error).
func roundWon(krw float64) float64 {
	if krw < 0 {
		return -roundWon(-krw)
	}
	const epsilon = 1e-9
	return float64(int64(krw + 0.5 + epsilon))
}
