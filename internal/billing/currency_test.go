package billing

import "testing"

func TestValidateDisplayCurrencySettings(t *testing.T) {
	tests := []struct {
		name      string
		currency  string
		krwPerUSD float64
		wantErr   error // nil = no error; ErrRateRequired checked by ==; else just non-nil
	}{
		{"usd always valid", "USD", 0, nil},
		{"usd valid even with a rate set", "USD", 1385.5, nil},
		{"krw with positive rate", "KRW", 1385.5, nil},
		{"krw with zero rate rejected", "KRW", 0, ErrRateRequired},
		{"krw with negative rate rejected", "KRW", -1, ErrRateRequired},
		{"unsupported currency", "EUR", 1, nil}, // checked separately below
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDisplayCurrencySettings(tc.currency, tc.krwPerUSD)
			if tc.name == "unsupported currency" {
				if err == nil {
					t.Fatal("expected an error for unsupported currency")
				}
				return
			}
			if tc.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != nil && err != tc.wantErr {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestConvertForDisplay_USD(t *testing.T) {
	got := ConvertForDisplay(123.456, "USD", 0, 0)
	if got.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", got.Currency)
	}
	if got.Value != 123.46 {
		t.Errorf("Value = %v, want 123.46", got.Value)
	}
	if got.KRWPerUSD != 0 || got.RateUpdatedAt != 0 {
		t.Errorf("expected zero rate fields for USD, got %+v", got)
	}
}

func TestConvertForDisplay_KRW(t *testing.T) {
	got := ConvertForDisplay(891.20, "KRW", 1385.5, 1735500000)
	if got.Currency != "KRW" {
		t.Errorf("Currency = %q, want KRW", got.Currency)
	}
	wantValue := roundWon(891.20 * 1385.5)
	if got.Value != wantValue {
		t.Errorf("Value = %v, want %v", got.Value, wantValue)
	}
	if got.KRWPerUSD != 1385.5 {
		t.Errorf("KRWPerUSD = %v, want 1385.5", got.KRWPerUSD)
	}
	if got.RateUpdatedAt != 1735500000 {
		t.Errorf("RateUpdatedAt = %v, want 1735500000", got.RateUpdatedAt)
	}
	if got.USD != 891.20 {
		t.Errorf("USD = %v, want 891.20", got.USD)
	}
}

func TestConvertForDisplay_UnrecognizedFallsBackToUSD(t *testing.T) {
	got := ConvertForDisplay(50, "EUR", 1000, 123)
	if got.Currency != "USD" {
		t.Errorf("Currency = %q, want USD fallback", got.Currency)
	}
	if got.Value != 50 {
		t.Errorf("Value = %v, want 50", got.Value)
	}
}

func TestRoundWon(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{1234567.4, 1234567},
		{1234567.5, 1234568},
		{1234567.6, 1234568},
		{-100.5, -101},
	}
	for _, tc := range tests {
		if got := roundWon(tc.in); got != tc.want {
			t.Errorf("roundWon(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestConvertForDisplay_ZeroAmount(t *testing.T) {
	got := ConvertForDisplay(0, "KRW", 1385.5, 100)
	if got.Value != 0 || got.USD != 0 {
		t.Errorf("expected zero conversion, got %+v", got)
	}
}
