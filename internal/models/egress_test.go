package models

import (
	"testing"
	"time"
)

func TestLevelFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		percent float64
		limit   uint64
		want    EgressLevel
	}{
		{"limit_zero_unlimited", 500, 0, EgressOK},
		{"79_99", 79.99, 100, EgressOK},
		{"80_exact", 80, 100, EgressWarning},
		{"94_99", 94.99, 100, EgressWarning},
		{"95_exact", 95, 100, EgressCritical},
		{"99_99", 99.99, 100, EgressCritical},
		{"100_exact", 100, 100, EgressExceeded},
		{"150", 150, 100, EgressExceeded},
		{"zero_percent", 0, 100, EgressOK},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := LevelFor(tc.percent, tc.limit); got != tc.want {
				t.Errorf("LevelFor(%v, %v) = %q, want %q", tc.percent, tc.limit, got, tc.want)
			}
		})
	}
}

func TestEgressLevel_Severity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		level EgressLevel
		want  int
	}{
		{EgressOK, 0},
		{EgressWarning, 1},
		{EgressCritical, 2},
		{EgressExceeded, 3},
		{EgressLevel("bogus"), 0},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.level), func(t *testing.T) {
			t.Parallel()
			if got := tc.level.Severity(); got != tc.want {
				t.Errorf("Severity(%q) = %d, want %d", tc.level, got, tc.want)
			}
		})
	}
}

func TestMonthOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"utc", time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC), "2026-03"},
		{"non_utc_converts", time.Date(2026, 3, 1, 2, 0, 0, 0, time.FixedZone("X", 5*3600)), "2026-02"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MonthOf(tc.in); got != tc.want {
				t.Errorf("MonthOf(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMonthBounds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		month     string
		wantStart time.Time
		wantEnd   time.Time
		wantErr   bool
	}{
		{
			name:      "dec_to_jan_rollover",
			month:     "2025-12",
			wantStart: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "leap_feb_2028",
			month:     "2028-02",
			wantStart: time.Date(2028, 2, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2028, 3, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "bad_format",
			month:   "not-a-month",
			wantErr: true,
		},
		{
			name:    "bad_format_day_included",
			month:   "2026-01-15",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start, end, err := MonthBounds(tc.month)
			if (err != nil) != tc.wantErr {
				t.Fatalf("MonthBounds(%q) err = %v, wantErr %v", tc.month, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if !start.Equal(tc.wantStart) {
				t.Errorf("start = %v, want %v", start, tc.wantStart)
			}
			if !end.Equal(tc.wantEnd) {
				t.Errorf("end = %v, want %v", end, tc.wantEnd)
			}
			if start.Location() != time.UTC || end.Location() != time.UTC {
				t.Errorf("MonthBounds must return UTC times, got start loc=%v end loc=%v", start.Location(), end.Location())
			}
			// leap year sanity check for Feb 2028.
			if tc.name == "leap_feb_2028" {
				days := end.Sub(start).Hours() / 24
				if days != 29 {
					t.Errorf("Feb 2028 span = %v days, want 29 (leap year)", days)
				}
			}
		})
	}
}

func TestComputeEgress(t *testing.T) {
	t.Parallel()

	t.Run("mid_month_projection", func(t *testing.T) {
		t.Parallel()
		// January has 31 days = 744 hours. now is 10 days (240h) in.
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		now := start.Add(240 * time.Hour)
		tx := uint64(10 * GiB)
		limit := uint64(100 * GiB)

		got := ComputeEgress("2026-01", tx, 0, limit, 0, now)

		if got.Month != "2026-01" {
			t.Errorf("Month = %q, want 2026-01", got.Month)
		}
		if got.TxBytes != tx {
			t.Errorf("TxBytes = %d, want %d", got.TxBytes, tx)
		}
		wantPercent := float64(tx) / float64(limit) * 100
		if got.Percent != wantPercent {
			t.Errorf("Percent = %v, want %v", got.Percent, wantPercent)
		}
		if got.Level != EgressOK {
			t.Errorf("Level = %q, want ok (10%% usage is below the 80%% warning threshold)", got.Level)
		}
		// Projected = tx * monthDuration / elapsed = 10GiB * 744h / 240h
		wantProjected := uint64(float64(tx) * 744.0 / 240.0)
		if got.ProjectedTxBytes != wantProjected {
			t.Errorf("ProjectedTxBytes = %d, want %d", got.ProjectedTxBytes, wantProjected)
		}
		if got.LimitSource != "agent" {
			t.Errorf("LimitSource = %q, want agent", got.LimitSource)
		}
		if got.RxLimitSource != "none" {
			t.Errorf("RxLimitSource = %q, want none", got.RxLimitSource)
		}
	})

	t.Run("rx_mirrors_tx_logic_independently", func(t *testing.T) {
		t.Parallel()
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		now := start.Add(240 * time.Hour)
		tx, txLimit := uint64(1*GiB), uint64(0) // tx unlimited
		rx, rxLimit := uint64(96*GiB), uint64(100*GiB)

		got := ComputeEgress("2026-01", tx, rx, txLimit, rxLimit, now)

		if got.Percent != 0 || got.Level != EgressOK {
			t.Errorf("tx (unlimited) Percent/Level = %v/%q, want 0/ok", got.Percent, got.Level)
		}
		wantRxPercent := float64(rx) / float64(rxLimit) * 100
		if got.RxPercent != wantRxPercent {
			t.Errorf("RxPercent = %v, want %v", got.RxPercent, wantRxPercent)
		}
		if got.RxLevel != EgressCritical {
			t.Errorf("RxLevel = %q, want critical (96%% is >= 95%%)", got.RxLevel)
		}
		wantProjectedRx := uint64(float64(rx) * 744.0 / 240.0)
		if got.ProjectedRxBytes != wantProjectedRx {
			t.Errorf("ProjectedRxBytes = %d, want %d", got.ProjectedRxBytes, wantProjectedRx)
		}
		if got.RxBytes != rx {
			t.Errorf("RxBytes = %d, want %d", got.RxBytes, rx)
		}
	})

	t.Run("past_month_projection_equals_tx_and_rx", func(t *testing.T) {
		t.Parallel()
		tx, rx := uint64(5*GiB), uint64(3*GiB)
		now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) // exactly month end for Feb 2026
		got := ComputeEgress("2026-02", tx, rx, 100*GiB, 100*GiB, now)
		if got.ProjectedTxBytes != tx {
			t.Errorf("ProjectedTxBytes = %d, want %d (past month end)", got.ProjectedTxBytes, tx)
		}
		if got.ProjectedRxBytes != rx {
			t.Errorf("ProjectedRxBytes = %d, want %d (past month end)", got.ProjectedRxBytes, rx)
		}

		// Well past month end too.
		now2 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		got2 := ComputeEgress("2026-02", tx, rx, 100*GiB, 100*GiB, now2)
		if got2.ProjectedTxBytes != tx {
			t.Errorf("ProjectedTxBytes = %d, want %d (far past month end)", got2.ProjectedTxBytes, tx)
		}
		if got2.ProjectedRxBytes != rx {
			t.Errorf("ProjectedRxBytes = %d, want %d (far past month end)", got2.ProjectedRxBytes, rx)
		}
	})

	t.Run("month_start_clamp_elapsed_min_1h", func(t *testing.T) {
		t.Parallel()
		// now == month start exactly => elapsed 0, clamped to 1h.
		start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		tx := uint64(1 * GiB)
		got := ComputeEgress("2026-04", tx, 0, 100*GiB, 0, start)

		monthDuration := 30 * 24 * time.Hour // April has 30 days
		wantProjected := uint64(float64(tx) * float64(monthDuration) / float64(time.Hour))
		if got.ProjectedTxBytes != wantProjected {
			t.Errorf("ProjectedTxBytes = %d, want %d", got.ProjectedTxBytes, wantProjected)
		}
	})

	t.Run("unparsable_month_projection_equals_tx_and_rx", func(t *testing.T) {
		t.Parallel()
		tx, rx := uint64(42), uint64(7)
		got := ComputeEgress("bogus", tx, rx, 100, 100, time.Now())
		if got.ProjectedTxBytes != tx {
			t.Errorf("ProjectedTxBytes = %d, want %d", got.ProjectedTxBytes, tx)
		}
		if got.ProjectedRxBytes != rx {
			t.Errorf("ProjectedRxBytes = %d, want %d", got.ProjectedRxBytes, rx)
		}
		if got.RxBytes != 7 {
			t.Errorf("RxBytes = %d, want 7", got.RxBytes)
		}
	})

	t.Run("limit_zero_percent_zero_level_ok_both_directions", func(t *testing.T) {
		t.Parallel()
		got := ComputeEgress("2026-01", 999*GiB, 999*GiB, 0, 0, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))
		if got.Percent != 0 {
			t.Errorf("Percent = %v, want 0", got.Percent)
		}
		if got.Level != EgressOK {
			t.Errorf("Level = %q, want ok", got.Level)
		}
		if got.RxPercent != 0 {
			t.Errorf("RxPercent = %v, want 0", got.RxPercent)
		}
		if got.RxLevel != EgressOK {
			t.Errorf("RxLevel = %q, want ok", got.RxLevel)
		}
	})

	t.Run("default_limit_sources_agent_and_none", func(t *testing.T) {
		t.Parallel()
		got := ComputeEgress("2026-01", GiB, GiB, 100*GiB, 50*GiB, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))
		if got.LimitSource != "agent" {
			t.Errorf("LimitSource = %q, want agent", got.LimitSource)
		}
		if got.RxLimitSource != "none" {
			t.Errorf("RxLimitSource = %q, want none", got.RxLimitSource)
		}
	})

	// 32-bit safety: uint64 byte counters/limits near and above the
	// 32-bit boundary (2^32) must not overflow or truncate when computed
	// on a GOARCH where int/uint is 32 bits (e.g. arm/386). All internal
	// math uses uint64/float64 explicitly, never platform-width int.
	t.Run("32bit_safety_large_counters", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name   string
			tx, rx uint64
			txLim  uint64
			rxLim  uint64
		}{
			{
				name:  "just_above_32bit_boundary",
				tx:    uint64(1<<32) + 1024,
				rx:    uint64(1<<32) + 2048,
				txLim: uint64(1<<32) * 2,
				rxLim: uint64(1<<32) * 2,
			},
			{
				name:  "near_uint64_max_quarter",
				tx:    uint64(1) << 62,
				rx:    uint64(1) << 61,
				txLim: uint64(1) << 63,
				rxLim: uint64(1) << 62,
			},
			{
				name:  "tx_equals_limit_at_scale",
				tx:    uint64(4) << 30, // 4 GiB
				rx:    uint64(2) << 30,
				txLim: uint64(4) << 30,
				rxLim: uint64(8) << 30,
			},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				now := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
				got := ComputeEgress("2026-01", tc.tx, tc.rx, tc.txLim, tc.rxLim, now)

				if got.TxBytes != tc.tx {
					t.Errorf("TxBytes = %d, want %d", got.TxBytes, tc.tx)
				}
				if got.RxBytes != tc.rx {
					t.Errorf("RxBytes = %d, want %d", got.RxBytes, tc.rx)
				}
				wantPercent := float64(tc.tx) / float64(tc.txLim) * 100
				if got.Percent != wantPercent {
					t.Errorf("Percent = %v, want %v", got.Percent, wantPercent)
				}
				wantRxPercent := float64(tc.rx) / float64(tc.rxLim) * 100
				if got.RxPercent != wantRxPercent {
					t.Errorf("RxPercent = %v, want %v", got.RxPercent, wantRxPercent)
				}
				// Projection must not overflow/wrap: projected tx must be
				// >= actual tx (elapsed < monthDuration for a mid-month ts).
				if got.ProjectedTxBytes < tc.tx {
					t.Errorf("ProjectedTxBytes = %d, want >= tx %d (no overflow/truncation)", got.ProjectedTxBytes, tc.tx)
				}
				if got.ProjectedRxBytes < tc.rx {
					t.Errorf("ProjectedRxBytes = %d, want >= rx %d (no overflow/truncation)", got.ProjectedRxBytes, tc.rx)
				}
			})
		}
	})
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   uint64
		want string
	}{
		{"bytes", 512, "512 B"},
		{"exactly_1kib", 1024, "1.0 KiB"},
		{"1_5_gib", uint64(1.5 * float64(GiB)), "1.5 GiB"},
		{"1_tib", TiB, "1.0 TiB"},
		{"zero", 0, "0 B"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := HumanBytes(tc.in); got != tc.want {
				t.Errorf("HumanBytes(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
