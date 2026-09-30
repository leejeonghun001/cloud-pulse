package hub

import (
	"context"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// rollupInterval is how often the store's rollup is run.
const rollupInterval = 1 * time.Minute

// pruneInterval is how often the store's retention pruning is run.
const pruneInterval = 1 * time.Hour

// alertEvalInterval is how often the alerting engine re-evaluates every
// rule against every host, independent of ingest-triggered evaluation
// (SPEC-v0.5 §B: needed for host_down and sustained-window transitions
// that must fire even without a fresh sample).
const alertEvalInterval = 30 * time.Second

// maxCollectTimeout caps how long a single collector run may take,
// regardless of CloudInterval.
const maxCollectTimeout = 2 * time.Minute

// RunBackground runs the hub's periodic maintenance loops (rollup,
// prune, and cloud bucket collection) until ctx is canceled. It blocks
// until all loops have stopped.
func (s *Server) RunBackground(ctx context.Context) {
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.runRollupLoop(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.runPruneLoop(ctx)
	}()

	if len(s.collectors) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runCloudLoop(ctx)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.runUpdateCheckLoop(ctx, s.updateFirstDelayOrDefault(), s.updateIntervalOrDefault())
	}()

	if s.opts.Alerting != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runAlertEvalLoop(ctx)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.runUpdateJobTimeoutLoop(ctx)
	}()

	wg.Wait()
}

// updateFirstDelayOrDefault returns s.updateFirstDelay if a test has set
// it (non-zero), otherwise the real firstUpdateCheckDelay.
func (s *Server) updateFirstDelayOrDefault() time.Duration {
	if s.updateFirstDelay > 0 {
		return s.updateFirstDelay
	}
	return firstUpdateCheckDelay
}

// updateIntervalOrDefault returns s.updateInterval if a test has set it
// (non-zero), otherwise the real updateCheckInterval.
func (s *Server) updateIntervalOrDefault() time.Duration {
	if s.updateInterval > 0 {
		return s.updateInterval
	}
	return updateCheckInterval
}

func (s *Server) runRollupLoop(ctx context.Context) {
	ticker := time.NewTicker(rollupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.opts.now()
			if err := s.store.Rollup(ctx, now); err != nil {
				s.logger.Error("scheduler: rollup failed", "error", err)
			}
		}
	}
}

func (s *Server) runPruneLoop(ctx context.Context) {
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.opts.now()
			if err := s.store.Prune(ctx, now); err != nil {
				s.logger.Error("scheduler: prune failed", "error", err)
			}
			if err := s.store.PruneAlertEvents(ctx, now); err != nil {
				s.logger.Error("scheduler: prune alert events failed", "error", err)
			}
			// SPEC-v0.6 §3 개선 c: audit_log entries older than
			// models.AuditRetentionDays (400 days) are pruned on the
			// same maintenance tick as every other retention window.
			if err := s.store.PruneAuditEntries(ctx, now); err != nil {
				s.logger.Error("scheduler: prune audit entries failed", "error", err)
			}
			s.limiter.cleanup()
		}
	}
}

// runAlertEvalLoop periodically re-evaluates every enabled alert rule
// against every host, independent of ingest-triggered evaluation (see
// afterIngest in alerts.go). Only started when s.opts.Alerting is
// non-nil (RunBackground checks this before spawning the goroutine that
// calls this function).
func (s *Server) runAlertEvalLoop(ctx context.Context) {
	ticker := time.NewTicker(alertEvalInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.opts.now()
			hosts, err := s.hostSnapshots(ctx, now)
			if err != nil {
				s.logger.Error("scheduler: build host snapshots for alert evaluation failed", "error", err)
				continue
			}
			if err := s.opts.Alerting.Evaluate(ctx, now, hosts); err != nil {
				s.logger.Error("scheduler: alert evaluation failed", "error", err)
			}
		}
	}
}

func (s *Server) runCloudLoop(ctx context.Context) {
	interval := s.opts.CloudInterval
	if interval <= 0 {
		interval = 15 * time.Minute
	}

	s.collectAll(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.collectAll(ctx)
		}
	}
}

// collectTimeout returns the timeout for a single collector run: the
// smaller of CloudInterval and maxCollectTimeout.
// updateJobTimeoutCheckInterval is how often
// runUpdateJobTimeoutLoop scans for in_progress remote-update jobs that
// have exceeded updateJobTimeout (SPEC-v0.6 §2).
const updateJobTimeoutCheckInterval = 1 * time.Minute

// runUpdateJobTimeoutLoop periodically fails any in_progress
// remote-update job that has not received a status report within
// updateJobTimeout (SPEC-v0.6 §2 step 5).
func (s *Server) runUpdateJobTimeoutLoop(ctx context.Context) {
	ticker := time.NewTicker(updateJobTimeoutCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkUpdateJobTimeouts(ctx, s.opts.now())
		}
	}
}

func (s *Server) collectTimeout() time.Duration {
	interval := s.opts.CloudInterval
	if interval <= 0 || interval > maxCollectTimeout {
		return maxCollectTimeout
	}
	return interval
}

// collectAll runs every registered collector once, saving partial
// results even when a collector returns an error, and records each
// collector's status.
func (s *Server) collectAll(ctx context.Context) {
	timeout := s.collectTimeout()
	now := s.opts.now()

	for _, c := range s.collectors {
		collectCtx, cancel := context.WithTimeout(ctx, timeout)
		stats, err := c.Collect(collectCtx)
		cancel()

		if len(stats) > 0 {
			if saveErr := s.store.SaveBucketStats(ctx, stats); saveErr != nil {
				s.logger.Error("scheduler: save bucket stats failed", "collector", c.Name(), "error", saveErr)
			}
		}

		status := models.CollectorStatus{
			Name:    c.Name(),
			Enabled: true,
			LastRun: now.Unix(),
		}
		if err != nil {
			status.LastError = err.Error()
			s.logger.Error("scheduler: collector failed", "collector", c.Name(), "error", err)
		}

		s.statusMu.Lock()
		s.status[c.Name()] = status
		s.statusMu.Unlock()
	}
}
