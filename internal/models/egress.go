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

// EgressUsage is the computed egress usage for a host during a calendar
// month.
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

// ComputeEgress computes the EgressUsage for the given month, byte counters,
// and limit, projecting tx bytes linearly to the end of the month based on
// now.
//
// Percent = tx/limit*100 (0 when limit is 0). Level is derived via LevelFor.
// ProjectedTxBytes extrapolates tx across the full month duration using
// elapsed time since the month start, clamped to [1h, monthDuration]; if now
// is at or past the month's end, or month is unparsable, the projection
// equals tx.
func ComputeEgress(month string, tx, rx, limit uint64, now time.Time) EgressUsage {
	usage := EgressUsage{
		Month:      month,
		TxBytes:    tx,
		RxBytes:    rx,
		LimitBytes: limit,
	}

	if limit == 0 {
		usage.Percent = 0
	} else {
		usage.Percent = float64(tx) / float64(limit) * 100
	}
	usage.Level = LevelFor(usage.Percent, limit)

	start, end, err := MonthBounds(month)
	if err != nil {
		usage.ProjectedTxBytes = tx
		return usage
	}

	now = now.UTC()
	if !now.Before(end) {
		usage.ProjectedTxBytes = tx
		return usage
	}

	monthDuration := end.Sub(start)
	elapsed := now.Sub(start)
	const minElapsed = time.Hour
	if elapsed < minElapsed {
		elapsed = minElapsed
	}
	if elapsed > monthDuration {
		elapsed = monthDuration
	}

	projected := float64(tx) * float64(monthDuration) / float64(elapsed)
	usage.ProjectedTxBytes = uint64(projected)
	return usage
}
