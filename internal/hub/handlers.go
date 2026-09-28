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
	"github.com/leejeonghun001/cloud-pulse/internal/version"
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

// handleVersion reports build metadata.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	})
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
		Accepted:   inserted,
		Duplicates: len(valid) - inserted,
		Rejected:   rejected,
	}
	writeJSON(w, http.StatusOK, resp)

	if len(valid) > 0 {
		s.afterIngest(report.Host, now)
	}
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

	summaries := make([]models.HostSummary, 0, len(records))
	for _, rec := range records {
		summary, err := s.hostSummary(ctx, rec, now)
		if err != nil {
			s.logger.Error("compute host summary failed", "host_id", rec.Info.ID, "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
		summaries = append(summaries, summary)
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

	summary, err := s.hostSummary(ctx, rec, now)
	if err != nil {
		s.logger.Error("compute host summary failed", "host_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
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

	entries := make([]egressHostEntry, 0, len(records))
	for _, rec := range records {
		limit := uint64(0)
		hostname := rec.HostID
		if h, ok := hostByID[rec.HostID]; ok {
			limit = h.Info.EgressLimitBytes
			hostname = h.Info.Hostname
		}
		usage := models.ComputeEgress(month, rec.TxBytes, rec.RxBytes, limit, now)
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

// hostSummary computes a HostSummary for rec at now, including its
// current-month egress usage.
func (s *Server) hostSummary(ctx context.Context, rec models.HostRecord, now time.Time) (models.HostSummary, error) {
	status := models.HostDown
	if now.Sub(time.Unix(rec.LastSeen, 0)) <= s.opts.OfflineAfter {
		status = models.HostUp
	}

	month := models.MonthOf(now)
	records, err := s.store.ListEgress(ctx, month)
	if err != nil {
		return models.HostSummary{}, fmt.Errorf("hub: list egress for host summary: %w", err)
	}

	var tx, rx uint64
	for _, r := range records {
		if r.HostID == rec.Info.ID {
			tx, rx = r.TxBytes, r.RxBytes
			break
		}
	}

	egress := models.ComputeEgress(month, tx, rx, rec.Info.EgressLimitBytes, now)

	return models.HostSummary{
		Host:     rec.Info,
		Status:   status,
		LastSeen: rec.LastSeen,
		Latest:   rec.Latest,
		Egress:   egress,
	}, nil
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
