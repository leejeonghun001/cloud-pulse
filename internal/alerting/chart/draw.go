package chart

import (
	"fmt"
	"image"
	"image/color"
	"time"
)

// drawHLine draws a 1px-thick horizontal line from x0 to x1 at y.
func drawHLine(img *image.RGBA, x0, x1, y int, c color.Color) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	fillRect(img, x0, y, x1-x0, 1, c)
}

// drawVLine draws a 1px-thick vertical line from y0 to y1 at x.
func drawVLine(img *image.RGBA, x, y0, y1 int, c color.Color) {
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	fillRect(img, x, y0, 1, y1-y0, c)
}

// drawLine draws a straight line between (x0,y0) and (x1,y1) using a
// simple integer Bresenham-style stepper (no anti-aliasing — this is
// a small notification chart, not a high-fidelity render), 2px thick
// for visibility at chart scale.
func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.Color) {
	dx := abs(x1 - x0)
	dy := abs(y1 - y0)
	sx, sy := 1, 1
	if x1 < x0 {
		sx = -1
	}
	if y1 < y0 {
		sy = -1
	}
	err := dx - dy
	x, y := x0, y0
	for {
		fillRect(img, x-1, y-1, 3, 3, c)
		if x == x1 && y == y1 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x += sx
		}
		if e2 < dx {
			err += dx
			y += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// drawAxes draws value-axis tick labels on the left and time-axis tick
// labels on the bottom.
func drawAxes(img *image.RGBA, plot image.Rectangle, minT, maxT time.Time, minV, maxV float64, unit string) {
	const valueTicks = 4
	for i := 0; i <= valueTicks; i++ {
		frac := float64(i) / float64(valueTicks)
		v := minV + frac*(maxV-minV)
		y := plot.Max.Y - int(frac*float64(plot.Dy()))
		label := formatValue(v) + unit
		drawText(img, plot.Min.X-textWidth(label)-6, y-textHeight()/2, label, colorAxis)
	}

	const timeTicks = 4
	for i := 0; i <= timeTicks; i++ {
		frac := float64(i) / float64(timeTicks)
		t := minT.Add(time.Duration(frac * float64(maxT.Sub(minT))))
		x := plot.Min.X + int(frac*float64(plot.Dx()))
		label := t.Format("15:04")
		lx := x - textWidth(label)/2
		if i == timeTicks {
			lx = plot.Max.X - textWidth(label)
		}
		if i == 0 {
			lx = plot.Min.X
		}
		drawText(img, lx, plot.Max.Y+8, label, colorAxis)
	}
}

// formatValue renders v with one decimal place, trimming a trailing
// ".0" for whole numbers to keep axis labels compact.
func formatValue(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	if len(s) >= 2 && s[len(s)-2:] == ".0" {
		return s[:len(s)-2]
	}
	return s
}
