package channelhealth

import (
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestSelectFallbackChannel(t *testing.T) {
	t.Parallel()

	channels := []models.NotifyChannel{
		{ID: 1, Name: "self", Enabled: true},
		{ID: 2, Name: "disabled", Enabled: false},
		{ID: 3, Name: "failing", Enabled: true},
		{ID: 4, Name: "healthy-higher-id", Enabled: true},
		{ID: 5, Name: "healthy-lower-id", Enabled: true},
	}
	health := map[int64]models.ChannelHealthState{
		3: models.ChannelFailing,
		4: models.ChannelHealthy,
		5: models.ChannelDegraded,
	}

	tests := []struct {
		name       string
		candidates []models.NotifyChannel
		failingID  int64
		health     map[int64]models.ChannelHealthState
		wantID     int64
		wantFound  bool
	}{
		{
			name:       "self excluded, disabled excluded, failing excluded, picks lowest remaining ID",
			candidates: channels,
			failingID:  1,
			health:     health,
			wantID:     4,
			wantFound:  true,
		},
		{
			name:       "degraded channel is still eligible (only failing is excluded)",
			candidates: []models.NotifyChannel{{ID: 5, Enabled: true}},
			failingID:  1,
			health:     health,
			wantID:     5,
			wantFound:  true,
		},
		{
			name:       "unknown health (no history) is eligible",
			candidates: []models.NotifyChannel{{ID: 9, Enabled: true}},
			failingID:  1,
			health:     map[int64]models.ChannelHealthState{},
			wantFound:  true,
			wantID:     9,
		},
		{
			name:       "no candidates at all yields not found (banner path)",
			candidates: nil,
			failingID:  1,
			health:     health,
			wantFound:  false,
		},
		{
			name:       "only self available yields not found",
			candidates: []models.NotifyChannel{{ID: 1, Enabled: true}},
			failingID:  1,
			health:     health,
			wantFound:  false,
		},
		{
			name:       "only disabled channels available yields not found",
			candidates: []models.NotifyChannel{{ID: 2, Enabled: false}, {ID: 3, Enabled: false}},
			failingID:  1,
			health:     health,
			wantFound:  false,
		},
		{
			name:       "only failing channels available yields not found",
			candidates: []models.NotifyChannel{{ID: 3, Enabled: true}},
			failingID:  1,
			health:     health,
			wantFound:  false,
		},
		{
			name:       "self is the only enabled non-failing channel yields not found",
			candidates: []models.NotifyChannel{{ID: 1, Enabled: true}, {ID: 2, Enabled: false}, {ID: 3, Enabled: true}},
			failingID:  1,
			health:     health,
			wantFound:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotID, gotFound := SelectFallbackChannel(tt.candidates, tt.failingID, tt.health)
			if gotFound != tt.wantFound {
				t.Fatalf("found = %v, want %v", gotFound, tt.wantFound)
			}
			if gotFound && gotID != tt.wantID {
				t.Errorf("id = %d, want %d", gotID, tt.wantID)
			}
		})
	}
}

func TestSelectFallbackChannel_Deterministic(t *testing.T) {
	t.Parallel()
	candidates := []models.NotifyChannel{
		{ID: 7, Enabled: true},
		{ID: 3, Enabled: true},
		{ID: 5, Enabled: true},
	}
	for i := 0; i < 5; i++ {
		id, found := SelectFallbackChannel(candidates, 1, nil)
		if !found || id != 3 {
			t.Fatalf("run %d: id=%d found=%v, want 3/true every time", i, id, found)
		}
	}
}
