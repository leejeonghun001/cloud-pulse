package chart

import (
	"image"
	"image/color"
	"image/draw"
)

// textScale is the integer upscaling factor applied to each font
// pixel, so labels stay legible at the chart's 800x400 resolution.
const textScale = 2

// charSpacing is the horizontal gap, in scaled pixels, between
// consecutive glyphs.
const charSpacing = 2

// textWidth returns the pixel width drawText would occupy for s at
// the current scale, including inter-glyph spacing but not a
// trailing gap.
func textWidth(s string) int {
	if s == "" {
		return 0
	}
	n := len([]rune(s))
	return n*(glyphWidth*textScale) + (n-1)*charSpacing
}

// textHeight returns the pixel height of one line of text at the
// current scale.
func textHeight() int {
	return glyphHeight * textScale
}

// drawText draws s starting at (x, y) — (x, y) is the top-left corner
// of the text's bounding box — in c onto img.
func drawText(img *image.RGBA, x, y int, s string, c color.Color) {
	cursor := x
	for _, r := range s {
		drawGlyph(img, cursor, y, r, c)
		cursor += glyphWidth*textScale + charSpacing
	}
}

// drawGlyph draws one character's 5x7 bitmap at (x, y), scaled by
// textScale.
func drawGlyph(img *image.RGBA, x, y int, r rune, c color.Color) {
	glyph := glyphFor(r)
	for row := 0; row < glyphHeight; row++ {
		bits := glyph[row]
		for col := 0; col < glyphWidth; col++ {
			// Bit 4 is the leftmost column (see font.go).
			if bits&(1<<(glyphWidth-1-col)) == 0 {
				continue
			}
			px := x + col*textScale
			py := y + row*textScale
			fillRect(img, px, py, textScale, textScale, c)
		}
	}
}

// fillRect fills the w x h rectangle at (x, y) with c, clipped to
// img's bounds. Alpha-blends c over the existing pixels rather than
// replacing them outright, so a semi-transparent fill (e.g. the
// breach-window shading) tints instead of washing out the
// background/grid beneath it.
func fillRect(img *image.RGBA, x, y, w, h int, c color.Color) {
	r := image.Rect(x, y, x+w, y+h).Intersect(img.Bounds())
	if r.Empty() {
		return
	}
	draw.Draw(img, r, &image.Uniform{C: c}, image.Point{}, draw.Over)
}
