// storagerun.go wires SPEC-v0.7 §3's storage-usage account polling and
// OAuth flows into the hub: a background poller (built on
// internal/poller, mirroring billingscheduler.go's shape), transient
// in-memory OAuth flow state (device-code polling state for Google,
// PKCE verifier state for Dropbox — see notes/v07-storage.md design
// decision 4/5 on why this is not persisted), and the manual-refresh
// rate limiter.
package hub

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/poller"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage"
)

// SettingStorageInterval is the settings table key for the hub-side
// storage-usage polling interval override (SPEC-v0.7 §3), mirroring
// SettingBillingInterval. Its absence means "use
// StorageRuntime.EnvInterval".
const SettingStorageInterval = "storage_interval"

// storageRefreshMinInterval is the minimum time between POST
// /api/v1/storage/refresh calls (SPEC-v0.7 §3: "지금 조회 버튼은 최소
// 1분 간격").
const storageRefreshMinInterval = 1 * time.Minute

// googleDevicePollTimeout bounds how long the hub keeps polling
// Google's token endpoint for one oauth/start call before giving up,
// distinct from the device code's own server-side expiry (usually
// 1800s) — this is a defensive upper bound in case a caller never
// disconnects/cleans up the account and Google's expiry is unusually
// long.
const googleDevicePollTimeout = 30 * time.Minute

// StorageRuntime wires SPEC-v0.7 §3's storage-usage account collection
// into the hub. Constructed by cmd/hub/storage.go and passed as
// Options.Storage; nil disables storage-usage entirely (GET
// /api/v1/storage/accounts still works — it has no provider
// dependency — but every mutating endpoint responds 501 and no
// background polling runs).
type StorageRuntime struct {
	// EnvInterval is the CP_STORAGE_INTERVAL value at hub startup, used
	// whenever no "storage_interval" setting has been confirmed from
	// the dashboard yet.
	EnvInterval models.StorageInterval
	// Now returns the current time; nil defaults to time.Now. Tests
	// inject a fixed clock.
	Now func() time.Time
	// HTTPClient is used by provider constructors for outbound HTTP
	// calls; nil defaults to http.DefaultClient's timeout-bounded
	// equivalent (set by cmd/hub/storage.go in production).
	HTTPClient storageusage.HTTPDoer
	// Registry contains the account providers used by this hub. Nil
	// lazily creates a fresh DefaultRegistry, preserving zero-value test
	// runtime behavior without sharing mutable provider state.
	Registry *storageusage.Registry
	// AllowCustomEndpoints relaxes the OAuth-flow SSRF checkers
	// (googleSSRFChecker/dropboxSSRFChecker) to accept any host and
	// plaintext http://, sourced from CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS
	// (see config.Hub.StorageAllowCustomEndpoints and
	// storageusage.AllowCustomEndpointsFromEnv). false in every
	// production hub; a test/smoke harness sets it to point the hub at
	// a fake local Google/Dropbox OAuth server.
	AllowCustomEndpoints bool
	// FakeBaseURL, when AllowCustomEndpoints is also true, is the base
	// URL (e.g. "http://127.0.0.1:18500") every Google/Dropbox OAuth
	// and API request is redirected onto instead of the real official
	// host — see storageusage.SSRFOptions.RedirectBase's doc comment
	// for why an SSRF-allowlist widening alone doesn't route requests
	// to a fake server. Sourced from CP_STORAGE_FAKE_BASE_URL
	// (storageusage.RedirectBaseFromEnv); empty means "no redirect,"
	// even if AllowCustomEndpoints is true.
	FakeBaseURL string

	registryOnce sync.Once
	gate         poller.RefreshGate
	gateOnce     sync.Once
	runMu        poller.RunMutex
	logGate      poller.DailyLogGate

	flowMu sync.Mutex
	flows  map[int64]*oauthFlowState // accountID -> in-progress OAuth flow
}

func (s *StorageRuntime) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *StorageRuntime) storageRegistry() *storageusage.Registry {
	s.registryOnce.Do(func() {
		if s.Registry == nil {
			s.Registry = storageusage.DefaultRegistry()
		}
	})
	return s.Registry
}

func (s *StorageRuntime) refreshGate() *poller.RefreshGate {
	s.gateOnce.Do(func() {
		s.gate.MinInterval = storageRefreshMinInterval
		s.gate.Now = s.Now
	})
	return &s.gate
}

// oauthFlowState is transient (never persisted) per-account OAuth
// progress: a Google device code awaiting user approval, or a Dropbox
// PKCE verifier awaiting the user-pasted code. Losing this on hub
// restart just means the user retries Connect (SPEC-v0.7 §3 design
// decision 4, notes/v07-storage.md).
type oauthFlowState struct {
	provider models.StorageAccountProvider

	// Google device flow.
	deviceCode      string
	pollIntervalSec int
	expiresAt       time.Time
	cancelPoll      context.CancelFunc
	pollDone        chan struct{}
	pollMu          sync.Mutex
	pollResult      *deviceFlowResult // set once polling concludes; nil while in progress

	// Dropbox PKCE.
	pkceVerifier string
}

// deviceFlowResult is the terminal outcome of a Google device-flow
// background poll, read by handleStorageOAuthComplete.
type deviceFlowResult struct {
	status string // "connected" | "denied" | "expired" | "error"
	detail string
}

// storageIntervalDuration converts a models.StorageInterval to a
// time.Duration, defaulting to 1h for an unrecognized value.
func storageIntervalDuration(v models.StorageInterval) time.Duration {
	switch v {
	case models.StorageInterval15m:
		return 15 * time.Minute
	case models.StorageInterval1h:
		return time.Hour
	case models.StorageInterval6h:
		return 6 * time.Hour
	case models.StorageInterval24h:
		return 24 * time.Hour
	default:
		return time.Hour
	}
}

// resolveStorageInterval returns the effective storage-usage polling
// interval: the hub-side "storage_interval" setting if present and
// valid, otherwise s.opts.Storage.EnvInterval.
func (s *Server) resolveStorageInterval(ctx context.Context) models.StorageInterval {
	if s.opts.Storage == nil {
		return models.StorageIntervalDefault
	}
	raw, ok, err := s.store.GetSetting(ctx, SettingStorageInterval)
	if err == nil && ok {
		v := models.StorageInterval(raw)
		if models.ValidStorageInterval(v) {
			return v
		}
	}
	if models.ValidStorageInterval(s.opts.Storage.EnvInterval) {
		return s.opts.Storage.EnvInterval
	}
	return models.StorageIntervalDefault
}

// RunStorageLoop runs the storage-usage account poller on a timer that
// re-reads the effective interval before every tick (mirroring
// RunBillingLoop). It blocks until ctx is canceled. A nil
// Options.Storage makes this a no-op.
func (s *Server) RunStorageLoop(ctx context.Context) {
	if s.opts.Storage == nil {
		return
	}
	poller.Loop(ctx,
		func() time.Duration { return storageIntervalDuration(s.resolveStorageInterval(ctx)) },
		s.collectStorageOnce,
	)
}

// collectStorageOnce polls every connected storage account once,
// persisting a merged snapshot per account (preserving the last
// successful quota on a quiet-skip status, mirroring
// billing.mergeSnapshot) and logging each classified failure at most
// once per UTC day per account.
func (s *Server) collectStorageOnce(ctx context.Context) {
	if s.opts.Storage == nil {
		return
	}
	s.opts.Storage.runMu.Guard(func() {
		s.collectStorageAccounts(ctx)
	})
	s.opts.Storage.refreshGate().Record()
}

func (s *Server) collectStorageAccounts(ctx context.Context) {
	accounts, err := s.store.ListStorageAccounts(ctx)
	if err != nil {
		s.logger.Error("storage: list accounts for collection failed", "error", err)
		return
	}
	now := s.opts.Storage.now()
	for _, acct := range accounts {
		s.collectOneStorageAccount(ctx, acct, now)
	}
	s.evaluateStorageAlerts(ctx, now)
}

// evaluateStorageAlerts runs SPEC-v0.7 §3's storage_usage_pct alert
// rules against every account's freshly collected snapshot, if the
// configured AlertEngine additionally implements StorageAlertEvaluator
// (see alertengine.go's doc comment on why this is a type assertion
// rather than a shared-interface method). A nil Options.Alerting, or
// one that doesn't implement the optional interface, silently skips
// storage alerting — the same "no engine configured" tolerance every
// other alert path already has.
func (s *Server) evaluateStorageAlerts(ctx context.Context, now time.Time) {
	evaluator, ok := s.opts.Alerting.(StorageAlertEvaluator)
	if s.opts.Alerting == nil || !ok {
		return
	}

	snapshots, err := s.store.ListStorageAccountSnapshots(ctx)
	if err != nil {
		s.logger.Error("storage: list snapshots for alert evaluation failed", "error", err)
		return
	}

	accounts, err := s.store.ListStorageAccounts(ctx)
	if err != nil {
		s.logger.Error("storage: list accounts for alert evaluation failed", "error", err)
		return
	}
	names := make(map[int64]string, len(accounts))
	for _, a := range accounts {
		names[a.ID] = a.Name
	}

	usages := make([]alerting.StorageAccountUsage, 0, len(snapshots))
	for _, snap := range snapshots {
		if snap.Status != models.StorageAccountOK {
			// No usable percentage on a quiet-skip snapshot — evaluating
			// a stale/never-succeeded quota against a threshold would
			// either compare against a zero value or a preserved-but-
			// possibly-outdated one; skip until the next successful
			// collection, matching how host_down's own liveness check
			// (not this metric) is the appropriate way to alert on a
			// disconnected/erroring account instead.
			continue
		}
		name := snap.AccountName
		if name == "" {
			name = names[snap.AccountID]
		}
		usages = append(usages, alerting.StorageAccountUsage{
			AccountID:   snap.AccountID,
			AccountName: name,
			UsedPercent: snap.Quota.UsedPercent(),
			Unlimited:   snap.Quota.Unlimited,
		})
	}

	if err := evaluator.EvaluateStorage(ctx, now, usages); err != nil {
		s.logger.Error("storage: evaluate storage alerts failed", "error", err)
	}
}

// collectOneStorageAccount polls a single account and persists the
// merged snapshot.
func (s *Server) collectOneStorageAccount(ctx context.Context, acct models.StorageAccount, now time.Time) {
	provider, ok := s.opts.Storage.storageRegistry().Get(string(acct.Provider))
	prev, prevErr := s.store.GetStorageAccountSnapshot(ctx, acct.ID)
	if prevErr != nil {
		s.logger.Error("storage: load previous snapshot failed", "account_id", acct.ID, "error", prevErr)
	}

	var fresh models.StorageAccountSnapshot
	fresh.AccountID = acct.ID

	if len(acct.Secret) == 0 {
		fresh.Status = models.StorageAccountPendingOAuth
		fresh.StatusDetail = "account has not completed the OAuth connection flow yet"
	} else if !ok {
		fresh.Status = models.StorageAccountError
		fresh.StatusDetail = "no provider registered for " + string(acct.Provider)
	} else {
		q, err := provider.FetchQuota(ctx, acct.Config, acct.Secret)
		if err != nil {
			status, detail := provider.Classify(err)
			fresh.Status = status
			fresh.StatusDetail = detail
			if s.opts.Storage.logGate.Allow(fmt.Sprintf("storage:%d", acct.ID)) {
				s.logger.Info("storage: account poll quiet-skip", "account_id", acct.ID, "provider", acct.Provider, "status", status, "detail", detail)
			}
		} else {
			fresh.Status = models.StorageAccountOK
			fresh.AccountEmail = q.AccountEmail
			fresh.AccountName = q.AccountName
			fresh.Quota = q.Quota
		}
	}

	merged := mergeStorageSnapshot(prev, fresh, now.Unix())
	merged.Stale = models.StorageAccountStale(merged.LastSuccessAt, now.Unix(), s.resolveStorageInterval(ctx))

	if err := s.store.SetStorageAccountSnapshot(ctx, merged); err != nil {
		s.logger.Error("storage: save snapshot failed", "account_id", acct.ID, "error", err)
	}
}

// mergeStorageSnapshot mirrors billing's mergeSnapshot: fresh's
// Status/StatusDetail always win, LastAttemptAt is always now, but on
// anything other than StorageAccountOK the previous quota/account
// identity/CollectedAt/LastSuccessAt are carried forward untouched.
func mergeStorageSnapshot(prev, fresh models.StorageAccountSnapshot, nowUnix int64) models.StorageAccountSnapshot {
	out := fresh
	out.LastAttemptAt = nowUnix

	if fresh.Status == models.StorageAccountOK {
		out.LastSuccessAt = nowUnix
		out.CollectedAt = nowUnix
		return out
	}

	out.AccountEmail = prev.AccountEmail
	out.AccountName = prev.AccountName
	out.Quota = prev.Quota
	out.CollectedAt = prev.CollectedAt
	out.LastSuccessAt = prev.LastSuccessAt
	return out
}

// storageRefreshAllowed reports whether a manual POST
// /api/v1/storage/refresh should be allowed right now (SPEC-v0.7 §3's
// 1-minute throttle), and if not, how long the caller must wait.
func (s *StorageRuntime) refreshAllowed() (allowed bool, retryAfter time.Duration) {
	return s.refreshGate().Allow()
}

// flowState returns the in-progress OAuth flow for accountID, creating
// one if none exists.
func (s *StorageRuntime) flowState(accountID int64, provider models.StorageAccountProvider) *oauthFlowState {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	if s.flows == nil {
		s.flows = make(map[int64]*oauthFlowState)
	}
	f, ok := s.flows[accountID]
	if !ok || f.provider != provider {
		f = &oauthFlowState{provider: provider}
		s.flows[accountID] = f
	}
	return f
}

// clearFlow removes accountID's in-progress OAuth flow state (called on
// successful completion, terminal failure, or account deletion),
// canceling any in-flight Google device-flow polling goroutine first.
func (s *StorageRuntime) clearFlow(accountID int64) {
	s.flowMu.Lock()
	f, ok := s.flows[accountID]
	delete(s.flows, accountID)
	s.flowMu.Unlock()
	if ok && f.cancelPoll != nil {
		f.cancelPoll()
	}
}
