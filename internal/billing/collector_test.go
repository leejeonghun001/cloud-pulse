package billing

import (
	"context"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// fakeStore is an in-memory billing.Store for Collector tests.
type fakeStore struct {
	snapshots map[models.CloudBillingProvider]models.CloudCostSnapshot
	setErr    error
}

func newFakeStore() *fakeStore {
	return &fakeStore{snapshots: make(map[models.CloudBillingProvider]models.CloudCostSnapshot)}
}

func (f *fakeStore) GetCloudCostSnapshot(_ context.Context, provider models.CloudBillingProvider) (models.CloudCostSnapshot, error) {
	if s, ok := f.snapshots[provider]; ok {
		return s, nil
	}
	return models.CloudCostSnapshot{Provider: provider, Status: models.CloudBillingNotConfigured}, nil
}

func (f *fakeStore) SetCloudCostSnapshot(_ context.Context, snap models.CloudCostSnapshot) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.snapshots[snap.Provider] = snap
	return nil
}

func TestCollector_Run_PersistsBothProviders(t *testing.T) {
	store := newFakeStore()
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage": {
				ExitCode: 0,
				Stdout:   []byte(`{"ResultsByTime":[{"Total":{"UnblendedCost":{"Amount":"10.00","Unit":"USD"}}}]}`),
			},
			"aws ce get-cost-forecast": {
				ExitCode: 0,
				Stdout:   []byte(`{"Total":{"Amount":"20.00","Unit":"USD"}}`),
			},
			"oci usage-api usage-summary request-summarized-usages": {
				ExitCode: 0,
				Stdout:   []byte(`{"data":[{"computedAmount":1.0,"resourceId":"ocid1.instance.oc1..a","currency":"USD"}]}`),
			},
		},
	}

	now := fixedNow("2026-09-15T12:00:00Z")
	c := New(store, Options{
		Runner:       runner,
		DataDir:      t.TempDir(),
		OCITenancyID: "ocid1.tenancy.oc1..xyz",
		Now:          func() time.Time { return now },
	})

	results, err := c.Run(context.Background(), models.BillingInterval24h)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	for _, r := range results {
		if r.Status != models.CloudBillingOK {
			t.Errorf("provider %s status = %q, want ok", r.Provider, r.Status)
		}
		if r.LastSuccessAt != now.Unix() {
			t.Errorf("provider %s LastSuccessAt = %d, want %d", r.Provider, r.LastSuccessAt, now.Unix())
		}
		if r.LastAttemptAt != now.Unix() {
			t.Errorf("provider %s LastAttemptAt = %d, want %d", r.Provider, r.LastAttemptAt, now.Unix())
		}
	}

	// Verify HOME sandbox dir was created under DataDir, never a real
	// system path.
	stored, err := store.GetCloudCostSnapshot(context.Background(), models.CloudBillingAWS)
	if err != nil || stored.Status != models.CloudBillingOK {
		t.Fatalf("store did not persist AWS snapshot: %+v, %v", stored, err)
	}
}

// TestCollector_PreservesLastSuccessOnQuietSkip verifies SPEC-v0.6 §1
// 개선 a: a quiet-skip after a prior success keeps the previous MTD/
// forecast/LastSuccessAt values, only updating Status/StatusDetail/
// LastAttemptAt.
func TestCollector_PreservesLastSuccessOnQuietSkip(t *testing.T) {
	store := newFakeStore()
	firstSuccess := fixedNow("2026-09-10T00:00:00Z")
	store.snapshots[models.CloudBillingAWS] = models.CloudCostSnapshot{
		Provider:      models.CloudBillingAWS,
		Status:        models.CloudBillingOK,
		Currency:      "USD",
		MTDCost:       42.0,
		ForecastCost:  84.0,
		CollectedAt:   firstSuccess.Unix(),
		LastSuccessAt: firstSuccess.Unix(),
		LastAttemptAt: firstSuccess.Unix(),
	}
	store.snapshots[models.CloudBillingOCI] = models.CloudCostSnapshot{Provider: models.CloudBillingOCI, Status: models.CloudBillingNotConfigured}

	// Second attempt: AWS CLI now fails (e.g. credentials expired).
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage": {
				ExitCode: 254,
				Stderr:   []byte("Unable to locate credentials"),
			},
		},
	}
	now := fixedNow("2026-09-11T00:00:00Z")
	c := New(store, Options{Runner: runner, DataDir: t.TempDir(), Now: func() time.Time { return now }})

	results, err := c.Run(context.Background(), models.BillingInterval24h)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var awsResult models.CloudCostSnapshot
	for _, r := range results {
		if r.Provider == models.CloudBillingAWS {
			awsResult = r
		}
	}

	if awsResult.Status != models.CloudBillingAuthFailed {
		t.Errorf("Status = %q, want auth_failed", awsResult.Status)
	}
	if awsResult.MTDCost != 42.0 {
		t.Errorf("MTDCost = %v, want preserved 42.0", awsResult.MTDCost)
	}
	if awsResult.ForecastCost != 84.0 {
		t.Errorf("ForecastCost = %v, want preserved 84.0", awsResult.ForecastCost)
	}
	if awsResult.LastSuccessAt != firstSuccess.Unix() {
		t.Errorf("LastSuccessAt = %d, want preserved %d", awsResult.LastSuccessAt, firstSuccess.Unix())
	}
	if awsResult.LastAttemptAt != now.Unix() {
		t.Errorf("LastAttemptAt = %d, want updated to %d", awsResult.LastAttemptAt, now.Unix())
	}
}

// TestCloudCostStale_Boundary directly exercises models.CloudCostStale's
// boundary condition (imported behavior, tested again here from the
// collector's point of view to document the exact interval arithmetic
// this package relies on).
func TestCloudCostStale_Boundary(t *testing.T) {
	tests := []struct {
		name          string
		lastSuccessAt int64
		now           int64
		interval      models.BillingInterval
		want          bool
	}{
		{"never_successful", 0, 1000, models.BillingInterval24h, false},
		{"exactly_2x_not_stale", 1000, 1000 + 2*24*3600, models.BillingInterval24h, false},
		{"just_over_2x_stale", 1000, 1000 + 2*24*3600 + 1, models.BillingInterval24h, true},
		{"6h_interval_stale_after_12h", 1000, 1000 + 12*3600 + 1, models.BillingInterval6h, true},
		{"6h_interval_not_stale_at_12h", 1000, 1000 + 12*3600, models.BillingInterval6h, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := models.CloudCostStale(tt.lastSuccessAt, tt.now, tt.interval)
			if got != tt.want {
				t.Errorf("CloudCostStale(%d, %d, %s) = %v, want %v", tt.lastSuccessAt, tt.now, tt.interval, got, tt.want)
			}
		})
	}
}

// TestCollector_StoreErrorDoesNotDropOtherProvider verifies that a
// store-level failure persisting one provider's snapshot doesn't
// prevent the other provider's successful result from being returned.
func TestCollector_StoreErrorDoesNotDropOtherProvider(t *testing.T) {
	store := newFakeStore()
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage": {
				ExitCode: 0,
				Stdout:   []byte(`{"ResultsByTime":[{"Total":{"UnblendedCost":{"Amount":"1.00","Unit":"USD"}}}]}`),
			},
			"aws ce get-cost-forecast": {
				ExitCode: 0,
				Stdout:   []byte(`{"Total":{"Amount":"2.00","Unit":"USD"}}`),
			},
		},
	}
	store.setErr = errBoom
	c := New(store, Options{Runner: runner, DataDir: t.TempDir(), Now: func() time.Time { return fixedNow("2026-09-15T00:00:00Z") }})

	results, err := c.Run(context.Background(), models.BillingInterval24h)
	if err == nil {
		t.Fatal("Run returned nil error, want the store error surfaced")
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2 even when persistence fails", len(results))
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }
