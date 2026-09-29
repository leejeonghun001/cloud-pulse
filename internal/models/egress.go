package models

import (
	"fmt"
	"time"
)

// humanBytesUnits are the IEC binary unit suffixes above bytes, in
// ascending order.
var humanBytesUnits = [...]string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}

// HumanBytes formats b using IEC binary units (e.g. "1.5 GiB"), used in
// alert messages. Values under 1024 bytes are formatted as "N B".
func HumanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(b)/float64(div), humanBytesUnits[exp])
}

// EgressLevel classifies current egress usage against a limit.
type EgressLevel string

// Egress usage levels.
const (
	EgressOK       EgressLevel = "ok"
	EgressWarning  EgressLevel = "warning"
	EgressCritical EgressLevel = "critical"
	EgressExceeded EgressLevel = "exceeded"
)

// Percent thresholds for EgressLevel classification.
const (
	EgressWarnPercent     = 80
	EgressCriticalPercent = 95
)

// Severity returns an ordinal ranking of l: ok=0, warning=1, critical=2,
// exceeded=3. Unknown levels return 0.
func (l EgressLevel) Severity() int {
	switch l {
	case EgressWarning:
		return 1
	case EgressCritical:
		return 2
	case EgressExceeded:
		return 3
	default:
		return 0
	}
}

// EgressUsage is the computed egress/ingress usage for a host during a
// calendar month. Tx*/Percent/Level/ProjectedTxBytes describe outbound
// traffic (unchanged, existing billing-limit semantics); Rx* fields mirror
// the same shape for inbound traffic, which has its own, independent,
// optional limit.
type EgressUsage struct {
	// Month is "YYYY-MM" in the UTC calendar.
	Month      string      `json:"month"`
	TxBytes    uint64      `json:"tx_bytes"`
	RxBytes    uint64      `json:"rx_bytes"`
	LimitBytes uint64      `json:"limit_bytes"`
	Percent    float64     `json:"percent"`
	Level      EgressLevel `json:"level"`
	// ProjectedTxBytes is a linear projection of TxBytes to the end of the
	// month.
	ProjectedTxBytes uint64 `json:"projected_tx_bytes"`

	// RxLimitBytes is the inbound limit in bytes; 0 means unlimited.
	RxLimitBytes uint64      `json:"rx_limit_bytes"`
	RxPercent    float64     `json:"rx_percent"`
	RxLevel      EgressLevel `json:"rx_level"`
	// ProjectedRxBytes is a linear projection of RxBytes to the end of
	// the month, computed identically to ProjectedTxBytes.
	ProjectedRxBytes uint64 `json:"projected_rx_bytes"`

	// LimitSource identifies the origin of LimitBytes (outbound):
	// "agent" (agent-reported/provider default) or "hub" (hub override).
	LimitSource string `json:"limit_source"`
	// RxLimitSource identifies the origin of RxLimitBytes: "none" (no
	// limit configured) or "hub" (hub override).
	RxLimitSource string `json:"rx_limit_source"`
}

// EgressRecord is a stored per-host, per-month egress accumulator.
type EgressRecord struct {
	HostID  string `json:"host_id"`
	Month   string `json:"month"`
	TxBytes uint64 `json:"tx_bytes"`
	RxBytes uint64 `json:"rx_bytes"`
}

// MonthOf returns t's UTC calendar month as "YYYY-MM".
func MonthOf(t time.Time) string {
	return t.UTC().Format("2006-01")
}

// MonthBounds parses month ("YYYY-MM") and returns the half-open UTC
// interval [start, end) spanning that calendar month.
func MonthBounds(month string) (start, end time.Time, err error) {
	start, err = time.Parse("2006-01", month)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("models: parse month %q: %w", month, err)
	}
	start = start.UTC()
	end = start.AddDate(0, 1, 0)
	return start, end, nil
}

// LevelFor classifies percent (usage as a percentage of limit) into an
// EgressLevel. A limit of 0 (unlimited) is always "ok".
func LevelFor(percent float64, limit uint64) EgressLevel {
	if limit == 0 {
		return EgressOK
	}
	switch {
	case percent >= 100:
		return EgressExceeded
	case percent >= EgressCriticalPercent:
		return EgressCritical
	case percent >= EgressWarnPercent:
		return EgressWarning
	default:
		return EgressOK
	}
}

// ComputeEgress computes the EgressUsage for the given month, byte
// counters, and limits, projecting tx and rx bytes linearly to the end of
// the month based on now.
//
// Percent = tx/txLimit*100 (0 when txLimit is 0); RxPercent mirrors this
// for rx/rxLimit. Level/RxLevel are derived via LevelFor. LimitSource is
// always "agent" and RxLimitSource is always "none" — callers that apply a
// hub override set these fields themselves after calling ComputeEgress.
// ProjectedTxBytes/ProjectedRxBytes extrapolate tx/rx across the full
// month duration using elapsed time since the month start, clamped to
// [1h, monthDuration]; if now is at or past the month's end, or month is
// unparsable, the projections equal tx/rx respectively.
func ComputeEgress(month string, tx, rx, txLimit, rxLimit uint64, now time.Time) EgressUsage {
	usage := EgressUsage{
		Month:         month,
		TxBytes:       tx,
		RxBytes:       rx,
		LimitBytes:    txLimit,
		RxLimitBytes:  rxLimit,
		LimitSource:   "agent",
		RxLimitSource: "none",
	}

	usage.Percent = percentOf(tx, txLimit)
	usage.Level = LevelFor(usage.Percent, txLimit)
	usage.RxPercent = percentOf(rx, rxLimit)
	usage.RxLevel = LevelFor(usage.RxPercent, rxLimit)

	start, end, err := MonthBounds(month)
	if err != nil {
		usage.ProjectedTxBytes = tx
		usage.ProjectedRxBytes = rx
		return usage
	}

	now = now.UTC()
	if !now.Before(end) {
		usage.ProjectedTxBytes = tx
		usage.ProjectedRxBytes = rx
		return usage
	}

	usage.ProjectedTxBytes = projectBytes(tx, start, end, now)
	usage.ProjectedRxBytes = projectBytes(rx, start, end, now)
	return usage
}

// percentOf returns value/limit*100, or 0 when limit is 0 (unlimited).
func percentOf(value, limit uint64) float64 {
	if limit == 0 {
		return 0
	}
	return float64(value) / float64(limit) * 100
}

// projectBytes linearly extrapolates value across [start, end) based on
// elapsed time since start through now, clamping elapsed to
// [1h, end-start]. Callers must ensure now is before end.
func projectBytes(value uint64, start, end, now time.Time) uint64 {
	monthDuration := end.Sub(start)
	elapsed := now.Sub(start)
	const minElapsed = time.Hour
	if elapsed < minElapsed {
		elapsed = minElapsed
	}
	if elapsed > monthDuration {
		elapsed = monthDuration
	}

	projected := float64(value) * float64(monthDuration) / float64(elapsed)
	return uint64(projected)
}
