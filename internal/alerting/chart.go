package alerting

import (
	"context"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting/chart"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// chartImageName is the filename every rendered alert chart is attached
// as, matching SPEC-v0.5 §B's example ("chart.png") and the Discord
// sender's "attachment://chart.png" embed-image convention.
const chartImageName = "chart.png"

// chartMinWindow is the minimum lookback window for a chart's metric
// history, applied when rule.DurationSec*3 would otherwise be smaller
// (SPEC-v0.5 §B: "last max(DurationSec*3, 1h) window").
const chartMinWindow = time.Hour

// buildChartImage renders a PNG chart of rule's metric history for host
// around ev, returning nil (not an error) when the metric has no
// chartable series (egress/host_down — percentage/boolean values with
// no raw time series to plot) or when rendering fails; a chart is a
// best-effort visual enhancement, never a reason to fail or block
// notification delivery.
func (e *Engine) buildChartImage(ctx context.Context, now time.Time, rule models.AlertRule, host models.HostSnapshot, ev models.AlertEvent) []byte {
	if !isSeriesMetric(rule.Metric) {
		return nil
	}

	window := time.Duration(rule.DurationSec) * 3 * time.Second
	if window < chartMinWindow {
		window = chartMinWindow
	}

	from := now.Add(-window).Unix()
	series, err := e.store.QuerySeries(ctx, host.HostID, from, now.Unix())
	if err != nil {
		e.logger.Error("alerting: query series for chart failed", "host_id", host.HostID, "rule_id", rule.ID, "error", err)
		return nil
	}
	if series.Len() == 0 {
		return nil
	}

	points := make([]chart.Point, series.Len())
	for i, ts := range series.Timestamps {
		points[i] = chart.Point{
			Time:  time.Unix(ts, 0),
			Value: seriesValueFor(series, rule.Metric, i),
		}
	}

	breachStart, breachEnd := breachWindowFor(rule, ev, now)

	img, err := chart.Render(chart.Params{
		Title:       chartTitle(rule, host, ev),
		Points:      points,
		Threshold:   rule.Threshold,
		BreachStart: breachStart,
		BreachEnd:   breachEnd,
		Unit:        metricUnit(rule.Metric),
	})
	if err != nil {
		e.logger.Error("alerting: render chart failed", "host_id", host.HostID, "rule_id", rule.ID, "error", err)
		return nil
	}
	return img
}

// isSeriesMetric reports whether metric has a raw time series that
// Store.QuerySeries can return (cpu/memory/disk/load1) — egress
// percentages and host_down don't.
func isSeriesMetric(metric models.AlertMetric) bool {
	switch metric {
	case models.AlertMetricCPU, models.AlertMetricMemory, models.AlertMetricDisk, models.AlertMetricLoad1:
		return true
	default:
		return false
	}
}

// breachWindowFor returns the [start, end) window to shade as
// "breaching" on the chart: from the event's StartedAt through now for
// a still-firing event, or through ResolvedAt for a resolved one.
func breachWindowFor(rule models.AlertRule, ev models.AlertEvent, now time.Time) (start, end time.Time) {
	if ev.StartedAt == 0 {
		return time.Time{}, time.Time{}
	}
	start = time.Unix(ev.StartedAt, 0)
	if ev.State == models.AlertEventResolved && ev.ResolvedAt != 0 {
		end = time.Unix(ev.ResolvedAt, 0)
		return start, end
	}
	// Still firing: shade through the right edge of the plot.
	_ = now
	return start, time.Time{}
}

// chartTitle renders the chart's title line, matching SPEC-v0.5 §B's
// example: "<hostname> · CPU 93.4% > 90% for 5m".
func chartTitle(rule models.AlertRule, host models.HostSnapshot, ev models.AlertEvent) string {
	unit := metricUnit(rule.Metric)
	durationPart := ""
	if rule.DurationSec > 0 {
		durationPart = fmt.Sprintf(" for %s", formatDuration(rule.DurationSec))
	}
	return fmt.Sprintf("%s \u00b7 %s %s%s %s %s%s%s",
		host.Hostname, metricLabel(rule.Metric), formatValue(ev.Value), unit,
		string(rule.Operator), formatValue(rule.Threshold), unit, durationPart)
}

// metricLabel returns a short human label for metric, used in the chart
// title.
func metricLabel(metric models.AlertMetric) string {
	switch metric {
	case models.AlertMetricCPU:
		return "CPU"
	case models.AlertMetricMemory:
		return "Memory"
	case models.AlertMetricDisk:
		return "Disk"
	case models.AlertMetricLoad1:
		return "Load1"
	default:
		return string(metric)
	}
}

// formatDuration renders seconds as a short "Xm"/"Xh"/"Xs" label for the
// chart title.
func formatDuration(seconds int) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d%time.Hour == 0 && d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%time.Minute == 0 && d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}
