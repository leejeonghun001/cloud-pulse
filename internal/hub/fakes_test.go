package hub

import (
	"context"
	"errors"
	"fmt"
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

	hosts    map[string]models.HostRecord
	samples  map[string][]models.Sample     // hostID -> samples, sorted by ts ascending
	egress   map[string]models.EgressRecord // key: hostID+"|"+month
	alerts   map[string]bool                // key: hostID+"|"+month+"|"+direction+"|"+level
	buckets  map[string]models.BucketStats  // key: provider+"|"+bucket
	history  map[string][]models.BucketPoint
	limits   map[string]models.HostLimits // key: hostID
	settings map[string]string            // key: setting key
	sessions map[string]models.Session    // key: IDHash

	alertRules  map[int64]models.AlertRule
	nextRuleID  int64
	channels    map[int64]models.NotifyChannel
	nextChanID  int64
	alertStates map[string]models.AlertState // key: ruleID+"|"+hostID
	alertEvents map[int64]models.AlertEvent
	nextEventID int64
	inventories map[string]models.Inventory // key: hostID

	cloudCostSnapshots map[models.CloudBillingProvider]models.CloudCostSnapshot

	updateJobs map[int64]models.UpdateJob
	nextJobID  int64

	pricingPlans map[int64]models.PricingPlan
	nextPlanID   int64
	hostPricing  map[string]models.HostPricing // key: hostID

	auditEntries []models.AuditEntry
	nextAuditID  int64

	rollupErr   error
	pruneErr    error
	rollupCalls int
	pruneCalls  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		hosts:              make(map[string]models.HostRecord),
		samples:            make(map[string][]models.Sample),
		egress:             make(map[string]models.EgressRecord),
		alerts:             make(map[string]bool),
		buckets:            make(map[string]models.BucketStats),
		history:            make(map[string][]models.BucketPoint),
		limits:             make(map[string]models.HostLimits),
		settings:           make(map[string]string),
		sessions:           make(map[string]models.Session),
		alertRules:         make(map[int64]models.AlertRule),
		channels:           make(map[int64]models.NotifyChannel),
		alertStates:        make(map[string]models.AlertState),
		alertEvents:        make(map[int64]models.AlertEvent),
		inventories:        make(map[string]models.Inventory),
		cloudCostSnapshots: make(map[models.CloudBillingProvider]models.CloudCostSnapshot),
		updateJobs:         make(map[int64]models.UpdateJob),
		pricingPlans:       make(map[int64]models.PricingPlan),
		hostPricing:        make(map[string]models.HostPricing),
	}
}

func egressKey(hostID, month string) string {
	return hostID + "|" + month
}

func alertKey(hostID, month string, dir models.Direction, level models.EgressLevel) string {
	return hostID + "|" + month + "|" + string(dir) + "|" + string(level)
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

func (f *fakeStore) MarkAlertSent(_ context.Context, hostID, month string, dir models.Direction, level models.EgressLevel) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := alertKey(hostID, month, dir, level)
	if f.alerts[key] {
		return false, nil
	}
	f.alerts[key] = true
	return true, nil
}

func (f *fakeStore) GetHostLimits(_ context.Context, hostID string) (models.HostLimits, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.limits[hostID]
	if !ok {
		return models.HostLimits{HostID: hostID}, nil
	}
	return l, nil
}

func (f *fakeStore) ListHostLimits(_ context.Context) ([]models.HostLimits, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.HostLimits, 0, len(f.limits))
	for _, l := range f.limits {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostID < out[j].HostID })
	return out, nil
}

func (f *fakeStore) SetHostLimits(_ context.Context, l models.HostLimits) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l.EgressLimitBytes == nil && l.IngressLimitBytes == nil {
		delete(f.limits, l.HostID)
		return nil
	}
	if l.UpdatedAt == 0 {
		l.UpdatedAt = time.Now().Unix()
	}
	f.limits[l.HostID] = l
	return nil
}

func (f *fakeStore) GetSetting(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.settings[key]
	return v, ok, nil
}

func (f *fakeStore) SetSetting(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if value == "" {
		delete(f.settings, key)
		return nil
	}
	f.settings[key] = value
	return nil
}

func (f *fakeStore) Rollup(_ context.Context, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollupCalls++
	return f.rollupErr
}

func (f *fakeStore) Prune(_ context.Context, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneCalls++
	cutoff := now.Unix()
	for idHash, sess := range f.sessions {
		if sess.ExpiresAt <= cutoff {
			delete(f.sessions, idHash)
		}
	}
	return f.pruneErr
}

func (f *fakeStore) CreateSession(_ context.Context, sess models.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[sess.IDHash] = sess
	return nil
}

func (f *fakeStore) GetSession(_ context.Context, idHash string) (models.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[idHash]
	if !ok {
		return models.Session{}, models.ErrNotFound
	}
	return sess, nil
}

func (f *fakeStore) TouchSession(_ context.Context, idHash string, lastSeen, expiresAt int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[idHash]
	if !ok {
		return models.ErrNotFound
	}
	sess.LastSeen = lastSeen
	sess.ExpiresAt = expiresAt
	f.sessions[idHash] = sess
	return nil
}

func (f *fakeStore) DeleteSession(_ context.Context, idHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, idHash)
	return nil
}

func (f *fakeStore) DeleteSessionsExcept(_ context.Context, keepIDHash string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	deleted := 0
	for idHash := range f.sessions {
		if idHash == keepIDHash {
			continue
		}
		delete(f.sessions, idHash)
		deleted++
	}
	return deleted, nil
}

func (f *fakeStore) ListSessions(_ context.Context) ([]models.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.Session, 0, len(f.sessions))
	for _, sess := range f.sessions {
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IDHash < out[j].IDHash })
	return out, nil
}

func (f *fakeStore) PruneSessions(_ context.Context, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := now.Unix()
	for idHash, sess := range f.sessions {
		if sess.ExpiresAt <= cutoff {
			delete(f.sessions, idHash)
		}
	}
	return nil
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

// --- Alerting (SPEC-v0.5 §B) ---

func (f *fakeStore) ListAlertRules(_ context.Context) ([]models.AlertRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.AlertRule, 0, len(f.alertRules))
	for _, r := range f.alertRules {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) GetAlertRule(_ context.Context, id int64) (models.AlertRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.alertRules[id]
	if !ok {
		return models.AlertRule{}, models.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) CreateAlertRule(_ context.Context, r models.AlertRule) (models.AlertRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRuleID++
	r.ID = f.nextRuleID
	now := time.Now().Unix()
	r.CreatedAt, r.UpdatedAt = now, now
	f.alertRules[r.ID] = r
	return r, nil
}

func (f *fakeStore) UpdateAlertRule(_ context.Context, r models.AlertRule) (models.AlertRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.alertRules[r.ID]
	if !ok {
		return models.AlertRule{}, models.ErrNotFound
	}
	r.CreatedAt = existing.CreatedAt
	r.UpdatedAt = time.Now().Unix()
	f.alertRules[r.ID] = r
	return r, nil
}

func (f *fakeStore) DeleteAlertRule(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.alertRules, id)
	return nil
}

func (f *fakeStore) ListNotifyChannels(_ context.Context) ([]models.NotifyChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.NotifyChannel, 0, len(f.channels))
	for _, c := range f.channels {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) GetNotifyChannel(_ context.Context, id int64) (models.NotifyChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.channels[id]
	if !ok {
		return models.NotifyChannel{}, models.ErrNotFound
	}
	return c, nil
}

func (f *fakeStore) CreateNotifyChannel(_ context.Context, ch models.NotifyChannel) (models.NotifyChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextChanID++
	ch.ID = f.nextChanID
	now := time.Now().Unix()
	ch.CreatedAt, ch.UpdatedAt = now, now
	f.channels[ch.ID] = ch
	return ch, nil
}

func (f *fakeStore) UpdateNotifyChannel(_ context.Context, ch models.NotifyChannel) (models.NotifyChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.channels[ch.ID]
	if !ok {
		return models.NotifyChannel{}, models.ErrNotFound
	}
	ch.CreatedAt = existing.CreatedAt
	ch.UpdatedAt = time.Now().Unix()
	f.channels[ch.ID] = ch
	return ch, nil
}

func (f *fakeStore) DeleteNotifyChannel(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.channels, id)
	return nil
}

func alertStateKey(ruleID int64, hostID string) string {
	return fmt.Sprintf("%d|%s", ruleID, hostID)
}

func (f *fakeStore) GetAlertState(_ context.Context, ruleID int64, hostID string) (models.AlertState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.alertStates[alertStateKey(ruleID, hostID)]
	if !ok {
		return models.AlertState{RuleID: ruleID, HostID: hostID, State: models.AlertStateOK}, nil
	}
	return st, nil
}

func (f *fakeStore) SetAlertState(_ context.Context, st models.AlertState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alertStates[alertStateKey(st.RuleID, st.HostID)] = st
	return nil
}

func (f *fakeStore) ListAlertStates(_ context.Context) ([]models.AlertState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.AlertState, 0, len(f.alertStates))
	for _, st := range f.alertStates {
		if st.State == models.AlertStateOK {
			continue
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RuleID != out[j].RuleID {
			return out[i].RuleID < out[j].RuleID
		}
		return out[i].HostID < out[j].HostID
	})
	return out, nil
}

func (f *fakeStore) DeleteAlertStatesForRule(_ context.Context, ruleID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, st := range f.alertStates {
		if st.RuleID == ruleID {
			delete(f.alertStates, key)
		}
	}
	return nil
}

func (f *fakeStore) CreateAlertEvent(_ context.Context, ev models.AlertEvent) (models.AlertEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextEventID++
	ev.ID = f.nextEventID
	if ev.Deliveries == nil {
		ev.Deliveries = []models.Delivery{}
	}
	f.alertEvents[ev.ID] = ev
	return ev, nil
}

func (f *fakeStore) UpdateAlertEvent(_ context.Context, ev models.AlertEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.alertEvents[ev.ID]; !ok {
		return models.ErrNotFound
	}
	if ev.Deliveries == nil {
		ev.Deliveries = []models.Delivery{}
	}
	f.alertEvents[ev.ID] = ev
	return nil
}

func (f *fakeStore) GetAlertEvent(_ context.Context, id int64) (models.AlertEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ev, ok := f.alertEvents[id]
	if !ok {
		return models.AlertEvent{}, models.ErrNotFound
	}
	return ev, nil
}

func (f *fakeStore) GetActiveAlertEvent(_ context.Context, ruleID int64, hostID string) (models.AlertEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.alertEvents {
		if ev.RuleID == ruleID && ev.HostID == hostID && ev.State == models.AlertEventFiring {
			return ev, nil
		}
	}
	return models.AlertEvent{}, models.ErrNotFound
}

func (f *fakeStore) ListAlertEvents(_ context.Context, state models.AlertEventState, hostID string, before int64, limit int) ([]models.AlertEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.AlertEvent, 0, len(f.alertEvents))
	for _, ev := range f.alertEvents {
		if state != "" && ev.State != state {
			continue
		}
		if hostID != "" && ev.HostID != hostID {
			continue
		}
		if before != 0 && ev.StartedAt >= before {
			continue
		}
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) ListActiveAlertEvents(_ context.Context) ([]models.AlertEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.AlertEvent, 0)
	for _, ev := range f.alertEvents {
		if ev.State == models.AlertEventFiring {
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out, nil
}

func (f *fakeStore) PruneAlertEvents(_ context.Context, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	const retention = 180 * 24 * time.Hour
	cutoff := now.Add(-retention).Unix()
	for id, ev := range f.alertEvents {
		if ev.State == models.AlertEventFiring {
			continue
		}
		if ev.StartedAt < cutoff {
			delete(f.alertEvents, id)
		}
	}
	return nil
}

// --- Inventory (SPEC-v0.5 §C) ---

func (f *fakeStore) GetHostInventory(_ context.Context, hostID string) (models.Inventory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inv, ok := f.inventories[hostID]
	if !ok {
		return models.Inventory{}, models.ErrNotFound
	}
	return inv, nil
}

func (f *fakeStore) SetHostInventory(_ context.Context, hostID string, inv models.Inventory) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inventories[hostID] = inv
	return nil
}

// --- Cloud billing (SPEC-v0.6 §1) ---

func (f *fakeStore) GetCloudCostSnapshot(_ context.Context, provider models.CloudBillingProvider) (models.CloudCostSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap, ok := f.cloudCostSnapshots[provider]
	if !ok {
		return models.CloudCostSnapshot{Provider: provider, Status: models.CloudBillingNotConfigured}, nil
	}
	return snap, nil
}

func (f *fakeStore) ListCloudCostSnapshots(_ context.Context) ([]models.CloudCostSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.CloudCostSnapshot, 0, len(f.cloudCostSnapshots))
	for _, snap := range f.cloudCostSnapshots {
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

func (f *fakeStore) SetCloudCostSnapshot(_ context.Context, snap models.CloudCostSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cloudCostSnapshots[snap.Provider] = snap
	return nil
}

// --- Remote agent updates (SPEC-v0.6 §2) ---

func (f *fakeStore) CreateUpdateJob(_ context.Context, j models.UpdateJob) (models.UpdateJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextJobID++
	j.ID = f.nextJobID
	now := time.Now().Unix()
	j.CreatedAt, j.UpdatedAt = now, now
	if j.Attempt == 0 {
		j.Attempt = 1
	}
	f.updateJobs[j.ID] = j
	return j, nil
}

func (f *fakeStore) GetUpdateJob(_ context.Context, id int64) (models.UpdateJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.updateJobs[id]
	if !ok {
		return models.UpdateJob{}, models.ErrNotFound
	}
	return j, nil
}

func (f *fakeStore) UpdateUpdateJob(_ context.Context, j models.UpdateJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.updateJobs[j.ID]
	if !ok {
		return models.ErrNotFound
	}
	j.CreatedAt = existing.CreatedAt
	j.UpdatedAt = time.Now().Unix()
	f.updateJobs[j.ID] = j
	return nil
}

func (f *fakeStore) ListUpdateJobs(_ context.Context, batchID string, state models.UpdateJobState) ([]models.UpdateJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.UpdateJob, 0, len(f.updateJobs))
	for _, j := range f.updateJobs {
		if batchID != "" && j.BatchID != batchID {
			continue
		}
		if state != "" && j.State != state {
			continue
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

func (f *fakeStore) GetLatestUpdateJobForHost(_ context.Context, hostID string) (models.UpdateJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var latest models.UpdateJob
	found := false
	for _, j := range f.updateJobs {
		if j.HostID != hostID {
			continue
		}
		if !found || j.CreatedAt > latest.CreatedAt {
			latest = j
			found = true
		}
	}
	if !found {
		return models.UpdateJob{}, models.ErrNotFound
	}
	return latest, nil
}

// --- Network cost estimation (SPEC-v0.6 §3) ---

func (f *fakeStore) ListPricingPlans(_ context.Context) ([]models.PricingPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.PricingPlan, 0, len(f.pricingPlans))
	for _, p := range f.pricingPlans {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) GetPricingPlan(_ context.Context, id int64) (models.PricingPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pricingPlans[id]
	if !ok {
		return models.PricingPlan{}, models.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) CreatePricingPlan(_ context.Context, p models.PricingPlan) (models.PricingPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextPlanID++
	p.ID = f.nextPlanID
	p.Builtin = false
	now := time.Now().Unix()
	p.CreatedAt, p.UpdatedAt = now, now
	f.pricingPlans[p.ID] = p
	return p, nil
}

func (f *fakeStore) UpdatePricingPlan(_ context.Context, p models.PricingPlan) (models.PricingPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.pricingPlans[p.ID]
	if !ok {
		return models.PricingPlan{}, models.ErrNotFound
	}
	p.CreatedAt = existing.CreatedAt
	p.UpdatedAt = time.Now().Unix()
	f.pricingPlans[p.ID] = p
	return p, nil
}

func (f *fakeStore) DeletePricingPlan(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pricingPlans, id)
	return nil
}

func (f *fakeStore) GetHostPricing(_ context.Context, hostID string) (models.HostPricing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hp, ok := f.hostPricing[hostID]
	if !ok {
		return models.HostPricing{HostID: hostID}, nil
	}
	return hp, nil
}

func (f *fakeStore) ListHostPricing(_ context.Context) ([]models.HostPricing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.HostPricing, 0, len(f.hostPricing))
	for _, hp := range f.hostPricing {
		out = append(out, hp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostID < out[j].HostID })
	return out, nil
}

func (f *fakeStore) SetHostPricing(_ context.Context, hp models.HostPricing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hostPricing[hp.HostID] = hp
	return nil
}

// --- Audit log (SPEC-v0.6 §3 개선 c) ---

func (f *fakeStore) CreateAuditEntry(_ context.Context, e models.AuditEntry) (models.AuditEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextAuditID++
	e.ID = f.nextAuditID
	f.auditEntries = append(f.auditEntries, e)
	return e, nil
}

func (f *fakeStore) ListAuditEntries(_ context.Context, entityType string, before int64, limit int) ([]models.AuditEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.AuditEntry, 0, len(f.auditEntries))
	for _, e := range f.auditEntries {
		if entityType != "" && e.EntityType != entityType {
			continue
		}
		if before != 0 && e.At >= before {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) PruneAuditEntries(_ context.Context, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := now.AddDate(0, 0, -models.AuditRetentionDays).Unix()
	kept := f.auditEntries[:0]
	for _, e := range f.auditEntries {
		if e.At >= cutoff {
			kept = append(kept, e)
		}
	}
	f.auditEntries = kept
	return nil
}

// setAlertRule directly stores a rule under its own ID, bypassing
// CreateAlertRule's auto-increment, for tests that need a specific ID
// (e.g. matching a channel reference fixture).
func (f *fakeStore) setAlertRule(r models.AlertRule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alertRules[r.ID] = r
	if r.ID > f.nextRuleID {
		f.nextRuleID = r.ID
	}
}

// setNotifyChannel directly stores a channel under its own ID,
// bypassing CreateNotifyChannel's auto-increment, for the same reason as
// setAlertRule.
func (f *fakeStore) setNotifyChannel(ch models.NotifyChannel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels[ch.ID] = ch
	if ch.ID > f.nextChanID {
		f.nextChanID = ch.ID
	}
}

// forceSetUpdateJob directly overwrites the stored row for j.ID
// (assumed to already exist), bypassing UpdateUpdateJob's
// real-wall-clock UpdatedAt stamping — for tests (SPEC-v0.6 §2's job
// timeout logic) that need full control over UpdatedAt to be
// deterministic against an injected clock rather than actual test
// execution speed.
func (f *fakeStore) forceSetUpdateJob(j models.UpdateJob) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateJobs[j.ID] = j
}

var errFakeStore = errors.New("fake store error")

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
