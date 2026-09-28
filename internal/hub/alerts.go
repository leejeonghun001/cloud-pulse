package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// alertNotifyTimeout bounds how long a single Notify call may run.
const alertNotifyTimeout = 10 * time.Second

// afterIngest is called after successfully ingesting at least one valid
// sample for host. It computes the host's current-month egress usage
// and, if the egress level has newly crossed into warning/critical/
// exceeded (per Store.MarkAlertSent's first-time semantics), logs a
// warning and fires a notification in the background. It never fails
// the ingest request: all errors are logged.
func (s *Server) afterIngest(host models.HostInfo, now time.Time) {
	ctx := context.Background()
	month := models.MonthOf(now)

	records, err := s.store.ListEgress(ctx, month)
	if err != nil {
		s.logger.Error("alerts: list egress failed", "host_id", host.ID, "error", err)
		return
	}

	var tx, rx uint64
	found := false
	for _, r := range records {
		if r.HostID == host.ID {
			tx, rx = r.TxBytes, r.RxBytes
			found = true
			break
		}
	}
	if !found {
		return
	}

	usage := models.ComputeEgress(month, tx, rx, host.EgressLimitBytes, now)
	if usage.Level == models.EgressOK {
		return
	}

	first, err := s.store.MarkAlertSent(ctx, host.ID, month, usage.Level)
	if err != nil {
		s.logger.Error("alerts: mark alert sent failed", "host_id", host.ID, "level", usage.Level, "error", err)
		return
	}
	if !first {
		return
	}

	title := fmt.Sprintf("cloud-pulse: %s egress %s", host.Hostname, usage.Level)
	message := fmt.Sprintf(
		"Host %s egress usage is %s: %s / %s (%.1f%%) this month (%s).",
		host.Hostname, usage.Level,
		models.HumanBytes(usage.TxBytes), models.HumanBytes(usage.LimitBytes),
		usage.Percent, usage.Month,
	)
	s.logger.Warn("egress alert",
		"host_id", host.ID,
		"hostname", host.Hostname,
		"level", usage.Level,
		"percent", usage.Percent,
		"month", usage.Month,
	)

	if s.notifier == nil {
		return
	}

	s.alertWG.Add(1)
	go func() {
		defer s.alertWG.Done()
		notifyCtx, cancel := context.WithTimeout(context.Background(), alertNotifyTimeout)
		defer cancel()
		if err := s.notifier.Notify(notifyCtx, title, message); err != nil {
			s.logger.Error("alerts: notify failed", "host_id", host.ID, "level", usage.Level, "error", err)
		}
	}()
}
