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
// sample for host. It computes the host's current-month egress/ingress
// usage (applying any hub-side limit overrides) and, for each direction
// independently, fires a notification for every newly attained level
// (per Store.MarkAlertSent's first-time semantics). It never fails the
// ingest request: all errors are logged.
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

	limits, err := s.store.GetHostLimits(ctx, host.ID)
	if err != nil {
		s.logger.Error("alerts: get host limits failed", "host_id", host.ID, "error", err)
		return
	}
	txLimit, rxLimit, _, _ := models.EffectiveLimits(host.EgressLimitBytes, limits)

	usage := models.ComputeEgress(month, tx, rx, txLimit, rxLimit, now)

	s.fireAlerts(ctx, host, month, models.DirectionOut, usage.Level, usage.Percent, usage)
	s.fireAlerts(ctx, host, month, models.DirectionIn, usage.RxLevel, usage.RxPercent, usage)
}

// fireAlerts marks and notifies every configured threshold newly
// attained by percent/level for host/month/dir, independently of the
// other direction.
func (s *Server) fireAlerts(ctx context.Context, host models.HostInfo, month string, dir models.Direction, level models.EgressLevel, percent float64, usage models.EgressUsage) {
	for _, lvl := range egressAlertLevels(level, percent) {
		first, err := s.store.MarkAlertSent(ctx, host.ID, month, dir, lvl)
		if err != nil {
			s.logger.Error("alerts: mark alert sent failed", "host_id", host.ID, "direction", dir, "level", lvl, "error", err)
			return
		}
		if !first {
			continue
		}
		s.notifyEgressAlert(host, usage, dir, lvl)
	}
}

// egressAlertLevels returns every configured threshold reached by
// level/percent in ascending severity order. MarkAlertSent deduplicates
// levels previously sent during an earlier ingest.
func egressAlertLevels(level models.EgressLevel, percent float64) []models.EgressLevel {
	if level == models.EgressOK {
		return nil
	}

	levels := []models.EgressLevel{models.EgressWarning}
	if percent >= models.EgressCriticalPercent {
		levels = append(levels, models.EgressCritical)
	}
	if percent >= 100 {
		levels = append(levels, models.EgressExceeded)
	}
	return levels
}

// directionLabel renders dir for alert titles/messages.
func directionLabel(dir models.Direction) string {
	if dir == models.DirectionIn {
		return "ingress (inbound)"
	}
	return "egress (outbound)"
}

func (s *Server) notifyEgressAlert(host models.HostInfo, usage models.EgressUsage, dir models.Direction, level models.EgressLevel) {
	label := directionLabel(dir)
	bytesUsed, limitBytes := usage.TxBytes, usage.LimitBytes
	percent := usage.Percent
	if dir == models.DirectionIn {
		bytesUsed, limitBytes = usage.RxBytes, usage.RxLimitBytes
		percent = usage.RxPercent
	}

	title := fmt.Sprintf("cloud-pulse: %s %s %s", host.Hostname, label, level)
	message := fmt.Sprintf(
		"Host %s %s usage is %s: %s / %s (%.1f%%) this month (%s).",
		host.Hostname, label, level,
		models.HumanBytes(bytesUsed), models.HumanBytes(limitBytes),
		percent, usage.Month,
	)
	s.logger.Warn("egress alert",
		"host_id", host.ID,
		"hostname", host.Hostname,
		"direction", dir,
		"level", level,
		"percent", percent,
		"month", usage.Month,
	)

	url, _, err := s.effectiveWebhookURL(context.Background())
	if err != nil {
		s.logger.Error("alerts: resolve webhook url failed", "host_id", host.ID, "error", err)
		return
	}
	// An explicitly injected notifier (via New's notifier parameter,
	// used by tests and by simple single-webhook wiring) fires
	// regardless of whether a URL is configured in settings/env, since
	// it already encapsulates its own destination. Otherwise, a
	// dynamically resolved notifier requires a non-empty effective URL.
	if s.notifier == nil && url == "" {
		return
	}
	notifier := s.resolveNotifier(url)
	if notifier == nil {
		return
	}

	s.alertWG.Add(1)
	go func() {
		defer s.alertWG.Done()
		notifyCtx, cancel := context.WithTimeout(context.Background(), alertNotifyTimeout)
		defer cancel()
		if err := notifier.Notify(notifyCtx, title, message); err != nil {
			s.logger.Error("alerts: notify failed", "host_id", host.ID, "direction", dir, "level", level, "error", err)
		}
	}()
}
