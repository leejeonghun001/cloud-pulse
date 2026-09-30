package chart

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sort"
)

// Bar is one labeled category in a BarParams chart, split into a
// "success" and "failure" sub-count so RenderBars can draw a stacked
// two-color bar per category (SPEC-v0.8 §3-1's update_batch_failed
// attachment: per-host succeeded/failed outcome).
type Bar struct {
	// Label is the category name drawn under its bar (ASCII only —
	// the embedded bitmap font has no other glyphs; see font.go's doc
	// comment). Truncated defensively by RenderBars if it would
	// overflow its column, never causing a render error.
	Label string
	// Success and Failure are the two stacked segment heights. Both
	// zero draws an empty (label-only) column rather than an error.
	Success int
	Failure int
}

// BarParams configures RenderBars. All fields are required unless
// noted.
type BarParams struct {
	// Title is drawn at the top, e.g. "Update batch batch-abc123 ·
	// 4/6 succeeded".
	Title string
	// Bars is the ordered list of categories to plot, left to right.
	// RenderBars does not sort or reorder these — callers control
	// display order (e.g. by OS, or by failure count).
	Bars []Bar
	// SuccessLabel/FailureLabel caption the two-color legend, e.g.
	// "Succeeded"/"Failed". ASCII only.
	SuccessLabel string
	FailureLabel string
}

// barColorSuccess/barColorFailure are the two stacked-segment fill
// colors, chosen for contrast against colorBackground and against
// each other (green/red, matching the dashboard's own cp-chip-ok/
// cp-chip-critical convention).
var (
	barColorSuccess = color.RGBA{0x3d, 0xd6, 0x8c, 0xff}
	barColorFailure = color.RGBA{0xf0, 0x5a, 0x5a, 0xff}
)

// barPlotMargin mirrors Render's marginLeft/Right/Top/Bottom but with
// extra bottom room for rotated-free horizontal bar labels (drawn
// upright below each bar, truncated rather than rotated — this is a
// small notification image, not an interactive chart).
const (
	barMarginLeft   = 50
	barMarginRight  = 20
	barMarginTop    = 50
	barMarginBottom = 60
)

// maxBarLabelChars bounds how many characters of a Bar.Label are drawn
// before truncating with a trailing ellipsis-like cut (no "…" glyph in
// the embedded ASCII-only font — see font.go — so truncation is a
// plain cut, not an ellipsis).
const maxBarLabelChars = 10

// RenderBars draws p as an 800x400 dark-themed PNG stacked bar chart:
// one two-color (success/failure) column per Bar, a value axis on the
// left, category labels along the bottom, a title, and a small
// legend — then encodes it as PNG. Output is deterministic for a
// given p (no wall-clock/randomness), matching Render's own
// determinism guarantee.
func RenderBars(p BarParams) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	fillRect(img, 0, 0, Width, Height, colorBackground)

	plot := image.Rect(barMarginLeft, barMarginTop, Width-barMarginRight, Height-barMarginBottom)

	maxTotal := barsMaxTotal(p.Bars)

	drawBarGrid(img, plot, maxTotal)
	drawBarColumns(img, plot, p.Bars, maxTotal)
	drawBarLabels(img, plot, p.Bars)
	drawText(img, barMarginLeft, 16, p.Title, colorTitle)
	drawBarLegend(img, p.SuccessLabel, p.FailureLabel)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("chart: encode bar png: %w", err)
	}
	return buf.Bytes(), nil
}

// barsMaxTotal returns the largest single bar's Success+Failure total
// across bars, or 1 if bars is empty or every total is 0 (avoids a
// division by zero when scaling bar heights, and keeps an all-zero
// chart's axis at a sane [0,1] range rather than collapsing to
// nothing).
func barsMaxTotal(bars []Bar) int {
	max := 0
	for _, b := range bars {
		total := b.Success + b.Failure
		if total > max {
			max = total
		}
	}
	if max == 0 {
		return 1
	}
	return max
}

// drawBarGrid draws the plot area's border and value-axis tick labels
// (0, 25%, 50%, 75%, 100% of maxTotal, rounded to whole numbers).
func drawBarGrid(img *image.RGBA, plot image.Rectangle, maxTotal int) {
	const ticks = 4
	for i := 0; i <= ticks; i++ {
		frac := float64(i) / float64(ticks)
		y := plot.Max.Y - int(frac*float64(plot.Dy()))
		drawHLine(img, plot.Min.X, plot.Max.X, y, colorGrid)
		value := int(frac * float64(maxTotal))
		label := fmt.Sprintf("%d", value)
		drawText(img, plot.Min.X-textWidth(label)-6, y-textHeight()/2, label, colorAxis)
	}
	drawVLine(img, plot.Min.X, plot.Min.Y, plot.Max.Y, colorAxis)
	drawVLine(img, plot.Max.X, plot.Min.Y, plot.Max.Y, colorAxis)
}

// drawBarColumns draws each bar as a fixed-width column, success
// segment stacked below failure (so a mixed-outcome bar reads
// top-to-bottom as "how much of this failed" at a glance), evenly
// spaced across plot's width.
func drawBarColumns(img *image.RGBA, plot image.Rectangle, bars []Bar, maxTotal int) {
	n := len(bars)
	if n == 0 {
		return
	}
	slotWidth := plot.Dx() / n
	barWidth := slotWidth * 2 / 3
	if barWidth < 1 {
		barWidth = 1
	}

	for i, b := range bars {
		slotX := plot.Min.X + i*slotWidth
		barX := slotX + (slotWidth-barWidth)/2

		successHeight := barSegmentHeight(b.Success, maxTotal, plot.Dy())
		failureHeight := barSegmentHeight(b.Failure, maxTotal, plot.Dy())

		successTop := plot.Max.Y - successHeight
		fillRect(img, barX, successTop, barWidth, successHeight, barColorSuccess)

		failureTop := successTop - failureHeight
		fillRect(img, barX, failureTop, barWidth, failureHeight, barColorFailure)
	}
}

// barSegmentHeight scales count against maxTotal into a pixel height
// within plotHeight, rounding down; a positive count always renders
// at least 1px tall so a real (if tiny) segment is never invisible.
func barSegmentHeight(count, maxTotal, plotHeight int) int {
	if count <= 0 || maxTotal <= 0 {
		return 0
	}
	h := count * plotHeight / maxTotal
	if h < 1 {
		h = 1
	}
	return h
}

// drawBarLabels draws each bar's (possibly truncated) label centered
// under its column.
func drawBarLabels(img *image.RGBA, plot image.Rectangle, bars []Bar) {
	n := len(bars)
	if n == 0 {
		return
	}
	slotWidth := plot.Dx() / n
	for i, b := range bars {
		label := truncateBarLabel(b.Label)
		slotX := plot.Min.X + i*slotWidth
		lx := slotX + (slotWidth-textWidth(label))/2
		drawText(img, lx, plot.Max.Y+10, label, colorAxis)
	}
}

// truncateBarLabel cuts label to maxBarLabelChars runes, leaving it
// unchanged if already short enough.
func truncateBarLabel(label string) string {
	runes := []rune(label)
	if len(runes) <= maxBarLabelChars {
		return label
	}
	return string(runes[:maxBarLabelChars])
}

// drawBarLegend draws a small two-swatch legend in the chart's top
// right, captioned by successLabel/failureLabel (falling back to
// "Success"/"Failure" when empty, so a caller that only cares about
// the default English wording doesn't have to pass anything).
func drawBarLegend(img *image.RGBA, successLabel, failureLabel string) {
	if successLabel == "" {
		successLabel = "Success"
	}
	if failureLabel == "" {
		failureLabel = "Failure"
	}

	const swatchSize = 12
	entries := []struct {
		color color.Color
		label string
	}{
		{barColorSuccess, successLabel},
		{barColorFailure, failureLabel},
	}

	y := 16
	for _, e := range entries {
		width := swatchSize + 6 + textWidth(e.label)
		x := Width - barMarginRight - width
		fillRect(img, x, y, swatchSize, swatchSize, e.color)
		drawText(img, x+swatchSize+6, y-2, e.label, colorAxis)
		y += swatchSize + 8
	}
}

// SortBarsByFailureDesc returns a copy of bars sorted by descending
// Failure count (ties broken by Label ascending for a stable,
// deterministic order) — a convenience for callers that want the
// worst-offending categories drawn first (leftmost) without hand-
// rolling the sort themselves.
func SortBarsByFailureDesc(bars []Bar) []Bar {
	out := make([]Bar, len(bars))
	copy(out, bars)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Failure != out[j].Failure {
			return out[i].Failure > out[j].Failure
		}
		return out[i].Label < out[j].Label
	})
	return out
}
