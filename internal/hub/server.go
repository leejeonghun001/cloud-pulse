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
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
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
	// hostID/month/dir, returning first=true if this is the first time
	// it was recorded (idempotent).
	MarkAlertSent(ctx context.Context, hostID, month string, dir models.Direction, level models.EgressLevel) (first bool, err error)
	// GetHostLimits returns the hub-side limit overrides for hostID. If
	// no row exists, it returns models.HostLimits{HostID: hostID} (both
	// pointers nil) and a nil error.
	GetHostLimits(ctx context.Context, hostID string) (models.HostLimits, error)
	// ListHostLimits returns every host's stored limit overrides,
	// sorted by host_id.
	ListHostLimits(ctx context.Context) ([]models.HostLimits, error)
	// SetHostLimits stores l's overrides for l.HostID. If both
	// EgressLimitBytes and IngressLimitBytes are nil, the row is
	// deleted (no override). If l.UpdatedAt is zero, it is set to the
	// current time.
	SetHostLimits(ctx context.Context, l models.HostLimits) error
	// GetSetting returns the stored value for key, and ok=false if no
	// value is stored.
	GetSetting(ctx context.Context, key string) (value string, ok bool, err error)
	// SetSetting stores value for key. An empty value deletes the
	// setting.
	SetSetting(ctx context.Context, key, value string) error
	// Rollup aggregates raw samples into coarser resolutions as of now.
	Rollup(ctx context.Context, now time.Time) error
	// Prune deletes data past its retention window as of now.
	Prune(ctx context.Context, now time.Time) error
	// CreateSession persists a new dashboard login session.
	CreateSession(ctx context.Context, sess models.Session) error
	// GetSession returns the session identified by idHash (hex-encoded
	// SHA-256 of the bearer token), or models.ErrNotFound.
	GetSession(ctx context.Context, idHash string) (models.Session, error)
	// TouchSession updates the session's LastSeen/ExpiresAt (sliding
	// idle expiry). Implementations may no-op if called more than once
	// per minute for the same session (throttled by the caller).
	TouchSession(ctx context.Context, idHash string, lastSeen, expiresAt int64) error
	// DeleteSession removes one session by idHash. Deleting a
	// non-existent session is not an error.
	DeleteSession(ctx context.Context, idHash string) error
	// DeleteSessionsExcept removes every session except keepIDHash (an
	// empty keepIDHash deletes all sessions), returning the number
	// deleted.
	DeleteSessionsExcept(ctx context.Context, keepIDHash string) (int, error)
	// ListSessions returns every stored session.
	ListSessions(ctx context.Context) ([]models.Session, error)
	// PruneSessions deletes sessions whose ExpiresAt is at or before
	// now.
	PruneSessions(ctx context.Context, now time.Time) error

	// --- Alerting (SPEC-v0.5 §B) ---

	// ListAlertRules returns every configured alert rule, sorted by ID.
	ListAlertRules(ctx context.Context) ([]models.AlertRule, error)
	// GetAlertRule returns one alert rule by id, or models.ErrNotFound.
	GetAlertRule(ctx context.Context, id int64) (models.AlertRule, error)
	// CreateAlertRule inserts r, ignoring r.ID, and returns the row with
	// its assigned ID and CreatedAt/UpdatedAt populated.
	CreateAlertRule(ctx context.Context, r models.AlertRule) (models.AlertRule, error)
	// UpdateAlertRule replaces the stored rule matching r.ID with r
	// (UpdatedAt refreshed to now), or returns models.ErrNotFound if no
	// rule with that ID exists.
	UpdateAlertRule(ctx context.Context, r models.AlertRule) (models.AlertRule, error)
	// DeleteAlertRule removes the rule with id. Deleting a non-existent
	// rule is not an error.
	DeleteAlertRule(ctx context.Context, id int64) error

	// ListNotifyChannels returns every configured notification channel,
	// sorted by ID. Config values are returned unredacted; callers that
	// expose them over the API must call models.NotifyChannel.Redacted.
	ListNotifyChannels(ctx context.Context) ([]models.NotifyChannel, error)
	// GetNotifyChannel returns one channel by id, or models.ErrNotFound.
	GetNotifyChannel(ctx context.Context, id int64) (models.NotifyChannel, error)
	// CreateNotifyChannel inserts ch, ignoring ch.ID, and returns the
	// row with its assigned ID and CreatedAt/UpdatedAt populated.
	CreateNotifyChannel(ctx context.Context, ch models.NotifyChannel) (models.NotifyChannel, error)
	// UpdateNotifyChannel replaces the stored channel matching ch.ID
	// with ch (UpdatedAt refreshed to now), or returns
	// models.ErrNotFound if no channel with that ID exists. Callers are
	// responsible for merging preserved secret fields (a request field
	// omitted or equal to models.RedactedConfigValue) into ch before
	// calling this method; the store itself performs no redaction
	// merge.
	UpdateNotifyChannel(ctx context.Context, ch models.NotifyChannel) (models.NotifyChannel, error)
	// DeleteNotifyChannel removes the channel with id. Deleting a
	// non-existent channel is not an error. Implementations do not
	// cascade into alert_rules.channel_ids; callers (the alerting
	// engine) must tolerate a rule referencing a since-deleted channel
	// ID by skipping it.
	DeleteNotifyChannel(ctx context.Context, id int64) error

	// GetAlertState returns the persisted state-machine row for
	// (ruleID, hostID), or a zero-value models.AlertState{RuleID,
	// HostID, State: models.AlertStateOK} with a nil error if no row
	// exists yet.
	GetAlertState(ctx context.Context, ruleID int64, hostID string) (models.AlertState, error)
	// SetAlertState upserts the state-machine row for (st.RuleID,
	// st.HostID). Implementations key on the (rule_id, host_id) primary
	// key.
	SetAlertState(ctx context.Context, st models.AlertState) error
	// ListAlertStates returns every persisted state-machine row whose
	// State is not models.AlertStateOK (i.e. pending or firing), for
	// the scheduler to re-evaluate on each tick without scanning every
	// rule/host pair that has never fired.
	ListAlertStates(ctx context.Context) ([]models.AlertState, error)
	// DeleteAlertStatesForRule removes every state-machine row for
	// ruleID, used when a rule is deleted so a stale firing/pending
	// state doesn't linger.
	DeleteAlertStatesForRule(ctx context.Context, ruleID int64) error

	// CreateAlertEvent inserts ev, ignoring ev.ID, and returns the row
	// with its assigned ID populated.
	CreateAlertEvent(ctx context.Context, ev models.AlertEvent) (models.AlertEvent, error)
	// UpdateAlertEvent replaces the stored event matching ev.ID with ev
	// in full (state, deliveries, resolved_at, ...), or returns
	// models.ErrNotFound if no event with that ID exists. Used both to
	// append delivery results and to transition Firing -> Resolved.
	UpdateAlertEvent(ctx context.Context, ev models.AlertEvent) error
	// GetAlertEvent returns one event by id, or models.ErrNotFound.
	GetAlertEvent(ctx context.Context, id int64) (models.AlertEvent, error)
	// GetActiveAlertEvent returns the currently-firing event for
	// (ruleID, hostID), or models.ErrNotFound if none is firing. Used to
	// find the event a re-notification or resolution should update
	// rather than creating a duplicate.
	GetActiveAlertEvent(ctx context.Context, ruleID int64, hostID string) (models.AlertEvent, error)
	// ListAlertEvents returns events matching the given filters, newest
	// first, at most limit rows. state == "" matches any state; hostID
	// == "" matches any host; before == 0 means no upper bound on
	// StartedAt (otherwise StartedAt < before), for cursor-style
	// pagination on repeated calls using the last row's StartedAt.
	ListAlertEvents(ctx context.Context, state models.AlertEventState, hostID string, before int64, limit int) ([]models.AlertEvent, error)
	// ListActiveAlertEvents returns every event currently in state
	// models.AlertEventFiring, newest first.
	ListActiveAlertEvents(ctx context.Context) ([]models.AlertEvent, error)
	// PruneAlertEvents deletes resolved events older than the retention
	// window as of now (180 days per SPEC-v0.5 §B). Events still firing
	// are never pruned regardless of age.
	PruneAlertEvents(ctx context.Context, now time.Time) error

	// --- Inventory (SPEC-v0.5 §C) ---

	// GetHostInventory returns the most recently stored inventory
	// snapshot for hostID, or models.ErrNotFound if none has been
	// collected yet.
	GetHostInventory(ctx context.Context, hostID string) (models.Inventory, error)
	// SetHostInventory replaces the stored inventory snapshot for
	// hostID with inv (one row per host; upsert on host_id).
	SetHostInventory(ctx context.Context, hostID string, inv models.Inventory) error

	// --- Cloud billing (SPEC-v0.6 §1) ---

	// GetCloudCostSnapshot returns the persisted snapshot for provider,
	// or a zero-value models.CloudCostSnapshot{Provider: provider,
	// Status: models.CloudBillingNotConfigured} with a nil error if no
	// row exists yet.
	GetCloudCostSnapshot(ctx context.Context, provider models.CloudBillingProvider) (models.CloudCostSnapshot, error)
	// ListCloudCostSnapshots returns every persisted provider snapshot.
	ListCloudCostSnapshots(ctx context.Context) ([]models.CloudCostSnapshot, error)
	// SetCloudCostSnapshot upserts the snapshot for snap.Provider,
	// keyed on provider (one row per provider).
	SetCloudCostSnapshot(ctx context.Context, snap models.CloudCostSnapshot) error

	// --- Remote agent updates (SPEC-v0.6 §2) ---

	// CreateUpdateJob inserts j, ignoring j.ID, and returns the row with
	// its assigned ID and CreatedAt/UpdatedAt populated.
	CreateUpdateJob(ctx context.Context, j models.UpdateJob) (models.UpdateJob, error)
	// GetUpdateJob returns one job by id, or models.ErrNotFound.
	GetUpdateJob(ctx context.Context, id int64) (models.UpdateJob, error)
	// UpdateUpdateJob replaces the stored job matching j.ID with j
	// (UpdatedAt refreshed to now), or returns models.ErrNotFound if no
	// job with that ID exists.
	UpdateUpdateJob(ctx context.Context, j models.UpdateJob) error
	// ListUpdateJobs returns jobs matching the given filters, newest
	// first. batchID == "" matches any batch; state == "" matches any
	// state.
	ListUpdateJobs(ctx context.Context, batchID string, state models.UpdateJobState) ([]models.UpdateJob, error)
	// GetLatestUpdateJobForHost returns the most recently created job
	// for hostID (any state), or models.ErrNotFound if none exists —
	// used to find the queued/in_progress job (if any) to hand back on
	// that host's next report.
	GetLatestUpdateJobForHost(ctx context.Context, hostID string) (models.UpdateJob, error)

	// --- Network cost estimation (SPEC-v0.6 §3) ---

	// ListPricingPlans returns every configured pricing plan, sorted by
	// ID.
	ListPricingPlans(ctx context.Context) ([]models.PricingPlan, error)
	// GetPricingPlan returns one plan by id, or models.ErrNotFound.
	GetPricingPlan(ctx context.Context, id int64) (models.PricingPlan, error)
	// CreatePricingPlan inserts p, ignoring p.ID and p.Builtin (always
	// stored false for a newly created plan), and returns the row with
	// its assigned ID and CreatedAt/UpdatedAt populated.
	CreatePricingPlan(ctx context.Context, p models.PricingPlan) (models.PricingPlan, error)
	// UpdatePricingPlan replaces the stored plan matching p.ID with p
	// (UpdatedAt refreshed to now), or returns models.ErrNotFound if no
	// plan with that ID exists. Callers must reject an attempt to
	// update a Builtin plan before calling this method (the store
	// itself does not enforce that rule).
	UpdatePricingPlan(ctx context.Context, p models.PricingPlan) (models.PricingPlan, error)
	// DeletePricingPlan removes the plan with id. Deleting a
	// non-existent plan is not an error. Callers must reject an attempt
	// to delete a Builtin plan before calling this method.
	DeletePricingPlan(ctx context.Context, id int64) error

	// GetHostPricing returns the pricing plan assignment for hostID, or
	// a zero-value models.HostPricing{HostID: hostID, PlanID: 0} with a
	// nil error if no row exists (caller applies the provider-based
	// default mapping in that case).
	GetHostPricing(ctx context.Context, hostID string) (models.HostPricing, error)
	// ListHostPricing returns every host's stored plan assignment.
	ListHostPricing(ctx context.Context) ([]models.HostPricing, error)
	// SetHostPricing upserts hp's assignment for hp.HostID.
	SetHostPricing(ctx context.Context, hp models.HostPricing) error

	// --- Audit log (SPEC-v0.6 §3 개선 c) ---

	// CreateAuditEntry inserts e, ignoring e.ID, and returns the row
	// with its assigned ID populated.
	CreateAuditEntry(ctx context.Context, e models.AuditEntry) (models.AuditEntry, error)
	// ListAuditEntries returns entries matching the given filters,
	// newest first, at most limit rows. entityType == "" matches any
	// entity type; before == 0 means no upper bound on At (otherwise At
	// < before), for cursor-style pagination.
	ListAuditEntries(ctx context.Context, entityType string, before int64, limit int) ([]models.AuditEntry, error)
	// PruneAuditEntries deletes entries older than
	// models.AuditRetentionDays as of now.
	PruneAuditEntries(ctx context.Context, now time.Time) error

	// Close releases any resources held by the store.
	Close() error
}

// SettingWebhookURL is the settings table key for the hub-side webhook
// URL override.
const SettingWebhookURL = "alert_webhook_url"

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

// ListenController is the subset of internal/listen.Manager's behavior
// the hub depends on: applying a desired set of listen addresses and
// reporting their current status. The network settings stage
// implements it; nil in tests means network settings endpoints operate
// without a real listener (a fake is injected instead).
type ListenController interface {
	// Apply reconciles the server's active listeners with addrs ("ip:port"
	// or ":port" entries), returning each new listener's status.
	Apply(ctx context.Context, addrs []string) ([]models.ListenerStatus, error)
	// Status returns the current status of every active listener.
	Status() []models.ListenerStatus
}

// AlertEngine evaluates alert rules against host state and manages the
// resulting firing/resolved lifecycle. Implementations live in
// internal/alerting; the alerting stage wires the real engine into
// Options.Alerting from cmd/hub. nil in tests/older code paths means
// alert routes that need it respond as if no engine is configured (the
// alerting stage's registerAlertRoutes handles this).
type AlertEngine interface {
	// Evaluate runs every enabled rule against hosts as of now,
	// advancing each rule+host's persisted state machine and enqueuing
	// any resulting notifications. Called after each successful ingest
	// (scoped to that one host) and periodically by the scheduler
	// (across every host, for host_down and sustained-window
	// transitions that need to fire even without new samples).
	Evaluate(ctx context.Context, now time.Time, hosts []models.HostSnapshot) error
	// Preview reports whether rule is currently satisfied for each
	// host, without altering any persisted state, for the rule editor's
	// live preview and POST /api/v1/alerts/rules/{id}/preview.
	Preview(ctx context.Context, now time.Time, rule models.AlertRule, hosts []models.HostSnapshot) (map[string]bool, error)
}

// NotifySenderFactory constructs a notification sender for a configured
// channel, used by alert delivery and the channel-test endpoints.
// Implementations live in internal/notify (notify.New adapted to this
// signature); the alerting stage wires the real factory into
// Options.NotifyFactory from cmd/hub. Returning an error (e.g. an
// unrecognized channel Type or invalid Config) must not panic.
type NotifySenderFactory func(ch models.NotifyChannel) (NotifySender, error)

// NotifySender delivers one notification message to a channel.
// internal/notify.Sender (adapted) and internal/alerting.Sender
// (adapted) both satisfy this interface; it is redeclared here so
// internal/hub does not need to import internal/notify or
// internal/alerting's message types directly. msg is boxed as `any`
// rather than a concrete type to avoid internal/hub importing either
// package: the alerting stage's own adapter
// (internal/hub/alertengine.go's notifySenderAdapter) unboxes msg as an
// internal/alerting.Message before delegating to a real
// internal/notify.Sender, and internal/hub's own callers (e.g.
// alertroutes.go's channel-test endpoints) box an internal/alerting.Message
// the same way — so every Send call in this codebase carries an
// internal/alerting.Message, never a raw internal/notify.Message.
type NotifySender interface {
	Send(ctx context.Context, msg any) error
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
	// EnvListen is the original CP_LISTEN value (e.g. ":8090",
	// "127.0.0.1:8090"), used to derive the "env" source
	// models.NetworkConfig for GET /api/v1/settings/network and as the
	// EnvListen field of models.NetworkState. Defaults to ":8090" when
	// empty (Options{} used directly in tests that don't care about
	// network settings).
	EnvListen string
	// OfflineAfter is the duration since a host's last-seen time after
	// which it is reported as down.
	OfflineAfter time.Duration
	// CloudInterval is the interval between cloud bucket collections.
	CloudInterval time.Duration
	// AlertWebhookURL is the webhook endpoint configured via environment
	// (CP_ALERT_WEBHOOK_URL). It is the fallback used when no hub-side
	// override is stored via Store.SetSetting(SettingWebhookURL, ...).
	AlertWebhookURL string
	// NotifierFor constructs a Notifier for the given webhook URL. If
	// nil, a hub.WebhookNotifier with a 10s-timeout client is used.
	// Tests inject a fake to observe/short-circuit outbound HTTP calls.
	NotifierFor func(url string) Notifier
	// Alerting evaluates alert rules (SPEC-v0.5 §B). nil disables the
	// alerts.* API's evaluation-dependent behavior (registerAlertRoutes
	// itself is still called; the alerting stage's handlers must handle
	// a nil Alerting gracefully, e.g. rule preview endpoints responding
	// 501 or an empty result until wired).
	Alerting AlertEngine
	// NotifyFactory constructs a NotifySender for a configured channel,
	// used by the channel-test endpoints and alert delivery. nil means
	// no notification sending is available yet (pre-alerting-stage).
	NotifyFactory NotifySenderFactory
	// UpdateSource resolves the latest published cloud-pulse release
	// tag for the background update checker. nil disables the checker
	// entirely (RunBackground's update-check loop becomes a no-op) and
	// GET /api/v1/version reports update_check_enabled accordingly.
	// cmd/hub constructs selfupdate.SourceFromEnv(os.LookupEnv) here
	// when CP_UPDATE_CHECK is true.
	UpdateSource LatestResolver
	// Listener manages the hub's actual HTTP listen addresses for the
	// Network settings page (GET/PUT/DELETE /api/v1/settings/network).
	// nil disables listener reconciliation (the network stage wires the
	// real internal/listen.Manager in cmd/hub; tests inject a fake).
	Listener ListenController
	// Now returns the current time; nil defaults to time.Now.
	Now func() time.Time
	// Billing wires SPEC-v0.6 §1's cloud billing collection/API and its
	// background scheduler — nil disables billing entirely (GET
	// /api/v1/billing responds with empty snapshots, POST
	// /api/v1/billing/refresh responds 501, and no background CLI
	// polling ever runs). See billingroutes.go/billingscheduler.go
	// (owned by the "billing" v0.6.0 stage) for BillingRuntime's
	// definition; cmd/hub/billing.go constructs the real one.
	Billing *BillingRuntime
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// envListen returns o.EnvListen, defaulting to ":8090" (config.Hub's own
// default for CP_LISTEN) when unset, so Options{} zero values used by
// tests that don't set EnvListen still produce a parseable value.
func (o Options) envListen() string {
	if o.EnvListen == "" {
		return ":8090"
	}
	return o.EnvListen
}

// notifierFor returns a Notifier for url using o.NotifierFor if set,
// otherwise a default hub.WebhookNotifier with a 10s-timeout client.
func (o Options) notifierFor(url string) Notifier {
	if o.NotifierFor != nil {
		return o.NotifierFor(url)
	}
	return WebhookNotifier{URL: url}
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

	// allowlist holds the server's current CIDR allowlist as an
	// *allowlistState, defaulting to opts.AllowedCIDRs until the network
	// settings handlers call setAllowedCIDRs (see middleware.go). An
	// atomic.Pointer allows cidrAllowlist to read it on every request
	// without locking, and setAllowedCIDRs to swap it without blocking
	// in-flight requests.
	allowlist atomic.Pointer[allowlistState]

	// agentConns tracks each agent's most recently observed local
	// address, for the Network settings page.
	agentConns *agentConnTracker

	// networkMu guards networkPending (the in-progress network change
	// awaiting confirmation, if any). See networkroutes.go.
	networkMu      sync.Mutex
	networkPending *pendingNetworkState
	// netAfterFunc overrides the timer constructor used to schedule a
	// pending network change's auto-revert; nil uses networkAfterFunc
	// (time.AfterFunc). Only tests in this package set this, to use a
	// fake/short-circuited timer instead of waiting on a real 120s
	// deadline.
	netAfterFunc afterFunc

	alertWG sync.WaitGroup

	statusMu sync.Mutex
	status   map[string]models.CollectorStatus

	updateMu      sync.Mutex
	updateStatusV updateStatus

	// updateFirstDelay/updateInterval override the update-check loop's
	// timing; both zero (the Server zero value) means "use the real
	// defaults" (firstUpdateCheckDelay/updateCheckInterval). Only tests
	// in this package set these, via newTestServerWithUpdateTiming.
	updateFirstDelay time.Duration
	updateInterval   time.Duration

	// limiter enforces per-IP and global login rate limiting plus the
	// PBKDF2 concurrency semaphore for POST /api/v1/auth/login and
	// /api/v1/auth/password (see SPEC-v0.4 §1, internal/hub/ratelimit.go).
	limiter *rateLimiter

	// batchLimitsMu guards batchLimits (SPEC-v0.6 §2's remote-update
	// batch max_parallel values, kept in-memory only — see
	// updateengine.go's batchMaxParallel doc comment).
	batchLimitsMu sync.Mutex
	batchLimits   map[string]int
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
		limiter:    newRateLimiter(opts.Now),
		agentConns: newAgentConnTracker(),
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
// completed: both legacy webhook-test-send goroutines (alertWG) and, if
// s.opts.Alerting implements alertEngineWaiter (internal/alerting.Engine
// does), its own async delivery worker's queue. cmd/hub should call this
// after stopping the HTTP server and canceling the background context,
// as part of a graceful shutdown, and tests use it to observe alert
// delivery results synchronously.
func (s *Server) Wait() {
	s.alertWG.Wait()
	if w, ok := s.opts.Alerting.(alertEngineWaiter); ok {
		w.Wait()
	}
}

// alertEngineWaiter is implemented by an AlertEngine whose notification
// delivery is asynchronous (internal/alerting.Engine) and needs a way
// for callers to synchronize on in-flight deliveries without importing
// internal/alerting directly from this package (see AlertEngine's doc
// comment on the same internal/hub/internal/alerting decoupling
// rationale). An AlertEngine that doesn't implement this (e.g. a test
// fake) simply makes Wait a no-op for that part.
type alertEngineWaiter interface {
	Wait()
}

// effectiveWebhookURL resolves the webhook URL alerts and the test-send
// endpoint should use: the hub-side setting (Store.GetSetting) if
// non-empty, otherwise s.opts.AlertWebhookURL (from CP_ALERT_WEBHOOK_URL),
// otherwise "". source is "hub", "env", or "none" respectively.
func (s *Server) effectiveWebhookURL(ctx context.Context) (url, source string, err error) {
	value, ok, err := s.store.GetSetting(ctx, SettingWebhookURL)
	if err != nil {
		return "", "", fmt.Errorf("hub: get webhook url setting: %w", err)
	}
	if ok && value != "" {
		return value, "hub", nil
	}
	if s.opts.AlertWebhookURL != "" {
		return s.opts.AlertWebhookURL, "env", nil
	}
	return "", "none", nil
}

// resolveNotifier returns the Notifier to use for the given webhook url,
// preferring an explicit notifier injected via New (used by tests and by
// callers that want a single fixed notifier), and otherwise constructing
// one via s.opts.notifierFor(url).
func (s *Server) resolveNotifier(url string) Notifier {
	if s.notifier != nil {
		return s.notifier
	}
	return s.opts.notifierFor(url)
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)

	mux.HandleFunc("POST /api/v1/agent/report", s.requireAgentToken(s.handleIngest))
	mux.HandleFunc("GET /api/v1/agent/time", s.requireAgentToken(s.handleAgentTime))

	mux.HandleFunc("GET /api/v1/hosts", s.requireUser(s.handleListHosts))
	mux.HandleFunc("GET /api/v1/hosts/{id}", s.requireUser(s.handleGetHost))
	mux.HandleFunc("GET /api/v1/hosts/{id}/metrics", s.requireUser(s.handleHostMetrics))
	mux.HandleFunc("GET /api/v1/egress", s.requireUser(s.handleEgress))
	mux.HandleFunc("GET /api/v1/buckets", s.requireUser(s.handleBuckets))
	mux.HandleFunc("GET /api/v1/version", s.requireUser(s.handleVersion))

	mux.HandleFunc("GET /api/v1/settings", s.requireAdmin(s.handleGetSettings))
	mux.HandleFunc("GET /api/v1/settings/agent-token", s.requireAdmin(s.handleGetAgentToken))
	mux.HandleFunc("PUT /api/v1/hosts/{id}/limits", s.requireAdmin(s.handleSetHostLimits))
	mux.HandleFunc("PUT /api/v1/settings/alerts", s.requireAdmin(s.handleSetAlertWebhook))
	mux.HandleFunc("POST /api/v1/settings/alerts/test", s.requireAdmin(s.handleTestAlertWebhook))

	s.registerAuthRoutes(mux)
	s.registerNetworkRoutes(mux)
	s.registerAlertRoutes(mux)
	s.registerInventoryRoutes(mux)
	s.registerBillingRoutes(mux)
	s.registerUpdateRoutes(mux)
	s.registerAuditRoutes(mux)
	s.registerPricingRoutes(mux)

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
