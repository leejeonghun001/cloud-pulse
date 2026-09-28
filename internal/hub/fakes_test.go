package hub

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// fakeStore is an in-memory Store implementation for tests. All access
// is guarded by mu so it can be used safely from concurrent goroutines
// (e.g. background scheduler + request handlers).
type fakeStore struct {
	mu sync.Mutex

	hosts   map[string]models.HostRecord
	samples map[string][]models.Sample     // hostID -> samples, sorted by ts ascending
	egress  map[string]models.EgressRecord // key: hostID+"|"+month
	alerts  map[string]bool                // key: hostID+"|"+month+"|"+level
	buckets map[string]models.BucketStats  // key: provider+"|"+bucket
	history map[string][]models.BucketPoint

	rollupErr   error
	pruneErr    error
	rollupCalls int
	pruneCalls  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		hosts:   make(map[string]models.HostRecord),
		samples: make(map[string][]models.Sample),
		egress:  make(map[string]models.EgressRecord),
		alerts:  make(map[string]bool),
		buckets: make(map[string]models.BucketStats),
		history: make(map[string][]models.BucketPoint),
	}
}

func egressKey(hostID, month string) string {
	return hostID + "|" + month
}

func alertKey(hostID, month string, level models.EgressLevel) string {
	return hostID + "|" + month + "|" + string(level)
}

func bucketKey(provider models.StorageProvider, bucket string) string {
	return string(provider) + "|" + bucket
}

func (f *fakeStore) UpsertHost(_ context.Context, h models.HostInfo, seenAt int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.hosts[h.ID]
	if !ok {
		rec = models.HostRecord{}
	}
	rec.Info = h
	rec.LastSeen = seenAt
	f.hosts[h.ID] = rec
	return nil
}

func (f *fakeStore) InsertSamples(_ context.Context, hostID string, samples []models.Sample) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	existing := f.samples[hostID]
	existingTS := make(map[int64]bool, len(existing))
	for _, s := range existing {
		existingTS[s.Timestamp] = true
	}

	inserted := 0
	var latest *models.Sample
	insertedSamples := make([]models.Sample, 0, len(samples))
	for _, s := range samples {
		if existingTS[s.Timestamp] {
			continue
		}
		existingTS[s.Timestamp] = true
		existing = append(existing, s)
		inserted++
		insertedSamples = append(insertedSamples, s)
		if latest == nil || s.Timestamp > latest.Timestamp {
			cp := s
			latest = &cp
		}
	}
	sort.Slice(existing, func(i, j int) bool { return existing[i].Timestamp < existing[j].Timestamp })
	f.samples[hostID] = existing

	if latest != nil {
		rec, ok := f.hosts[hostID]
		if ok {
			if rec.Latest == nil || latest.Timestamp > rec.Latest.Timestamp {
				rec.Latest = latest
				f.hosts[hostID] = rec
			}
		}
	}

	if _, ok := f.hosts[hostID]; ok {
		for _, s := range insertedSamples {
			month := models.MonthOf(time.Unix(s.Timestamp, 0).UTC())
			key := egressKey(hostID, month)
			e := f.egress[key]
			e.HostID = hostID
			e.Month = month
			e.TxBytes += s.NetTxBytes
			e.RxBytes += s.NetRxBytes
			f.egress[key] = e
		}
	}

	return inserted, nil
}

func (f *fakeStore) ListHosts(_ context.Context) ([]models.HostRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.HostRecord, 0, len(f.hosts))
	for _, h := range f.hosts {
		out = append(out, h)
	}
	return out, nil
}

func (f *fakeStore) GetHost(_ context.Context, id string) (models.HostRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.hosts[id]
	if !ok {
		return models.HostRecord{}, models.ErrNotFound
	}
	return rec, nil
}

func (f *fakeStore) QuerySeries(_ context.Context, hostID string, from, to int64) (models.Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := models.ResolutionFor(from, to)
	series := models.NewSeries(hostID, res, from, to)
	for _, s := range f.samples[hostID] {
		if s.Timestamp < from || s.Timestamp > to {
			continue
		}
		series.Append(models.SeriesPoint{
			TS:        s.Timestamp,
			CPU:       s.CPUPercent,
			Mem:       s.MemUsedPercent,
			Disk:      s.DiskUsedPercent,
			NetRx:     s.NetRxBps,
			NetTx:     s.NetTxBps,
			DiskRead:  s.DiskReadBps,
			DiskWrite: s.DiskWriteBps,
			Load1:     s.Load1,
		})
	}
	return series, nil
}

func (f *fakeStore) ListEgress(_ context.Context, month string) ([]models.EgressRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.EgressRecord, 0)
	for _, e := range f.egress {
		if e.Month == month {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) SaveBucketStats(_ context.Context, stats []models.BucketStats) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range stats {
		key := bucketKey(s.Provider, s.Bucket)
		f.buckets[key] = s
		f.history[key] = append(f.history[key], models.BucketPoint{
			Timestamp:      s.CollectedAt,
			SizeBytes:      s.SizeBytes,
			ObjectCount:    s.ObjectCount,
			RequestsWindow: s.RequestsWindow,
			ClassAOpsMTD:   s.ClassAOpsMTD,
			ClassBOpsMTD:   s.ClassBOpsMTD,
		})
	}
	return nil
}

func (f *fakeStore) LatestBuckets(_ context.Context) ([]models.BucketStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.BucketStats, 0, len(f.buckets))
	for _, s := range f.buckets {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out, nil
}

func (f *fakeStore) BucketHistory(_ context.Context, provider models.StorageProvider, bucket string, since int64) ([]models.BucketPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := bucketKey(provider, bucket)
	var out []models.BucketPoint
	for _, p := range f.history[key] {
		if p.Timestamp >= since {
			out = append(out, p)
		}
	}
	if out == nil {
		out = []models.BucketPoint{}
	}
	return out, nil
}

func (f *fakeStore) MarkAlertSent(_ context.Context, hostID, month string, level models.EgressLevel) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := alertKey(hostID, month, level)
	if f.alerts[key] {
		return false, nil
	}
	f.alerts[key] = true
	return true, nil
}

func (f *fakeStore) Rollup(_ context.Context, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollupCalls++
	return f.rollupErr
}

func (f *fakeStore) Prune(_ context.Context, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneCalls++
	return f.pruneErr
}

func (f *fakeStore) Close() error { return nil }

// setEgress directly sets a host's egress accumulator for a month,
// bypassing InsertSamples, for tests that want precise control over tx/rx
// totals.
func (f *fakeStore) setEgress(hostID, month string, tx, rx uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.egress[egressKey(hostID, month)] = models.EgressRecord{HostID: hostID, Month: month, TxBytes: tx, RxBytes: rx}
}

// fakeCollector is a BucketCollector test double.
type fakeCollector struct {
	name  string
	stats []models.BucketStats
	err   error

	mu    sync.Mutex
	calls int
}

func (c *fakeCollector) Name() string { return c.name }

func (c *fakeCollector) Collect(_ context.Context) ([]models.BucketStats, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.stats, c.err
}

func (c *fakeCollector) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// fakeNotifier is a Notifier test double recording every call.
type fakeNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
	err   error
}

type notifyCall struct {
	title, message string
}

func (n *fakeNotifier) Notify(_ context.Context, title, message string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, notifyCall{title, message})
	return n.err
}

func (n *fakeNotifier) callCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.calls)
}

var errFakeStore = errors.New("fake store error")
