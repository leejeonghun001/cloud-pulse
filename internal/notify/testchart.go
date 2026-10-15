package notify

import (
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting/chart"
)

// GeneratedTestChartPNG renders a deterministic demo chart, never a file,
// photo, host metric or other operator data. WhatsApp requires an image even
// for connectivity checks and channel-failure fallback notifications.
func GeneratedTestChartPNG() ([]byte, error) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	return chart.Render(chart.Params{
		Title: "cloud-pulse · channel connectivity demo", Threshold: 90, Unit: "%",
		Points: []chart.Point{
			{Time: base.Add(-15 * time.Minute), Value: 48},
			{Time: base.Add(-10 * time.Minute), Value: 62},
			{Time: base.Add(-5 * time.Minute), Value: 84},
			{Time: base, Value: 93},
		},
		BreachStart: base.Add(-5 * time.Minute),
	})
}
