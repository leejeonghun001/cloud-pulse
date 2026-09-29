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
