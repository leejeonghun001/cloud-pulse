package models

import "testing"

func TestComputeChannelHealthState(t *testing.T) {
	t.Parallel()

	failed := &SyntheticResult{Outcome: SyntheticFailed}
	verified := &SyntheticResult{Outcome: SyntheticVerified}
	accepted := &SyntheticResult{Outcome: SyntheticAccepted}

	tests := []struct {
		name          string
		lastSynthetic *SyntheticResult
		stats7d       ChannelDeliveryStats
		want          ChannelHealthState
	}{
		{
			name:          "failed synthetic overrides perfect delivery stats",
			lastSynthetic: failed,
			stats7d:       ChannelDeliveryStats{Total: 100, Success: 100},
			want:          ChannelFailing,
		},
		{
			name:          "failed synthetic overrides even no delivery data",
			lastSynthetic: failed,
			stats7d:       ChannelDeliveryStats{},
			want:          ChannelFailing,
		},
		{
			name:          "no synthetic and no deliveries is unknown",
			lastSynthetic: nil,
			stats7d:       ChannelDeliveryStats{},
			want:          ChannelUnknown,
		},
		{
			name:          "verified synthetic with no deliveries is unknown",
			lastSynthetic: verified,
			stats7d:       ChannelDeliveryStats{},
			want:          ChannelUnknown,
		},
		{
			name:          "100% success rate is healthy",
			lastSynthetic: verified,
			stats7d:       ChannelDeliveryStats{Total: 10, Success: 10},
			want:          ChannelHealthy,
		},
		{
			name:          "exactly at degraded threshold (90%) is healthy (exclusive bound)",
			lastSynthetic: accepted,
			stats7d:       ChannelDeliveryStats{Total: 10, Success: 9},
			want:          ChannelHealthy,
		},
		{
			name:          "just below degraded threshold is degraded",
			lastSynthetic: nil,
			stats7d:       ChannelDeliveryStats{Total: 100, Success: 89},
			want:          ChannelDegraded,
		},
		{
			name:          "exactly at failing threshold (50%) is degraded (exclusive bound)",
			lastSynthetic: nil,
			stats7d:       ChannelDeliveryStats{Total: 10, Success: 5},
			want:          ChannelDegraded,
		},
		{
			name:          "just below failing threshold is failing",
			lastSynthetic: nil,
			stats7d:       ChannelDeliveryStats{Total: 100, Success: 49},
			want:          ChannelFailing,
		},
		{
			name:          "zero success rate is failing",
			lastSynthetic: nil,
			stats7d:       ChannelDeliveryStats{Total: 10, Success: 0},
			want:          ChannelFailing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ComputeChannelHealthState(tt.lastSynthetic, tt.stats7d)
			if got != tt.want {
				t.Errorf("ComputeChannelHealthState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChannelDeliveryStats_SuccessRate(t *testing.T) {
	t.Parallel()

	if got := (ChannelDeliveryStats{}).SuccessRate(); got != -1 {
		t.Errorf("zero Total SuccessRate() = %v, want -1", got)
	}
	if got := (ChannelDeliveryStats{Total: 4, Success: 3}).SuccessRate(); got != 0.75 {
		t.Errorf("SuccessRate() = %v, want 0.75", got)
	}
	if got := (ChannelDeliveryStats{Total: 3, Success: 0}).SuccessRate(); got != 0 {
		t.Errorf("SuccessRate() = %v, want 0", got)
	}
}

func TestValidWeekday(t *testing.T) {
	t.Parallel()
	for _, w := range []int{0, 3, 6} {
		if !ValidWeekday(w) {
			t.Errorf("ValidWeekday(%d) = false, want true", w)
		}
	}
	for _, w := range []int{-1, 7, 100} {
		if ValidWeekday(w) {
			t.Errorf("ValidWeekday(%d) = true, want false", w)
		}
	}
}

func TestValidMinuteOfDay(t *testing.T) {
	t.Parallel()
	for _, m := range []int{0, 720, 1439} {
		if !ValidMinuteOfDay(m) {
			t.Errorf("ValidMinuteOfDay(%d) = false, want true", m)
		}
	}
	for _, m := range []int{-1, 1440, 5000} {
		if ValidMinuteOfDay(m) {
			t.Errorf("ValidMinuteOfDay(%d) = true, want false", m)
		}
	}
}
