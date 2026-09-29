package chart

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sort"
	"time"
)

// Width and Height are the fixed dimensions of every rendered chart.
const (
	Width  = 800
	Height = 400
)

// plotMargin is the fixed padding, in pixels, around the plot area on
// every side (room for axis labels/title).
const (
	marginLeft   = 84 // fits the widest value label ("100.0%" at textScale 2) plus a gap
	marginRight  = 20
	marginTop    = 50
	marginBottom = 40
)

// Point is one sample of the metric being charted.
type Point struct {
	// Time is the sample's timestamp.
	Time time.Time
	// Value is the metric's value at Time, in the metric's own unit
	// (matching AlertRule.Threshold's unit).
	Value float64
}

// Params configures Render. All fields are required unless noted.
type Params struct {
	// Title is drawn at the top, e.g. "host-1 · CPU 93.4% > 90% for 5m".
	Title string
	// Points is the metric's history, in chronological order. Render
	// sorts a copy defensively, so callers don't need to pre-sort.
	Points []Point
	// Threshold is the rule's configured threshold, drawn as a dashed
	// horizontal line.
	Threshold float64
	// BreachStart and BreachEnd bound the shaded "this is where the
	// condition was breaching" window. Zero BreachEnd means "still
	// breaching as of the last point" (shades through the right edge).
	BreachStart time.Time
	BreachEnd   time.Time
	// Unit is appended to value-axis labels, e.g. "%".
	Unit string
}

// Colors used by the dark theme. Chosen for contrast against the dark
// background and against each other (WCAG-AA-ish, not measured
// formally — this is a notification image, not the dashboard UI).
var (
	colorBackground = color.RGBA{0x12, 0x16, 0x1f, 0xff}
	colorGrid       = color.RGBA{0x2a, 0x31, 0x3d, 0xff}
	colorAxis       = color.RGBA{0x8a, 0x93, 0xa3, 0xff}
	colorTitle      = color.RGBA{0xe8, 0xea, 0xed, 0xff}
	colorLine       = color.RGBA{0x4c, 0xa6, 0xff, 0xff}
	colorThreshold  = color.RGBA{0xf0, 0x5a, 0x5a, 0xff}
	// colorBreachFill is NON-premultiplied (color.NRGBA): a premultiplied
	// color.RGBA with R > A is invalid and draw.Over then paints an
	// opaque, over-saturated band that hides the grid and threshold line.
	colorBreachFill = color.NRGBA{0xf0, 0x5a, 0x5a, 0x33}
)

// Render draws p as an 800x400 dark-themed PNG line chart: the metric
// line, a dashed threshold line, a shaded breach window, axis ticks,
// and a title — then encodes it as PNG. Output is deterministic for a
// given p (no wall-clock/randomness inside Render itself); golden-hash
// tests rely on this.
func Render(p Params) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	fillRect(img, 0, 0, Width, Height, colorBackground)

	points := make([]Point, len(p.Points))
	copy(points, p.Points)
	sort.Slice(points, func(i, j int) bool { return points[i].Time.Before(points[j].Time) })

	plot := image.Rect(marginLeft, marginTop, Width-marginRight, Height-marginBottom)

	minT, maxT, minV, maxV := bounds(points, p.Threshold)

	drawBreachWindow(img, plot, minT, maxT, p.BreachStart, p.BreachEnd)
	drawGrid(img, plot)
	drawThresholdLine(img, plot, minV, maxV, p.Threshold)
	drawSeries(img, plot, points, minT, maxT, minV, maxV)
	drawAxes(img, plot, minT, maxT, minV, maxV, p.Unit)
	drawText(img, marginLeft, 16, p.Title, colorTitle)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("chart: encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// bounds computes the time/value axis ranges for points, always
// including threshold in the value range so the threshold line is
// never clipped, and padding the value range by 10% so the series
// doesn't touch the plot edges.
func bounds(points []Point, threshold float64) (minT, maxT time.Time, minV, maxV float64) {
	if len(points) == 0 {
		// A fixed range keeps empty-state output deterministic too; callers
		// can safely use it in tests and notification previews without a
		// wall-clock-dependent PNG hash.
		end := time.Unix(0, 0).UTC()
		return end.Add(-time.Hour), end, 0, threshold + 1
	}
	minT, maxT = points[0].Time, points[0].Time
	minV, maxV = points[0].Value, points[0].Value
	for _, pt := range points {
		if pt.Time.Before(minT) {
			minT = pt.Time
		}
		if pt.Time.After(maxT) {
			maxT = pt.Time
		}
		if pt.Value < minV {
			minV = pt.Value
		}
		if pt.Value > maxV {
			maxV = pt.Value
		}
	}
	if threshold < minV {
		minV = threshold
	}
	if threshold > maxV {
		maxV = threshold
	}
	if maxT.Equal(minT) {
		maxT = minT.Add(time.Minute)
	}
	span := maxV - minV
	if span <= 0 {
		span = 1
	}
	pad := span * 0.1
	minV -= pad
	maxV += pad
	if minV < 0 && minV > -pad {
		minV = 0 // avoid a barely-negative axis for naturally non-negative metrics like percent/load
	}
	return minT, maxT, minV, maxV
}

// xFor maps t into plot's horizontal pixel range.
func xFor(plot image.Rectangle, minT, maxT time.Time, t time.Time) int {
	span := maxT.Sub(minT)
	if span <= 0 {
		return plot.Min.X
	}
	frac := float64(t.Sub(minT)) / float64(span)
	return plot.Min.X + int(frac*float64(plot.Dx()))
}

// yFor maps v into plot's vertical pixel range (inverted — larger
// values draw higher/smaller Y).
func yFor(plot image.Rectangle, minV, maxV, v float64) int {
	span := maxV - minV
	if span <= 0 {
		return plot.Max.Y
	}
	frac := (v - minV) / span
	return plot.Max.Y - int(frac*float64(plot.Dy()))
}

// drawGrid draws the plot area's border and a light horizontal
// midline grid.
func drawGrid(img *image.RGBA, plot image.Rectangle) {
	const gridLines = 4
	for i := 0; i <= gridLines; i++ {
		y := plot.Min.Y + i*plot.Dy()/gridLines
		drawHLine(img, plot.Min.X, plot.Max.X, y, colorGrid)
	}
	drawHLine(img, plot.Min.X, plot.Max.X, plot.Min.Y, colorAxis)
	drawHLine(img, plot.Min.X, plot.Max.X, plot.Max.Y, colorAxis)
	drawVLine(img, plot.Min.X, plot.Min.Y, plot.Max.Y, colorAxis)
	drawVLine(img, plot.Max.X, plot.Min.Y, plot.Max.Y, colorAxis)
}

// drawBreachWindow shades [breachStart, breachEnd) within plot. A
// zero breachStart means there is no breach window to draw. A zero
// breachEnd shades through the right edge of the plot.
func drawBreachWindow(img *image.RGBA, plot image.Rectangle, minT, maxT, breachStart, breachEnd time.Time) {
	if breachStart.IsZero() {
		return
	}
	x0 := xFor(plot, minT, maxT, breachStart)
	x1 := plot.Max.X
	if !breachEnd.IsZero() {
		x1 = xFor(plot, minT, maxT, breachEnd)
	}
	if x1 < x0 {
		x0, x1 = x1, x0
	}
	fillRect(img, x0, plot.Min.Y, x1-x0, plot.Dy(), colorBreachFill)
}

// drawThresholdLine draws a horizontal dashed line at value threshold.
func drawThresholdLine(img *image.RGBA, plot image.Rectangle, minV, maxV, threshold float64) {
	y := yFor(plot, minV, maxV, threshold)
	const dashLen, gapLen = 6, 4
	x := plot.Min.X
	for x < plot.Max.X {
		end := x + dashLen
		if end > plot.Max.X {
			end = plot.Max.X
		}
		drawHLine(img, x, end, y, colorThreshold)
		x = end + gapLen
	}
}

// drawSeries draws the metric line connecting points, in plot's
// coordinate space.
func drawSeries(img *image.RGBA, plot image.Rectangle, points []Point, minT, maxT time.Time, minV, maxV float64) {
	if len(points) == 0 {
		return
	}
	prevX, prevY := xFor(plot, minT, maxT, points[0].Time), yFor(plot, minV, maxV, points[0].Value)
	if len(points) == 1 {
		fillRect(img, prevX-1, prevY-1, 3, 3, colorLine)
		return
	}
	for _, pt := range points[1:] {
		x, y := xFor(plot, minT, maxT, pt.Time), yFor(plot, minV, maxV, pt.Value)
		drawLine(img, prevX, prevY, x, y, colorLine)
		prevX, prevY = x, y
	}
}
