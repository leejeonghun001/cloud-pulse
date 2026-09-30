package chart

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// fixedBarParams returns a deterministic BarParams for the golden-hash
// and structural tests.
func fixedBarParams() BarParams {
	return BarParams{
		Title: "Update batch batch-abc123 \u00b7 4/6 succeeded",
		Bars: []Bar{
			{Label: "linux", Success: 3, Failure: 1},
			{Label: "darwin", Success: 1, Failure: 0},
			{Label: "windows", Success: 0, Failure: 1},
		},
		SuccessLabel: "Succeeded",
		FailureLabel: "Failed",
	}
}

func TestRenderBars_Deterministic(t *testing.T) {
	t.Parallel()

	p := fixedBarParams()
	a, err := RenderBars(p)
	if err != nil {
		t.Fatalf("RenderBars() error = %v", err)
	}
	b, err := RenderBars(p)
	if err != nil {
		t.Fatalf("RenderBars() second call error = %v", err)
	}
	if hashOf(a) != hashOf(b) {
		t.Fatalf("RenderBars() is not deterministic: hash1=%s hash2=%s", hashOf(a), hashOf(b))
	}
}

func TestRenderBars_GoldenHash(t *testing.T) {
	t.Parallel()

	// Locks in RenderBars' exact decoded-pixel output for a fixed
	// input. If this fails after an intentional visual change,
	// regenerate wantHash (run the test, copy the "got" value) after
	// visually confirming the new output still looks correct — see
	// TestRenderBars_WritesInspectablePNG.
	const wantHash = "1a0afbd7d0b67178712abfb0c65d23f10bb6167de4efa13db5c20fdcb8363a6c"

	data, err := RenderBars(fixedBarParams())
	if err != nil {
		t.Fatalf("RenderBars() error = %v", err)
	}
	got := hashOf(data)
	if got != wantHash {
		t.Fatalf("RenderBars() hash = %s, want %s (update wantHash to the got value once the output is visually confirmed)", got, wantHash)
	}
}

func TestRenderBars_ValidPNG(t *testing.T) {
	t.Parallel()

	data, err := RenderBars(fixedBarParams())
	if err != nil {
		t.Fatalf("RenderBars() error = %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png.Decode() error = %v", err)
	}
	b := img.Bounds()
	if b.Dx() != Width || b.Dy() != Height {
		t.Errorf("decoded image size = %dx%d, want %dx%d", b.Dx(), b.Dy(), Width, Height)
	}
}

func TestRenderBars_EmptyBars(t *testing.T) {
	t.Parallel()

	// Must not panic or error with zero bars (e.g. an empty batch).
	_, err := RenderBars(BarParams{Title: "no data"})
	if err != nil {
		t.Fatalf("RenderBars() with no bars error = %v", err)
	}
}

// TestRenderBars_WritesInspectablePNG writes a rendered bar chart to a
// deterministic /tmp path for manual visual review during
// development, mirroring render_test.go's
// TestRender_WritesInspectablePNG pattern.
func TestRenderBars_WritesInspectablePNG(t *testing.T) {
	t.Parallel()

	data, err := RenderBars(fixedBarParams())
	if err != nil {
		t.Fatalf("RenderBars() error = %v", err)
	}
	path := filepath.Join(os.TempDir(), "cloud-pulse-bars-chart-preview.png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write preview PNG: %v", err)
	}
	t.Logf("wrote bar chart preview to %s (%d bytes) for manual visual review", path, len(data))
}

func TestRenderBars_AllZeroCounts(t *testing.T) {
	t.Parallel()

	_, err := RenderBars(BarParams{
		Title: "all zero",
		Bars:  []Bar{{Label: "linux"}, {Label: "darwin"}},
	})
	if err != nil {
		t.Fatalf("RenderBars() with all-zero counts error = %v", err)
	}
}

func TestBarsMaxTotal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		bars []Bar
		want int
	}{
		{"empty", nil, 1},
		{"all_zero", []Bar{{Label: "a"}, {Label: "b"}}, 1},
		{"mixed", []Bar{{Success: 3, Failure: 1}, {Success: 1}}, 4},
	}
	for _, tc := range cases {
		if got := barsMaxTotal(tc.bars); got != tc.want {
			t.Errorf("%s: barsMaxTotal() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestBarSegmentHeight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		count, maxT, h int
		want           int
	}{
		{"zero_count", 0, 10, 100, 0},
		{"zero_max", 5, 0, 100, 0},
		{"half", 5, 10, 100, 50},
		{"tiny_nonzero_count_rounds_up_to_1px", 1, 1000, 100, 1},
		{"full", 10, 10, 100, 100},
	}
	for _, tc := range cases {
		if got := barSegmentHeight(tc.count, tc.maxT, tc.h); got != tc.want {
			t.Errorf("%s: barSegmentHeight(%d,%d,%d) = %d, want %d", tc.name, tc.count, tc.maxT, tc.h, got, tc.want)
		}
	}
}

func TestTruncateBarLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"linux", "linux"},
		{"", ""},
		{"exactly-10c", "exactly-10"}, // 11 chars -> truncated to 10
		{"windows", "windows"},
	}
	for _, tc := range cases {
		if got := truncateBarLabel(tc.in); got != tc.want {
			t.Errorf("truncateBarLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSortBarsByFailureDesc(t *testing.T) {
	t.Parallel()

	in := []Bar{
		{Label: "b", Failure: 1},
		{Label: "a", Failure: 3},
		{Label: "c", Failure: 3},
		{Label: "d", Failure: 0},
	}
	got := SortBarsByFailureDesc(in)

	want := []string{"a", "c", "b", "d"} // failure=3 tie broken by label asc, then descending
	for i, w := range want {
		if got[i].Label != w {
			t.Errorf("got[%d].Label = %q, want %q (full order: %+v)", i, got[i].Label, w, got)
		}
	}

	// Original slice must not be mutated.
	if in[0].Label != "b" {
		t.Error("SortBarsByFailureDesc mutated its input slice")
	}
}
