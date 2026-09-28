package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// hostInfoRefreshInterval is how often Run refreshes the host info sent
// with each flush (static/slow-changing metadata such as uptime/boot
// time).
const hostInfoRefreshInterval = 10 * time.Minute

// Run drives the agent's collect/report loop: it collects a Sample and
// enqueues it into r immediately, then every interval, until ctx is
// canceled. hostInfo is invoked at startup and every 10 minutes thereafter
// to refresh the reported host metadata. Flush failures are logged and
// retried on the next tick; the in-memory buffer absorbs hub outages.
// Run returns nil when ctx is canceled.
func Run(ctx context.Context, c *Collector, r *Reporter, hostInfo func(context.Context) models.HostInfo, interval time.Duration, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	info := hostInfo(ctx)
	lastInfoRefresh := time.Now()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	tick := func() {
		if time.Since(lastInfoRefresh) >= hostInfoRefreshInterval {
			info = hostInfo(ctx)
			lastInfoRefresh = time.Now()
		}

		sample, err := c.Collect(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			logger.WarnContext(ctx, "collect failed", "error", err)
			return
		}
		r.Enqueue(sample)

		if err := r.Flush(ctx, info); err != nil {
			if errors.Is(err, ErrUnauthorized) {
				logger.WarnContext(ctx, "hub rejected report: unauthorized", "error", err)
				return
			}
			logger.WarnContext(ctx, "flush failed, will retry next tick", "error", err)
		}
	}

	tick()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			tick()
		}
	}
}
