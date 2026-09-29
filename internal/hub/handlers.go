package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// maxReportBytes is the maximum accepted size of an agent report body.
const maxReportBytes = 2 << 20 // 2 MiB

// maxSamplesPerReport is the maximum number of samples accepted in a
// single agent report.
const maxSamplesPerReport = 500

// maxSampleAge bounds how far in the past a sample's timestamp may be
// relative to now before it is rejected.
const maxSampleAge = 26 * time.Hour

// maxSampleSkew bounds how far in the future a sample's timestamp may be
// relative to now before it is rejected.
const maxSampleSkew = 5 * time.Minute

// handleHealthz reports basic liveness; it requires no authentication.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleVersion reports build metadata and the hub's self-update check
// status.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	info := buildVersionInfo(s.opts.UpdateSource != nil, s.updateStatusSnapshot())
	writeJSON(w, http.StatusOK, info)
}

// handleStatic serves the embedded web assets from s.assets for
// non-API paths. Requests under "/api/" that reach this fallback (no
// other route matched) get a JSON 404, since http.ServeMux would
// otherwise route them here as the least-specific match. When assets is
// nil, every request falls back to a JSON 404.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || s.assets == nil {
		writeJSON(w, http.StatusNotFound, models.APIError{Error: "not found"})
		return
	}
	// Embedded assets have neither modification times nor ETags. Require
	// browsers to revalidate them so an upgraded hub immediately serves the
	// replacement dashboard instead of a stale cached bundle.
	w.Header().Set("Cache-Control", "no-cache")
	http.FileServerFS(s.assets).ServeHTTP(w, r)
}

// handleIngest processes an agent report: POST /api/v1/agent/report.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.opts.now()

	r.Body = http.MaxBytesReader(w, r.Body, maxReportBytes)

	var report models.AgentReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	if !models.ValidHostID(report.Host.ID) {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid host id"})
		return
	}

	// 413 chosen over 400 for too-many-samples: the request is
	// well-formed but exceeds a size limit, which maps naturally to
	// Payload Too Large. Documented here and in notes/hub.md.
	if len(report.Samples) > maxSamplesPerReport {
		writeJSON(w, http.StatusRequestEntityTooLarge, models.APIError{Error: "too many samples"})
		return
	}

	minTS := now.Add(-maxSampleAge).Unix()
	maxTS := now.Add(maxSampleSkew).Unix()

	valid := make([]models.Sample, 0, len(report.Samples))
	rejected := 0
	for _, sample := range report.Samples {
		if sample.Timestamp < minTS || sample.Timestamp > maxTS {
			rejected++
			continue
		}
		valid = append(valid, sample)
	}

	if err := s.store.UpsertHost(ctx, report.Host, now.Unix()); err != nil {
		s.logger.Error("ingest: upsert host failed", "host_id", report.Host.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	inserted, err := s.store.InsertSamples(ctx, report.Host.ID, valid)
	if err != nil {
		s.logger.Error("ingest: insert samples failed", "host_id", report.Host.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	resp := models.IngestResponse{
		Accepted:      inserted,
		Duplicates:    len(valid) - inserted,
		Rejected:      rejected,
		ServerTimeMs:  s.opts.now().UnixMilli(),
		LatestVersion: s.updateStatusSnapshot().latestVersion,
	}
	writeJSON(w, http.StatusOK, resp)

	s.recordAgentConn(report.Host.ID, report.Host.Hostname, r)

	if len(valid) > 0 {
		s.afterIngest(report.Host, now)
	}
}

// handleAgentTime responds with the hub's current wall clock so agents
// can estimate their clock offset: GET /api/v1/agent/time. It requires
// the agent token and performs no database access.
func (s *Server) handleAgentTime(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, models.TimeResponse{ServerTimeMs: s.opts.now().UnixMilli()})
}

// handleListHosts responds with all known hosts, sorted by hostname:
// GET /api/v1/hosts.
func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.opts.now()

	records, err := s.store.ListHosts(ctx)
	if err != nil {
		s.logger.Error("list hosts failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	month := models.MonthOf(now)
	egressRecords, err := s.store.ListEgress(ctx, month)
	if err != nil {
		s.logger.Error("list egress failed", "month", month, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	egressByHost := indexEgressByHost(egressRecords)

	limits, err := s.store.ListHostLimits(ctx)
	if err != nil {
		s.logger.Error("list host limits failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	limitsByHost := indexLimitsByHost(limits)

	summaries := make([]models.HostSummary, 0, len(records))
	for _, rec := range records {
		tx, rx := egressByHost[rec.Info.ID].TxBytes, egressByHost[rec.Info.ID].RxBytes
		summaries = append(summaries, s.buildHostSummary(rec, limitsByHost[rec.Info.ID], tx, rx, now, s.opts.OfflineAfter))
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Host.Hostname < summaries[j].Host.Hostname
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"hosts":        summaries,
		"generated_at": now.Unix(),
	})
}

// handleGetHost responds with a single host summary: GET
// /api/v1/hosts/{id}.
func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.opts.now()
	id := r.PathValue("id")

	rec, err := s.store.GetHost(ctx, id)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "host not found"})
			return
		}
		s.logger.Error("get host failed", "host_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	tx, rx, err := s.currentMonthEgress(ctx, id, now)
	if err != nil {
		s.logger.Error("compute host summary failed", "host_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	limits, err := s.store.GetHostLimits(ctx, id)
	if err != nil {
		s.logger.Error("get host limits failed", "host_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	summary := s.buildHostSummary(rec, limits, tx, rx, now, s.opts.OfflineAfter)
	writeJSON(w, http.StatusOK, summary)
}

// rangeDurations maps accepted "range" query values to their duration.
var rangeDurations = map[string]time.Duration{
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// handleHostMetrics responds with a time series for one host: GET
// /api/v1/hosts/{id}/metrics?range=1h|6h|24h|7d|30d.
func (s *Server) handleHostMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.opts.now()
	id := r.PathValue("id")

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "1h"
	}
	duration, ok := rangeDurations[rangeParam]
	if !ok {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid range"})
		return
	}

	if _, err := s.store.GetHost(ctx, id); err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "host not found"})
			return
		}
		s.logger.Error("get host failed", "host_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	to := now.Unix()
	from := now.Add(-duration).Unix()

	series, err := s.store.QuerySeries(ctx, id, from, to)
	if err != nil {
		s.logger.Error("query series failed", "host_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, series)
}

// egressHostEntry is one host's egress usage in the /api/v1/egress
// response.
type egressHostEntry struct {
	HostID   string             `json:"host_id"`
	Hostname string             `json:"hostname"`
	Egress   models.EgressUsage `json:"egress"`
}

// handleEgress responds with per-host egress usage for a month: GET
// /api/v1/egress?month=YYYY-MM (default current month).
func (s *Server) handleEgress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.opts.now()

	month := r.URL.Query().Get("month")
	if month == "" {
		month = models.MonthOf(now)
	}
	if _, _, err := models.MonthBounds(month); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid month"})
		return
	}

	records, err := s.store.ListEgress(ctx, month)
	if err != nil {
		s.logger.Error("list egress failed", "month", month, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	hosts, err := s.store.ListHosts(ctx)
	if err != nil {
		s.logger.Error("list hosts failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	hostByID := make(map[string]models.HostRecord, len(hosts))
	for _, h := range hosts {
		hostByID[h.Info.ID] = h
	}

	limits, err := s.store.ListHostLimits(ctx)
	if err != nil {
		s.logger.Error("list host limits failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	limitsByHost := indexLimitsByHost(limits)

	entries := make([]egressHostEntry, 0, len(records))
	for _, rec := range records {
		agentLimit := uint64(0)
		hostname := rec.HostID
		if h, ok := hostByID[rec.HostID]; ok {
			agentLimit = h.Info.EgressLimitBytes
			hostname = h.Info.Hostname
		}
		txLimit, rxLimit, txSource, rxSource := models.EffectiveLimits(agentLimit, limitsByHost[rec.HostID])
		usage := models.ComputeEgress(month, rec.TxBytes, rec.RxBytes, txLimit, rxLimit, now)
		usage.LimitSource = txSource
		usage.RxLimitSource = rxSource
		entries = append(entries, egressHostEntry{
			HostID:   rec.HostID,
			Hostname: hostname,
			Egress:   usage,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Hostname < entries[j].Hostname
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"month": month,
		"hosts": entries,
	})
}

// handleBuckets responds with the latest stats, 24h history, and
// free-tier info for every known bucket, plus collector status: GET
// /api/v1/buckets.
func (s *Server) handleBuckets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.opts.now()

	latest, err := s.store.LatestBuckets(ctx)
	if err != nil {
		s.logger.Error("latest buckets failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	since := now.Add(-24 * time.Hour).Unix()
	views := make([]models.BucketView, 0, len(latest))
	for _, stats := range latest {
		history, err := s.store.BucketHistory(ctx, stats.Provider, stats.Bucket, since)
		if err != nil {
			s.logger.Error("bucket history failed", "provider", stats.Provider, "bucket", stats.Bucket, "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
		view := models.BucketView{
			Latest:  stats,
			History: history,
		}
		if stats.Provider == models.StorageR2 {
			view.FreeTier = &models.R2FreeTier
		}
		views = append(views, view)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"buckets":    views,
		"collectors": s.collectorStatuses(),
	})
}

// indexEgressByHost builds a hostID -> EgressRecord lookup from records.
func indexEgressByHost(records []models.EgressRecord) map[string]models.EgressRecord {
	out := make(map[string]models.EgressRecord, len(records))
	for _, r := range records {
		out[r.HostID] = r
	}
	return out
}

// indexLimitsByHost builds a hostID -> HostLimits lookup from limits.
// A host absent from limits (no stored override) resolves to the zero
// value via the map's zero-value semantics, which callers pass to
// models.EffectiveLimits as HostLimits{} (both pointers nil, same
// meaning as "no override").
func indexLimitsByHost(limits []models.HostLimits) map[string]models.HostLimits {
	out := make(map[string]models.HostLimits, len(limits))
	for _, l := range limits {
		out[l.HostID] = l
	}
	return out
}

// currentMonthEgress returns hostID's tx/rx byte accumulators for the
// current month (now), or zero if the host has no egress record yet.
func (s *Server) currentMonthEgress(ctx context.Context, hostID string, now time.Time) (tx, rx uint64, err error) {
	month := models.MonthOf(now)
	records, err := s.store.ListEgress(ctx, month)
	if err != nil {
		return 0, 0, fmt.Errorf("hub: list egress for host summary: %w", err)
	}
	for _, r := range records {
		if r.HostID == hostID {
			return r.TxBytes, r.RxBytes, nil
		}
	}
	return 0, 0, nil
}

// buildHostSummary computes a HostSummary for rec at now given its
// current-month tx/rx byte totals and hub-side limit overrides,
// resolving effective outbound/inbound limits via
// models.EffectiveLimits, and the available agent update (if any) via
// s.agentUpdateFor.
func (s *Server) buildHostSummary(rec models.HostRecord, limits models.HostLimits, tx, rx uint64, now time.Time, offlineAfter time.Duration) models.HostSummary {
	status := models.HostDown
	if now.Sub(time.Unix(rec.LastSeen, 0)) <= offlineAfter {
		status = models.HostUp
	}

	month := models.MonthOf(now)
	txLimit, rxLimit, txSource, rxSource := models.EffectiveLimits(rec.Info.EgressLimitBytes, limits)
	egress := models.ComputeEgress(month, tx, rx, txLimit, rxLimit, now)
	egress.LimitSource = txSource
	egress.RxLimitSource = rxSource

	return models.HostSummary{
		Host:     rec.Info,
		Status:   status,
		LastSeen: rec.LastSeen,
		Latest:   rec.Latest,
		Egress:   egress,
		Update:   s.agentUpdateFor(rec.Info),
	}
}

// collectorStatuses returns a snapshot of all registered collectors'
// statuses, sorted by name. It never returns nil (an empty slice encodes
// as [] rather than null).
func (s *Server) collectorStatuses() []models.CollectorStatus {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()

	out := make([]models.CollectorStatus, 0, len(s.status))
	for _, st := range s.status {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
