// Package hub implements the cloud-pulse hub: an HTTP API that ingests
// agent reports, serves host/egress/bucket read models, and runs
// background maintenance (rollups, retention pruning, cloud bucket
// collection, and egress alerting).
//
// hub defines the interfaces it depends on (Store, BucketCollector,
// Notifier); internal/storage and internal/cloud provide concrete
// implementations, injected by cmd/hub.
package hub

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// Store is the persistence interface the hub depends on. Concrete
// implementations live in internal/storage.
type Store interface {
	// UpsertHost creates or updates a host's identity/metadata and its
	// last-seen timestamp.
	UpsertHost(ctx context.Context, h models.HostInfo, seenAt int64) error
	// InsertSamples idempotently inserts samples for hostID (unique on
	// host_id, ts), returning the count actually inserted (excluding
	// duplicates). It must also update the host's latest sample if a
	// newer one is inserted, and accumulate NetTx/RxBytes into the
	// monthly egress totals for newly inserted rows only.
	InsertSamples(ctx context.Context, hostID string, samples []models.Sample) (inserted int, err error)
	// ListHosts returns all known hosts.
	ListHosts(ctx context.Context) ([]models.HostRecord, error)
	// GetHost returns a single host by id, or models.ErrNotFound.
	GetHost(ctx context.Context, id string) (models.HostRecord, error)
	// QuerySeries returns a time series for hostID over [from, to] at a
	// resolution chosen by the implementation based on the range.
	QuerySeries(ctx context.Context, hostID string, from, to int64) (models.Series, error)
	// ListEgress returns per-host egress accumulators for month
	// ("YYYY-MM").
	ListEgress(ctx context.Context, month string) ([]models.EgressRecord, error)
	// SaveBucketStats persists collected bucket statistics.
	SaveBucketStats(ctx context.Context, stats []models.BucketStats) error
	// LatestBuckets returns the most recently collected stats for every
	// known bucket.
	LatestBuckets(ctx context.Context) ([]models.BucketStats, error)
	// BucketHistory returns historical points for one bucket since a
	// unix-seconds timestamp.
	BucketHistory(ctx context.Context, provider models.StorageProvider, bucket string, since int64) ([]models.BucketPoint, error)
	// MarkAlertSent records that an alert at level was sent for
	// hostID/month, returning first=true if this is the first time it
	// was recorded (idempotent).
	MarkAlertSent(ctx context.Context, hostID, month string, level models.EgressLevel) (first bool, err error)
	// Rollup aggregates raw samples into coarser resolutions as of now.
	Rollup(ctx context.Context, now time.Time) error
	// Prune deletes data past its retention window as of now.
	Prune(ctx context.Context, now time.Time) error
	// Close releases any resources held by the store.
	Close() error
}

// BucketCollector collects statistics for one cloud storage provider's
// buckets (e.g. S3 or R2). Implementations live in internal/cloud.
type BucketCollector interface {
	// Name identifies the collector (e.g. "s3", "r2") for status
	// reporting.
	Name() string
	// Collect returns bucket statistics, allowing partial results
	// together with a non-nil error.
	Collect(ctx context.Context) ([]models.BucketStats, error)
}

// Notifier delivers an alert notification. Implementations live in
// internal/hub (WebhookNotifier) or elsewhere.
type Notifier interface {
	// Notify sends a notification with the given title and message.
	Notify(ctx context.Context, title, message string) error
}

// Options configures a Server.
type Options struct {
	// AgentToken authenticates agent report ingestion.
	AgentToken string
	// UIToken authenticates read endpoints when non-empty. An empty
	// UIToken disables authentication on read endpoints.
	UIToken string
	// AllowedCIDRs restricts client addresses permitted to reach the
	// server. A nil slice allows all addresses.
	AllowedCIDRs []netip.Prefix
	// OfflineAfter is the duration since a host's last-seen time after
	// which it is reported as down.
	OfflineAfter time.Duration
	// CloudInterval is the interval between cloud bucket collections.
	CloudInterval time.Duration
	// Now returns the current time; nil defaults to time.Now.
	Now func() time.Time
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Server is the cloud-pulse hub HTTP server and background scheduler.
type Server struct {
	opts       Options
	store      Store
	collectors []BucketCollector
	notifier   Notifier
	assets     fs.FS
	logger     *slog.Logger

	mux *http.ServeMux

	alertWG sync.WaitGroup

	statusMu sync.Mutex
	status   map[string]models.CollectorStatus
}

// New constructs a Server. collectors and notifier may be empty/nil.
// assets may be nil, in which case "/" serves a JSON 404 for any path.
// logger must not be nil.
func New(opts Options, store Store, collectors []BucketCollector, notifier Notifier, assets fs.FS, logger *slog.Logger) *Server {
	s := &Server{
		opts:       opts,
		store:      store,
		collectors: collectors,
		notifier:   notifier,
		assets:     assets,
		logger:     logger,
		status:     make(map[string]models.CollectorStatus, len(collectors)),
	}
	for _, c := range collectors {
		s.status[c.Name()] = models.CollectorStatus{Name: c.Name(), Enabled: true}
	}
	s.mux = s.routes()
	return s
}

// Handler returns the fully wrapped HTTP handler (routes plus
// middleware) for the server.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.jsonMethodNotAllowed(s.mux)
	h = s.securityHeaders(h)
	h = s.cidrAllowlist(h)
	h = s.requestLogger(h)
	h = s.recoverMiddleware(h)
	return h
}

// Wait blocks until all in-flight background alert notifications have
// completed. cmd/hub should call this after stopping the HTTP server and
// canceling the background context, as part of a graceful shutdown.
func (s *Server) Wait() {
	s.alertWG.Wait()
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)

	mux.HandleFunc("POST /api/v1/agent/report", s.requireAgentToken(s.handleIngest))

	mux.HandleFunc("GET /api/v1/hosts", s.requireUIToken(s.handleListHosts))
	mux.HandleFunc("GET /api/v1/hosts/{id}", s.requireUIToken(s.handleGetHost))
	mux.HandleFunc("GET /api/v1/hosts/{id}/metrics", s.requireUIToken(s.handleHostMetrics))
	mux.HandleFunc("GET /api/v1/egress", s.requireUIToken(s.handleEgress))
	mux.HandleFunc("GET /api/v1/buckets", s.requireUIToken(s.handleBuckets))
	mux.HandleFunc("GET /api/v1/version", s.requireUIToken(s.handleVersion))

	// No catch-all is registered for the bare pattern "/api/" or "/":
	// doing so with no method restriction would make it match every
	// method on every subpath, which defeats ServeMux's built-in
	// method-mismatch (405) detection for the more specific routes
	// above. "GET /" below is method-scoped so it doesn't have that
	// problem; handleStatic itself rejects "/api/" paths that fall
	// through to it as the least-specific match with a JSON 404.
	mux.HandleFunc("GET /", s.handleStatic)

	return mux
}

// notFoundRecorder and jsonNotFound were considered for translating Go's
// default plain-text 404 into JSON, but registering any catch-all
// pattern (with or without a method) under "/api/" or "/" shadows
// ServeMux's per-route method-mismatch detection for more specific
// routes. handleStatic's explicit "/api/" check (see handlers.go)
// achieves the same JSON-404 result without that side effect.

// ErrNotFound is re-exported for convenience by callers that only import
// hub; it is identical to models.ErrNotFound.
var ErrNotFound = models.ErrNotFound

// isNotFound reports whether err wraps models.ErrNotFound.
func isNotFound(err error) bool {
	return errors.Is(err, models.ErrNotFound)
}
