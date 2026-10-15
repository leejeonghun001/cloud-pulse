package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
)

// BenchmarkScaleReadHandlers is a repeatable read-path profile for a
// representative fleet: 300 hosts with 24 hourly samples, 50 rules, and 500
// active events. Run with -benchtime=5x when comparing storage/query changes.
func BenchmarkScaleReadHandlers(b *testing.B) {
	db := seedScaleProfileDB(b)
	b.Cleanup(func() { _ = db.Close() }) // best-effort close of benchmark fixture
	opts := testOptions()
	opts.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	s := New(opts, db, nil, nil, nil, testLogger())
	h := s.Handler()

	paths := []string{
		"/api/v1/hosts",
		"/api/v1/alerts/active",
		"/api/v1/egress",
		"/api/v1/buckets",
		"/api/v1/billing",
		"/api/v1/storage/accounts",
	}
	// Inventory lookups for the fleet listing: the former per-host query
	// pattern versus the single batched query the handler now uses.
	b.Run("inventory/per-host-queries", func(b *testing.B) {
		ctx := context.Background()
		for b.Loop() {
			for host := 0; host < 300; host++ {
				if _, err := db.GetHostInventory(ctx, fmt.Sprintf("scale-%03d", host)); err != nil {
					b.Fatalf("get inventory: %v", err)
				}
			}
		}
	})
	b.Run("inventory/batched", func(b *testing.B) {
		ctx := context.Background()
		for b.Loop() {
			if _, err := db.ListHostInventoryCounts(ctx); err != nil {
				b.Fatalf("list inventories: %v", err)
			}
		}
	})
	for _, endpoint := range paths {
		b.Run(endpoint, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				req := httptest.NewRequest(http.MethodGet, endpoint, nil)
				req.RemoteAddr = "203.0.113.1:1234"
				req.Header.Set("Authorization", "Bearer "+opts.UIToken)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					b.Fatalf("GET %s status = %d: %s", endpoint, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

func seedScaleProfileDB(b *testing.B) *storage.DB {
	b.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(b.TempDir(), "scale.db"))
	if err != nil {
		b.Fatalf("open scale DB: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	for host := 0; host < 300; host++ {
		id := fmt.Sprintf("scale-%03d", host)
		if err := db.UpsertHost(ctx, models.HostInfo{ID: id, Hostname: id, OS: "linux", Platform: "linux", Arch: "amd64", CPUCores: 4}, now.Unix()); err != nil {
			b.Fatalf("upsert host: %v", err)
		}
		samples := make([]models.Sample, 24)
		for hour := range samples {
			samples[hour] = models.Sample{Timestamp: now.Add(-time.Duration(23-hour) * time.Hour).Unix(), CPUPercent: 42, NetTxBytes: uint64(host + hour + 1), NetRxBytes: uint64(host + hour + 1)}
		}
		if _, err := db.InsertSamples(ctx, id, samples); err != nil {
			b.Fatalf("insert samples: %v", err)
		}
		inv := models.Inventory{CollectedAt: now.Unix()}
		for c := 0; c < 10; c++ {
			inv.Docker.Containers = append(inv.Docker.Containers, models.Container{Name: fmt.Sprintf("svc-%d", c), State: "running"})
		}
		for p := 0; p < 8; p++ {
			inv.Ports = append(inv.Ports, models.ListeningPort{Port: 8000 + p, Proto: "tcp"})
		}
		if err := db.SetHostInventory(ctx, id, inv); err != nil {
			b.Fatalf("set inventory: %v", err)
		}
	}
	for i := 0; i < 50; i++ {
		if _, err := db.CreateAlertRule(ctx, models.AlertRule{Name: fmt.Sprintf("rule-%d", i), Enabled: true, Metric: models.AlertMetricCPU, Operator: models.AlertOperatorGT, Threshold: 80}); err != nil {
			b.Fatalf("create rule: %v", err)
		}
	}
	for i := 0; i < 500; i++ {
		if _, err := db.CreateAlertEvent(ctx, models.AlertEvent{RuleID: int64(i%50 + 1), RuleName: "scale rule", HostID: fmt.Sprintf("scale-%03d", i%300), Hostname: "scale host", Metric: models.AlertMetricCPU, State: models.AlertEventFiring, Value: 95, Threshold: 80, StartedAt: now.Add(-time.Duration(i) * time.Minute).Unix()}); err != nil {
			b.Fatalf("create event: %v", err)
		}
	}
	buckets := make([]models.BucketStats, 20)
	for i := range buckets {
		buckets[i] = models.BucketStats{Provider: models.StorageS3, Bucket: fmt.Sprintf("bucket-%d", i), CollectedAt: now.Unix(), SizeBytes: uint64(i + 1)}
	}
	if err := db.SaveBucketStats(ctx, buckets); err != nil {
		b.Fatalf("save buckets: %v", err)
	}
	for i := 0; i < 5; i++ {
		account, err := db.CreateStorageAccount(ctx, models.StorageAccount{Provider: models.StorageAccountGoogleDrive, Name: fmt.Sprintf("account-%d", i)})
		if err != nil {
			b.Fatalf("create storage account: %v", err)
		}
		if err := db.SetStorageAccountSnapshot(ctx, models.StorageAccountSnapshot{AccountID: account.ID, Status: models.StorageAccountOK, CollectedAt: now.Unix(), LastSuccessAt: now.Unix(), LastAttemptAt: now.Unix()}); err != nil {
			b.Fatalf("set storage snapshot: %v", err)
		}
	}
	return db
}
