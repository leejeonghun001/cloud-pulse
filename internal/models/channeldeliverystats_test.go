package models

import "testing"

func diagCode(c DiagnosisCode) *DiagnosisCode { return &c }

func TestAggregateChannelDeliveryStats_Empty(t *testing.T) {
	t.Parallel()
	got := AggregateChannelDeliveryStats(nil)
	if got.Total != 0 || got.SuccessRate() != -1 {
		t.Errorf("got %+v, want zero stats with SuccessRate() == -1", got)
	}
}

func TestAggregateChannelDeliveryStats_MixedOutcomes(t *testing.T) {
	t.Parallel()
	deliveries := []Delivery{
		{OK: true, At: 100, DurationMS: 200},
		{OK: true, At: 300, DurationMS: 400},
		{OK: false, At: 200, Diagnosis: diagCode(DiagnosisTimeout)},
		{OK: false, At: 500, Diagnosis: diagCode(DiagnosisConnectionRefused)},
	}
	got := AggregateChannelDeliveryStats(deliveries)

	if got.Total != 4 || got.Success != 2 {
		t.Errorf("Total/Success = %d/%d, want 4/2", got.Total, got.Success)
	}
	if got.LastSuccessAt != 300 {
		t.Errorf("LastSuccessAt = %d, want 300", got.LastSuccessAt)
	}
	if got.LastFailureAt != 500 {
		t.Errorf("LastFailureAt = %d, want 500", got.LastFailureAt)
	}
	if got.LastFailureDiagnosis != DiagnosisConnectionRefused {
		t.Errorf("LastFailureDiagnosis = %q, want %q (the most recent failure, not just the last in the slice)", got.LastFailureDiagnosis, DiagnosisConnectionRefused)
	}
	// avg of 200 and 400 (only successes carry duration here)
	if got.AvgLatencyMS != 300 {
		t.Errorf("AvgLatencyMS = %d, want 300", got.AvgLatencyMS)
	}
	if got.MaxLatencyMS != 400 {
		t.Errorf("MaxLatencyMS = %d, want 400", got.MaxLatencyMS)
	}
}

func TestAggregateChannelDeliveryStats_ZeroLatencyDeliveriesExcludedFromAverage(t *testing.T) {
	t.Parallel()
	deliveries := []Delivery{
		{OK: true, At: 1, DurationMS: 0}, // pre-v0.8 delivery, unknown latency
		{OK: true, At: 2, DurationMS: 100},
	}
	got := AggregateChannelDeliveryStats(deliveries)
	if got.AvgLatencyMS != 100 {
		t.Errorf("AvgLatencyMS = %d, want 100 (zero-latency entries excluded)", got.AvgLatencyMS)
	}
	if got.MaxLatencyMS != 100 {
		t.Errorf("MaxLatencyMS = %d, want 100", got.MaxLatencyMS)
	}
}

func TestAggregateChannelDeliveryStats_FailureWithNilDiagnosisClearsPreviousIfMoreRecent(t *testing.T) {
	t.Parallel()
	deliveries := []Delivery{
		{OK: false, At: 100, Diagnosis: diagCode(DiagnosisTimeout)},
		{OK: false, At: 200, Diagnosis: nil},
	}
	got := AggregateChannelDeliveryStats(deliveries)
	if got.LastFailureDiagnosis != "" {
		t.Errorf("LastFailureDiagnosis = %q, want empty (most recent failure had no diagnosis)", got.LastFailureDiagnosis)
	}
}

func TestDeliveriesForChannel_FiltersByChannelID(t *testing.T) {
	t.Parallel()
	events := []AlertEvent{
		{ID: 1, Deliveries: []Delivery{{ChannelID: 1, At: 10}, {ChannelID: 2, At: 20}}},
		{ID: 2, Deliveries: []Delivery{{ChannelID: 1, At: 30}}},
	}
	got := DeliveriesForChannel(events, 1)
	if len(got) != 2 || got[0].At != 10 || got[1].At != 30 {
		t.Errorf("got %+v, want deliveries at 10 and 30 for channel 1", got)
	}
}

func TestDeliveriesForChannel_NoMatchesReturnsEmptyNotNil(t *testing.T) {
	t.Parallel()
	got := DeliveriesForChannel([]AlertEvent{{Deliveries: []Delivery{{ChannelID: 99}}}}, 1)
	if got == nil || len(got) != 0 {
		t.Errorf("got %+v, want non-nil empty slice", got)
	}
}

func TestHasConsecutiveDeliveryFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		deliveries []Delivery
		n          int
		want       bool
	}{
		{
			name:       "empty history",
			deliveries: nil,
			n:          ConsecutiveFailureThreshold,
			want:       false,
		},
		{
			name: "fewer than n failures",
			deliveries: []Delivery{
				{OK: false, At: 10},
				{OK: false, At: 20},
			},
			n:    ConsecutiveFailureThreshold,
			want: false,
		},
		{
			name: "exactly n failures",
			deliveries: []Delivery{
				{OK: false, At: 10},
				{OK: false, At: 20},
				{OK: false, At: 30},
			},
			n:    ConsecutiveFailureThreshold,
			want: true,
		},
		{
			name: "older success before n failures",
			deliveries: []Delivery{
				{OK: true, At: 10},
				{OK: false, At: 20},
				{OK: false, At: 30},
				{OK: false, At: 40},
			},
			n:    ConsecutiveFailureThreshold,
			want: true,
		},
		{
			name: "newest success breaks the streak",
			deliveries: []Delivery{
				{OK: false, At: 10},
				{OK: false, At: 20},
				{OK: false, At: 30},
				{OK: true, At: 40},
			},
			n:    ConsecutiveFailureThreshold,
			want: false,
		},
		{
			name: "newest-first input order is normalized",
			deliveries: []Delivery{
				{OK: false, At: 30},
				{OK: false, At: 10},
				{OK: false, At: 20},
			},
			n:    ConsecutiveFailureThreshold,
			want: true,
		},
		{
			name: "n <= 0 is never a failure signal",
			deliveries: []Delivery{
				{OK: false, At: 10},
			},
			n:    0,
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := HasConsecutiveDeliveryFailures(tt.deliveries, tt.n); got != tt.want {
				t.Errorf("HasConsecutiveDeliveryFailures(%d deliveries, n=%d) = %v, want %v", len(tt.deliveries), tt.n, got, tt.want)
			}
		})
	}
}
