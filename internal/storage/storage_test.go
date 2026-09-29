package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// storeContract is a local copy of internal/hub's Store interface, used
// only to lock DB's method set at compile time. It must be kept in sync
// with the SPEC's "Hub interfaces" section; internal/hub defines the real
// interface independently and is expected to match.
type storeContract interface {
	UpsertHost(ctx context.Context, h models.HostInfo, seenAt int64) error
	InsertSamples(ctx context.Context, hostID string, samples []models.Sample) (int, error)
	ListHosts(ctx context.Context) ([]models.HostRecord, error)
	GetHost(ctx context.Context, id string) (models.HostRecord, error)
	QuerySeries(ctx context.Context, hostID string, from, to int64) (models.Series, error)
	ListEgress(ctx context.Context, month string) ([]models.EgressRecord, error)
	SaveBucketStats(ctx context.Context, stats []models.BucketStats) error
	LatestBuckets(ctx context.Context) ([]models.BucketStats, error)
	BucketHistory(ctx context.Context, provider models.StorageProvider, bucket string, since int64) ([]models.BucketPoint, error)
	MarkAlertSent(ctx context.Context, hostID, month string, dir models.Direction, level models.EgressLevel) (bool, error)
	GetHostLimits(ctx context.Context, hostID string) (models.HostLimits, error)
	ListHostLimits(ctx context.Context) ([]models.HostLimits, error)
	SetHostLimits(ctx context.Context, l models.HostLimits) error
	GetSetting(ctx context.Context, key string) (value string, ok bool, err error)
	SetSetting(ctx context.Context, key, value string) error
	Rollup(ctx context.Context, now time.Time) error
	Prune(ctx context.Context, now time.Time) error
	Close() error
}

var _ storeContract = (*DB)(nil)

// openTestDB opens a DB backed by a temp file and registers cleanup to
// close it before the test ends (required on Windows to release file
// locks).
func openTestDB(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "cloud-pulse.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

func sampleAt(ts int64) models.Sample {
	return models.Sample{
		Timestamp:       ts,
		CPUPercent:      10.5,
		Load1:           0.1,
		Load5:           0.2,
		Load15:          0.3,
		MemTotal:        1000,
		MemAvailable:    400,
		MemUsed:         600,
		MemUsedPercent:  60,
		SwapTotal:       100,
		SwapUsed:        10,
		DiskTotal:       2000,
		DiskUsed:        500,
		DiskUsedPercent: 25,
		DiskReadBps:     1.0,
		DiskWriteBps:    2.0,
		NetRxBps:        3.0,
		NetTxBps:        4.0,
		NetRxBytes:      100,
		NetTxBytes:      200,
		UptimeSeconds:   3600,
	}
}

func TestOpen_MigrationsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cloud-pulse.db")

	db1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db2.Close(); err != nil {
			t.Errorf("close second: %v", err)
		}
	})

	// The schema must still be usable: a host upsert followed by a get
	// exercises the tables created by the migration.
	if err := db2.UpsertHost(ctx, models.HostInfo{ID: "h1"}, 1000); err != nil {
		t.Fatalf("UpsertHost after reopen: %v", err)
	}
	rec, err := db2.GetHost(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHost after reopen: %v", err)
	}
	if rec.Info.ID != "h1" {
		t.Fatalf("got host id %q, want h1", rec.Info.ID)
	}
}

func TestUpsertHost_LastSeenMax(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	info := models.HostInfo{ID: "h1", Hostname: "alpha"}
	if err := db.UpsertHost(ctx, info, 1000); err != nil {
		t.Fatalf("UpsertHost 1: %v", err)
	}
	// Older seenAt must not move last_seen backwards.
	if err := db.UpsertHost(ctx, info, 500); err != nil {
		t.Fatalf("UpsertHost 2: %v", err)
	}
	rec, err := db.GetHost(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if rec.LastSeen != 1000 {
		t.Fatalf("last_seen = %d, want 1000 (must not regress)", rec.LastSeen)
	}

	info.Hostname = "beta"
	if err := db.UpsertHost(ctx, info, 2000); err != nil {
		t.Fatalf("UpsertHost 3: %v", err)
	}
	rec, err = db.GetHost(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHost 2: %v", err)
	}
	if rec.LastSeen != 2000 {
		t.Fatalf("last_seen = %d, want 2000", rec.LastSeen)
	}
	if rec.Info.Hostname != "beta" {
		t.Fatalf("hostname = %q, want beta", rec.Info.Hostname)
	}
}

func TestGetHost_NotFound(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	_, err := db.GetHost(ctx, "missing")
	if !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetHost error = %v, want wrapping models.ErrNotFound", err)
	}
}

func TestListHosts_SortedByID(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	for _, id := range []string{"charlie", "alpha", "bravo"} {
		if err := db.UpsertHost(ctx, models.HostInfo{ID: id}, 1); err != nil {
			t.Fatalf("UpsertHost(%s): %v", id, err)
		}
	}

	recs, err := db.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d hosts, want 3", len(recs))
	}
	want := []string{"alpha", "bravo", "charlie"}
	for i, w := range want {
		if recs[i].Info.ID != w {
			t.Fatalf("recs[%d].ID = %q, want %q", i, recs[i].Info.ID, w)
		}
	}
}

func TestInsertSamples_DuplicatesAndLatest(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.UpsertHost(ctx, models.HostInfo{ID: "h1"}, 1); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	s1 := sampleAt(1000)
	s2 := sampleAt(1015)
	n, err := db.InsertSamples(ctx, "h1", []models.Sample{s1, s2})
	if err != nil {
		t.Fatalf("InsertSamples: %v", err)
	}
	if n != 2 {
		t.Fatalf("inserted = %d, want 2", n)
	}

	// Re-inserting the same batch must be fully ignored.
	n, err = db.InsertSamples(ctx, "h1", []models.Sample{s1, s2})
	if err != nil {
		t.Fatalf("InsertSamples dup: %v", err)
	}
	if n != 0 {
		t.Fatalf("dup inserted = %d, want 0", n)
	}

	// An older sample than the current latest must not become "latest".
	older := sampleAt(500)
	n, err = db.InsertSamples(ctx, "h1", []models.Sample{older})
	if err != nil {
		t.Fatalf("InsertSamples older: %v", err)
	}
	if n != 1 {
		t.Fatalf("older inserted = %d, want 1", n)
	}

	rec, err := db.GetHost(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if rec.Latest == nil || rec.Latest.Timestamp != 1015 {
		t.Fatalf("latest = %+v, want ts=1015", rec.Latest)
	}

	// A genuinely newer sample must become "latest".
	newer := sampleAt(2000)
	if _, err := db.InsertSamples(ctx, "h1", []models.Sample{newer}); err != nil {
		t.Fatalf("InsertSamples newer: %v", err)
	}
	rec, err = db.GetHost(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHost 2: %v", err)
	}
	if rec.Latest == nil || rec.Latest.Timestamp != 2000 {
		t.Fatalf("latest = %+v, want ts=2000", rec.Latest)
	}
}

func TestInsertSamples_EgressAcrossMonthBoundary(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.UpsertHost(ctx, models.HostInfo{ID: "h1"}, 1); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	// 2026-09-30T23:59:50Z and 2026-10-01T00:00:05Z straddle a month
	// boundary; egress must be attributed to different months.
	sepTS := time.Date(2026, 9, 30, 23, 59, 50, 0, time.UTC).Unix()
	octTS := time.Date(2026, 10, 1, 0, 0, 5, 0, time.UTC).Unix()

	sSep := sampleAt(sepTS)
	sSep.NetTxBytes, sSep.NetRxBytes = 1000, 100
	sOct := sampleAt(octTS)
	sOct.NetTxBytes, sOct.NetRxBytes = 2000, 200

	if _, err := db.InsertSamples(ctx, "h1", []models.Sample{sSep, sOct}); err != nil {
		t.Fatalf("InsertSamples: %v", err)
	}

	sepRecords, err := db.ListEgress(ctx, "2026-09")
	if err != nil {
		t.Fatalf("ListEgress sep: %v", err)
	}
	if len(sepRecords) != 1 || sepRecords[0].TxBytes != 1000 || sepRecords[0].RxBytes != 100 {
		t.Fatalf("sep egress = %+v, want tx=1000 rx=100", sepRecords)
	}

	octRecords, err := db.ListEgress(ctx, "2026-10")
	if err != nil {
		t.Fatalf("ListEgress oct: %v", err)
	}
	if len(octRecords) != 1 || octRecords[0].TxBytes != 2000 || octRecords[0].RxBytes != 200 {
		t.Fatalf("oct egress = %+v, want tx=2000 rx=200", octRecords)
	}

	// Re-inserting the same batch (duplicate) must NOT double-count
	// egress in either month.
	if _, err := db.InsertSamples(ctx, "h1", []models.Sample{sSep, sOct}); err != nil {
		t.Fatalf("InsertSamples dup: %v", err)
	}
	sepRecords, err = db.ListEgress(ctx, "2026-09")
	if err != nil {
		t.Fatalf("ListEgress sep 2: %v", err)
	}
	if sepRecords[0].TxBytes != 1000 {
		t.Fatalf("sep egress after dup = %d, want unchanged 1000", sepRecords[0].TxBytes)
	}
	octRecords, err = db.ListEgress(ctx, "2026-10")
	if err != nil {
		t.Fatalf("ListEgress oct 2: %v", err)
	}
	if octRecords[0].TxBytes != 2000 {
		t.Fatalf("oct egress after dup = %d, want unchanged 2000", octRecords[0].TxBytes)
	}
}

func TestListEgress_SortedByHostID(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	month := "2026-05"
	ts := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	for _, id := range []string{"zeta", "alpha"} {
		if err := db.UpsertHost(ctx, models.HostInfo{ID: id}, 1); err != nil {
			t.Fatalf("UpsertHost(%s): %v", id, err)
		}
		s := sampleAt(ts)
		if _, err := db.InsertSamples(ctx, id, []models.Sample{s}); err != nil {
			t.Fatalf("InsertSamples(%s): %v", id, err)
		}
	}

	recs, err := db.ListEgress(ctx, month)
	if err != nil {
		t.Fatalf("ListEgress: %v", err)
	}
	if len(recs) != 2 || recs[0].HostID != "alpha" || recs[1].HostID != "zeta" {
		t.Fatalf("recs = %+v, want [alpha, zeta]", recs)
	}
}

func TestQuerySeries_TierSelectionAndRollup(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.UpsertHost(ctx, models.HostInfo{ID: "h1"}, 1); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	// Insert two raw samples 15s apart inside the same 5m bucket so
	// Rollup produces a deterministic average.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	s1 := sampleAt(base)
	s1.CPUPercent = 10
	s2 := sampleAt(base + 15)
	s2.CPUPercent = 20

	if _, err := db.InsertSamples(ctx, "h1", []models.Sample{s1, s2}); err != nil {
		t.Fatalf("InsertSamples: %v", err)
	}

	now := time.Unix(base+15, 0)
	if err := db.Rollup(ctx, now); err != nil {
		t.Fatalf("Rollup: %v", err)
	}

	// Raw tier: range <= 6h.
	raw, err := db.QuerySeries(ctx, "h1", base, base+15)
	if err != nil {
		t.Fatalf("QuerySeries raw: %v", err)
	}
	if raw.Resolution != models.ResolutionRaw {
		t.Fatalf("raw resolution = %d, want %d", raw.Resolution, models.ResolutionRaw)
	}
	if raw.Len() != 2 {
		t.Fatalf("raw len = %d, want 2", raw.Len())
	}
	if raw.Timestamps[0] != base || raw.Timestamps[1] != base+15 {
		t.Fatalf("raw timestamps = %v, want ordered [%d %d]", raw.Timestamps, base, base+15)
	}

	// 5m tier: range > 6h and <= 7d. Average CPU of 10 and 20 is 15.
	from5m := base - int64(23*time.Hour.Seconds())
	to5m := base + int64(15*time.Hour.Seconds())
	fiveM, err := db.QuerySeries(ctx, "h1", from5m, to5m)
	if err != nil {
		t.Fatalf("QuerySeries 5m: %v", err)
	}
	if fiveM.Resolution != models.Resolution5m {
		t.Fatalf("5m resolution = %d, want %d", fiveM.Resolution, models.Resolution5m)
	}
	if fiveM.Len() != 1 {
		t.Fatalf("5m len = %d, want 1", fiveM.Len())
	}
	if fiveM.CPU[0] != 15 {
		t.Fatalf("5m CPU avg = %v, want 15", fiveM.CPU[0])
	}

	// 1h tier: range > 7d. Rollup only re-derives 1h from a 6h trailing
	// window of 5m data relative to `now`, so query relative to now too.
	if err := db.Rollup(ctx, now); err != nil {
		t.Fatalf("Rollup 2: %v", err)
	}
	from1h := now.Unix() - int64(8*24*time.Hour.Seconds())
	to1h := now.Unix() + int64(1*24*time.Hour.Seconds())
	oneH, err := db.QuerySeries(ctx, "h1", from1h, to1h)
	if err != nil {
		t.Fatalf("QuerySeries 1h: %v", err)
	}
	if oneH.Resolution != models.Resolution1h {
		t.Fatalf("1h resolution = %d, want %d", oneH.Resolution, models.Resolution1h)
	}
	if oneH.Len() != 1 {
		t.Fatalf("1h len = %d, want 1", oneH.Len())
	}
	if oneH.CPU[0] != 15 {
		t.Fatalf("1h CPU avg = %v, want 15", oneH.CPU[0])
	}
}

func TestQuerySeries_EmptyNotNull(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	series, err := db.QuerySeries(ctx, "missing-host", 0, 100)
	if err != nil {
		t.Fatalf("QuerySeries: %v", err)
	}
	if series.Timestamps == nil || series.CPU == nil {
		t.Fatalf("empty series has nil slices: %+v", series)
	}
	if series.Len() != 0 {
		t.Fatalf("len = %d, want 0", series.Len())
	}
}

func TestRollup_Idempotent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.UpsertHost(ctx, models.HostInfo{ID: "h1"}, 1); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).Unix()
	samples := []models.Sample{sampleAt(base), sampleAt(base + 15), sampleAt(base + 30)}
	samples[0].CPUPercent, samples[0].NetTxBytes = 10, 100
	samples[1].CPUPercent, samples[1].NetTxBytes = 20, 200
	samples[2].CPUPercent, samples[2].NetTxBytes = 30, 300

	if _, err := db.InsertSamples(ctx, "h1", samples); err != nil {
		t.Fatalf("InsertSamples: %v", err)
	}

	now := time.Unix(base+30, 0)
	if err := db.Rollup(ctx, now); err != nil {
		t.Fatalf("Rollup 1: %v", err)
	}
	first, err := db.QuerySeries(ctx, "h1", base-int64(23*time.Hour.Seconds()), base+int64(15*time.Hour.Seconds()))
	if err != nil {
		t.Fatalf("QuerySeries 1: %v", err)
	}

	if err := db.Rollup(ctx, now); err != nil {
		t.Fatalf("Rollup 2: %v", err)
	}
	second, err := db.QuerySeries(ctx, "h1", base-int64(23*time.Hour.Seconds()), base+int64(15*time.Hour.Seconds()))
	if err != nil {
		t.Fatalf("QuerySeries 2: %v", err)
	}

	if first.Len() != second.Len() || first.Len() != 1 {
		t.Fatalf("lens = %d, %d, want both 1", first.Len(), second.Len())
	}
	if first.CPU[0] != second.CPU[0] || first.NetTx[0] != second.NetTx[0] {
		t.Fatalf("rollup not idempotent: first=%+v second=%+v", first, second)
	}
	if first.CPU[0] != 20 {
		t.Fatalf("CPU avg = %v, want 20", first.CPU[0])
	}
}

func TestPrune_RemovesOldRawKeeps5m(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.UpsertHost(ctx, models.HostInfo{ID: "h1"}, 1); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	oldTS := now.Add(-48 * time.Hour).Unix() // older than 26h raw retention

	if _, err := db.InsertSamples(ctx, "h1", []models.Sample{sampleAt(oldTS)}); err != nil {
		t.Fatalf("InsertSamples: %v", err)
	}
	if err := db.Rollup(ctx, now.Add(-47*time.Hour)); err != nil {
		t.Fatalf("Rollup: %v", err)
	}

	// Sanity: rolled-up 5m point exists before Prune via a wide query.
	before, err := db.QuerySeries(ctx, "h1", oldTS-3600, oldTS+3600)
	if err != nil {
		t.Fatalf("QuerySeries before: %v", err)
	}
	if before.Len() != 1 {
		t.Fatalf("before prune len = %d, want 1 (raw tier)", before.Len())
	}

	if err := db.Prune(ctx, now); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	afterRaw, err := db.QuerySeries(ctx, "h1", oldTS-3600, oldTS+3600)
	if err != nil {
		t.Fatalf("QuerySeries after raw: %v", err)
	}
	if afterRaw.Len() != 0 {
		t.Fatalf("raw rows survived prune: len = %d", afterRaw.Len())
	}

	from5m := oldTS - int64(23*time.Hour.Seconds())
	to5m := oldTS + int64(15*time.Hour.Seconds())
	after5m, err := db.QuerySeries(ctx, "h1", from5m, to5m)
	if err != nil {
		t.Fatalf("QuerySeries after 5m: %v", err)
	}
	if after5m.Len() != 1 {
		t.Fatalf("5m rows did not survive prune: len = %d", after5m.Len())
	}
}

func TestBucketStats_LatestAndHistory(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC).Unix()
	stats := []models.BucketStats{
		{Provider: models.StorageS3, Bucket: "b1", CollectedAt: base, SizeBytes: 100},
		{Provider: models.StorageS3, Bucket: "b1", CollectedAt: base + 3600, SizeBytes: 200},
		{Provider: models.StorageS3, Bucket: "b2", CollectedAt: base, SizeBytes: 50},
		{Provider: models.StorageR2, Bucket: "b1", CollectedAt: base, SizeBytes: 10},
	}
	if err := db.SaveBucketStats(ctx, stats); err != nil {
		t.Fatalf("SaveBucketStats: %v", err)
	}

	latest, err := db.LatestBuckets(ctx)
	if err != nil {
		t.Fatalf("LatestBuckets: %v", err)
	}
	if len(latest) != 3 {
		t.Fatalf("latest count = %d, want 3", len(latest))
	}
	// Sorted by provider then bucket: r2/b1, s3/b1, s3/b2.
	if latest[0].Provider != models.StorageR2 || latest[0].Bucket != "b1" {
		t.Fatalf("latest[0] = %+v, want r2/b1", latest[0])
	}
	if latest[1].Provider != models.StorageS3 || latest[1].Bucket != "b1" || latest[1].SizeBytes != 200 {
		t.Fatalf("latest[1] = %+v, want s3/b1 size=200 (most recent)", latest[1])
	}
	if latest[2].Provider != models.StorageS3 || latest[2].Bucket != "b2" {
		t.Fatalf("latest[2] = %+v, want s3/b2", latest[2])
	}

	hist, err := db.BucketHistory(ctx, models.StorageS3, "b1", base)
	if err != nil {
		t.Fatalf("BucketHistory: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("history count = %d, want 2", len(hist))
	}
	if hist[0].Timestamp != base || hist[1].Timestamp != base+3600 {
		t.Fatalf("history not ascending: %+v", hist)
	}
}

func TestMarkAlertSent_FirstThenSecond(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	first, err := db.MarkAlertSent(ctx, "h1", "2026-01", models.DirectionOut, models.EgressWarning)
	if err != nil {
		t.Fatalf("MarkAlertSent 1: %v", err)
	}
	if !first {
		t.Fatalf("first call: first = false, want true")
	}

	second, err := db.MarkAlertSent(ctx, "h1", "2026-01", models.DirectionOut, models.EgressWarning)
	if err != nil {
		t.Fatalf("MarkAlertSent 2: %v", err)
	}
	if second {
		t.Fatalf("second call: first = true, want false")
	}

	// A different level for the same host/month/direction is independent.
	third, err := db.MarkAlertSent(ctx, "h1", "2026-01", models.DirectionOut, models.EgressCritical)
	if err != nil {
		t.Fatalf("MarkAlertSent 3: %v", err)
	}
	if !third {
		t.Fatalf("different level: first = false, want true")
	}
}

func TestMarkAlertSent_DirectionIndependence(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	// Same host/month/level but opposite directions must be tracked
	// independently: marking "out" must not affect "in".
	firstOut, err := db.MarkAlertSent(ctx, "h1", "2026-02", models.DirectionOut, models.EgressWarning)
	if err != nil {
		t.Fatalf("MarkAlertSent out: %v", err)
	}
	if !firstOut {
		t.Fatalf("first out call: first = false, want true")
	}

	firstIn, err := db.MarkAlertSent(ctx, "h1", "2026-02", models.DirectionIn, models.EgressWarning)
	if err != nil {
		t.Fatalf("MarkAlertSent in: %v", err)
	}
	if !firstIn {
		t.Fatalf("first in call: first = false, want true (direction must be independent of out)")
	}

	secondOut, err := db.MarkAlertSent(ctx, "h1", "2026-02", models.DirectionOut, models.EgressWarning)
	if err != nil {
		t.Fatalf("MarkAlertSent out again: %v", err)
	}
	if secondOut {
		t.Fatalf("second out call: first = true, want false")
	}

	secondIn, err := db.MarkAlertSent(ctx, "h1", "2026-02", models.DirectionIn, models.EgressWarning)
	if err != nil {
		t.Fatalf("MarkAlertSent in again: %v", err)
	}
	if secondIn {
		t.Fatalf("second in call: first = true, want false")
	}
}

func TestHostLimits_CRUD(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	// No row yet: GetHostLimits returns a zero-value HostLimits with the
	// requested HostID and nil pointers, not an error.
	got, err := db.GetHostLimits(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHostLimits (missing): %v", err)
	}
	if got.HostID != "h1" || got.EgressLimitBytes != nil || got.IngressLimitBytes != nil {
		t.Fatalf("GetHostLimits (missing) = %+v, want HostID=h1 and nil pointers", got)
	}

	egress := uint64(1000)
	ingress := uint64(0) // explicitly unlimited, distinct from nil (no override)
	if err := db.SetHostLimits(ctx, models.HostLimits{
		HostID:            "h1",
		EgressLimitBytes:  &egress,
		IngressLimitBytes: &ingress,
	}); err != nil {
		t.Fatalf("SetHostLimits: %v", err)
	}

	got, err = db.GetHostLimits(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHostLimits: %v", err)
	}
	if got.EgressLimitBytes == nil || *got.EgressLimitBytes != 1000 {
		t.Fatalf("EgressLimitBytes = %v, want 1000", got.EgressLimitBytes)
	}
	if got.IngressLimitBytes == nil || *got.IngressLimitBytes != 0 {
		t.Fatalf("IngressLimitBytes = %v, want pointer to 0 (explicitly unlimited, not nil)", got.IngressLimitBytes)
	}
	if got.UpdatedAt == 0 {
		t.Fatalf("UpdatedAt = 0, want auto-set to current time")
	}

	// Explicit UpdatedAt is preserved as given.
	if err := db.SetHostLimits(ctx, models.HostLimits{
		HostID:            "h1",
		EgressLimitBytes:  &egress,
		IngressLimitBytes: &ingress,
		UpdatedAt:         12345,
	}); err != nil {
		t.Fatalf("SetHostLimits with explicit UpdatedAt: %v", err)
	}
	got, err = db.GetHostLimits(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHostLimits after update: %v", err)
	}
	if got.UpdatedAt != 12345 {
		t.Fatalf("UpdatedAt = %d, want 12345", got.UpdatedAt)
	}

	// Both nil deletes the row, reverting to the no-override state.
	if err := db.SetHostLimits(ctx, models.HostLimits{HostID: "h1"}); err != nil {
		t.Fatalf("SetHostLimits delete: %v", err)
	}
	got, err = db.GetHostLimits(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHostLimits after delete: %v", err)
	}
	if got.EgressLimitBytes != nil || got.IngressLimitBytes != nil {
		t.Fatalf("GetHostLimits after delete = %+v, want nil pointers", got)
	}
}

func TestHostLimits_OnlyOneFieldSet(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	egress := uint64(5000)
	// Only EgressLimitBytes set; IngressLimitBytes stays nil (no
	// override), which must NOT trigger the both-nil delete path.
	if err := db.SetHostLimits(ctx, models.HostLimits{
		HostID:           "h2",
		EgressLimitBytes: &egress,
	}); err != nil {
		t.Fatalf("SetHostLimits: %v", err)
	}

	got, err := db.GetHostLimits(ctx, "h2")
	if err != nil {
		t.Fatalf("GetHostLimits: %v", err)
	}
	if got.EgressLimitBytes == nil || *got.EgressLimitBytes != 5000 {
		t.Fatalf("EgressLimitBytes = %v, want 5000", got.EgressLimitBytes)
	}
	if got.IngressLimitBytes != nil {
		t.Fatalf("IngressLimitBytes = %v, want nil", got.IngressLimitBytes)
	}
}

func TestHostLimits_32BitSafeLargeValues(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	// 1<<62 is the spec's documented ceiling for values that must round-
	// trip through SQLite's signed 64-bit INTEGER without overflow or
	// truncation, including on 32-bit platforms where int/uint default
	// to 32 bits (these are explicitly uint64/int64 typed, but this
	// guards against any accidental narrowing).
	big := uint64(1) << 62
	if err := db.SetHostLimits(ctx, models.HostLimits{
		HostID:            "h3",
		EgressLimitBytes:  &big,
		IngressLimitBytes: &big,
	}); err != nil {
		t.Fatalf("SetHostLimits: %v", err)
	}

	got, err := db.GetHostLimits(ctx, "h3")
	if err != nil {
		t.Fatalf("GetHostLimits: %v", err)
	}
	if got.EgressLimitBytes == nil || *got.EgressLimitBytes != big {
		t.Fatalf("EgressLimitBytes = %v, want %d", got.EgressLimitBytes, big)
	}
	if got.IngressLimitBytes == nil || *got.IngressLimitBytes != big {
		t.Fatalf("IngressLimitBytes = %v, want %d", got.IngressLimitBytes, big)
	}
}

func TestListHostLimits_SortedByHostID(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	e1, e2, e3 := uint64(100), uint64(200), uint64(300)
	for id, v := range map[string]*uint64{"zeta": &e3, "alpha": &e1, "mid": &e2} {
		if err := db.SetHostLimits(ctx, models.HostLimits{HostID: id, EgressLimitBytes: v}); err != nil {
			t.Fatalf("SetHostLimits(%s): %v", id, err)
		}
	}

	limits, err := db.ListHostLimits(ctx)
	if err != nil {
		t.Fatalf("ListHostLimits: %v", err)
	}
	if len(limits) != 3 {
		t.Fatalf("got %d limits, want 3", len(limits))
	}
	want := []string{"alpha", "mid", "zeta"}
	for i, w := range want {
		if limits[i].HostID != w {
			t.Fatalf("limits[%d].HostID = %q, want %q", i, limits[i].HostID, w)
		}
	}
}

func TestSettings_CRUD(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	// Unset key: ok=false, no error.
	_, ok, err := db.GetSetting(ctx, "alert_webhook_url")
	if err != nil {
		t.Fatalf("GetSetting (missing): %v", err)
	}
	if ok {
		t.Fatalf("GetSetting (missing) ok = true, want false")
	}

	if err := db.SetSetting(ctx, "alert_webhook_url", "https://example.com/hook"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	value, ok, err := db.GetSetting(ctx, "alert_webhook_url")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if !ok || value != "https://example.com/hook" {
		t.Fatalf("GetSetting = (%q, %v), want (https://example.com/hook, true)", value, ok)
	}

	// Overwriting replaces the value.
	if err := db.SetSetting(ctx, "alert_webhook_url", "https://example.com/other"); err != nil {
		t.Fatalf("SetSetting overwrite: %v", err)
	}
	value, ok, err = db.GetSetting(ctx, "alert_webhook_url")
	if err != nil {
		t.Fatalf("GetSetting after overwrite: %v", err)
	}
	if !ok || value != "https://example.com/other" {
		t.Fatalf("GetSetting after overwrite = (%q, %v), want (https://example.com/other, true)", value, ok)
	}

	// Empty value deletes the setting.
	if err := db.SetSetting(ctx, "alert_webhook_url", ""); err != nil {
		t.Fatalf("SetSetting empty: %v", err)
	}
	_, ok, err = db.GetSetting(ctx, "alert_webhook_url")
	if err != nil {
		t.Fatalf("GetSetting after delete: %v", err)
	}
	if ok {
		t.Fatalf("GetSetting after delete ok = true, want false")
	}
}

func TestSettings_KeysIndependent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.SetSetting(ctx, "key_a", "value_a"); err != nil {
		t.Fatalf("SetSetting key_a: %v", err)
	}
	if err := db.SetSetting(ctx, "key_b", "value_b"); err != nil {
		t.Fatalf("SetSetting key_b: %v", err)
	}

	// Deleting one key must not affect the other.
	if err := db.SetSetting(ctx, "key_a", ""); err != nil {
		t.Fatalf("SetSetting delete key_a: %v", err)
	}
	_, ok, err := db.GetSetting(ctx, "key_a")
	if err != nil {
		t.Fatalf("GetSetting key_a: %v", err)
	}
	if ok {
		t.Fatalf("key_a ok = true after delete, want false")
	}
	value, ok, err := db.GetSetting(ctx, "key_b")
	if err != nil {
		t.Fatalf("GetSetting key_b: %v", err)
	}
	if !ok || value != "value_b" {
		t.Fatalf("key_b = (%q, %v), want (value_b, true)", value, ok)
	}
}

// TestOpen_UpgradeFrom0001PreservesAlertsSent verifies migration 0002's
// alerts_sent rebuild: a database created with only migration 0001
// applied (schema_migrations has only version 1, and an alerts_sent row
// exists in the pre-0002 schema without a direction column) must, after
// storage.Open applies 0002, end up with that row preserved under
// direction 'out'.
func TestOpen_UpgradeFrom0001PreservesAlertsSent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build a database that has gone through exactly migration 0001,
	// bypassing the embedded-migration runner so we control the schema
	// version precisely (simulating a real v0.1 database on disk).
	legacyDSN := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	legacy, err := sql.Open("sqlite", legacyDSN)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	migration0001, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		_ = legacy.Close()
		t.Fatalf("read 0001 migration: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, string(migration0001)); err != nil {
		_ = legacy.Close()
		t.Fatalf("apply 0001 migration: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		_ = legacy.Close()
		t.Fatalf("create schema_migrations: %v", err)
	}
	if _, err := legacy.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (1, ?)`, time.Now().Unix(),
	); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert schema_migrations row: %v", err)
	}
	// Pre-0002 alerts_sent has no direction column at all.
	if _, err := legacy.ExecContext(ctx,
		`INSERT INTO alerts_sent (host_id, month, level, sent_at) VALUES ('h1', '2026-01', 'warning', 1000)`,
	); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy alerts_sent row: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Now open through the normal path: migration 0002 must apply and
	// preserve the row with direction='out'.
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open (upgrade): %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	row := db.sql.QueryRowContext(ctx,
		`SELECT direction, sent_at FROM alerts_sent WHERE host_id = ? AND month = ? AND level = ?`,
		"h1", "2026-01", "warning",
	)
	var direction string
	var sentAt int64
	if err := row.Scan(&direction, &sentAt); err != nil {
		t.Fatalf("scan upgraded alerts_sent row: %v", err)
	}
	if direction != "out" {
		t.Fatalf("direction = %q, want %q", direction, "out")
	}
	if sentAt != 1000 {
		t.Fatalf("sent_at = %d, want 1000 (preserved)", sentAt)
	}

	// The MarkAlertSent method must also work post-upgrade, including
	// treating the pre-existing 'out' row as already sent.
	first, err := db.MarkAlertSent(ctx, "h1", "2026-01", models.DirectionOut, models.EgressWarning)
	if err != nil {
		t.Fatalf("MarkAlertSent post-upgrade: %v", err)
	}
	if first {
		t.Fatalf("MarkAlertSent post-upgrade: first = true, want false (row pre-existed)")
	}

	// A second application of migrate() (simulating a second process
	// start) must be a no-op and not error or duplicate rows.
	if err := db.migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	var count int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts_sent`).Scan(&count); err != nil {
		t.Fatalf("count alerts_sent: %v", err)
	}
	if count != 1 {
		t.Fatalf("alerts_sent row count = %d, want 1", count)
	}
}

func TestInsertSamples_ConcurrentGoroutines(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.UpsertHost(ctx, models.HostInfo{ID: "h1"}, 1); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	const goroutines = 4
	const perGoroutine = 20
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Unix()

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			samples := make([]models.Sample, 0, perGoroutine)
			for i := 0; i < perGoroutine; i++ {
				samples = append(samples, sampleAt(base+int64(g*perGoroutine+i)*15))
			}
			if _, err := db.InsertSamples(ctx, "h1", samples); err != nil {
				errs <- fmt.Errorf("goroutine %d: %w", g, err)
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent InsertSamples error: %v", err)
	}

	total := base + int64(goroutines*perGoroutine)*15
	series, err := db.QuerySeries(ctx, "h1", base, total)
	if err != nil {
		t.Fatalf("QuerySeries: %v", err)
	}
	if series.Len() != goroutines*perGoroutine {
		t.Fatalf("total rows = %d, want %d", series.Len(), goroutines*perGoroutine)
	}
}
