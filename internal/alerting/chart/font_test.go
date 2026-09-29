package chart

import "testing"

func TestGlyphFor_LowercaseUppercased(t *testing.T) {
	t.Parallel()

	if glyphFor('a') != glyphFor('A') {
		t.Errorf("glyphFor('a') != glyphFor('A'); font has no separate lowercase forms")
	}
}

func TestGlyphFor_UnknownRuneReturnsBlank(t *testing.T) {
	t.Parallel()

	got := glyphFor('\u4e2d') // arbitrary unsupported rune (CJK)
	want := font5x7[' ']
	if got != want {
		t.Errorf("glyphFor(unsupported rune) = %v, want blank glyph %v", got, want)
	}
}

func TestFont5x7_AllGlyphsFixedSize(t *testing.T) {
	t.Parallel()

	for r, g := range font5x7 {
		for row, bits := range g {
			if bits > 0b11111 {
				t.Errorf("glyph %q row %d has bits set outside the 5-bit column range: %05b", r, row, bits)
			}
		}
	}
}

func TestTextWidth_EmptyString(t *testing.T) {
	t.Parallel()

	if got := textWidth(""); got != 0 {
		t.Errorf("textWidth(\"\") = %d, want 0", got)
	}
}

func TestTextWidth_Monotonic(t *testing.T) {
	t.Parallel()

	if textWidth("AB") <= textWidth("A") {
		t.Errorf("textWidth(\"AB\") should exceed textWidth(\"A\")")
	}
}
