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

		got := ComputeEgress("2026-01", tx, 0, limit, now)

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
	})

	t.Run("past_month_projection_equals_tx", func(t *testing.T) {
		t.Parallel()
		tx := uint64(5 * GiB)
		now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) // exactly month end for Feb 2026
		got := ComputeEgress("2026-02", tx, 0, 100*GiB, now)
		if got.ProjectedTxBytes != tx {
			t.Errorf("ProjectedTxBytes = %d, want %d (past month end)", got.ProjectedTxBytes, tx)
		}

		// Well past month end too.
		now2 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		got2 := ComputeEgress("2026-02", tx, 0, 100*GiB, now2)
		if got2.ProjectedTxBytes != tx {
			t.Errorf("ProjectedTxBytes = %d, want %d (far past month end)", got2.ProjectedTxBytes, tx)
		}
	})

	t.Run("month_start_clamp_elapsed_min_1h", func(t *testing.T) {
		t.Parallel()
		// now == month start exactly => elapsed 0, clamped to 1h.
		start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		tx := uint64(1 * GiB)
		got := ComputeEgress("2026-04", tx, 0, 100*GiB, start)

		monthDuration := 30 * 24 * time.Hour // April has 30 days
		wantProjected := uint64(float64(tx) * float64(monthDuration) / float64(time.Hour))
		if got.ProjectedTxBytes != wantProjected {
			t.Errorf("ProjectedTxBytes = %d, want %d", got.ProjectedTxBytes, wantProjected)
		}
	})

	t.Run("unparsable_month_projection_equals_tx", func(t *testing.T) {
		t.Parallel()
		tx := uint64(42)
		got := ComputeEgress("bogus", tx, 7, 100, time.Now())
		if got.ProjectedTxBytes != tx {
			t.Errorf("ProjectedTxBytes = %d, want %d", got.ProjectedTxBytes, tx)
		}
		if got.RxBytes != 7 {
			t.Errorf("RxBytes = %d, want 7", got.RxBytes)
		}
	})

	t.Run("limit_zero_percent_zero_level_ok", func(t *testing.T) {
		t.Parallel()
		got := ComputeEgress("2026-01", 999*GiB, 0, 0, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))
		if got.Percent != 0 {
			t.Errorf("Percent = %v, want 0", got.Percent)
		}
		if got.Level != EgressOK {
			t.Errorf("Level = %q, want ok", got.Level)
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
