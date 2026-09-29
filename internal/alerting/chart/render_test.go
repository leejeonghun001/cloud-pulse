package chart

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixedParams returns a deterministic Params for the golden-hash and
// visual-inspection tests: fixed timestamps (never time.Now()), fixed
// values.
func fixedParams() Params {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	var pts []Point
	for i := 0; i < 60; i++ {
		v := 40.0 + float64(i)*0.9
		if i > 40 {
			v = 93.4
		}
		pts = append(pts, Point{Time: base.Add(time.Duration(i) * time.Minute), Value: v})
	}
	return Params{
		Title:       "host-1 \u00b7 CPU 93.4% > 90% for 5m",
		Points:      pts,
		Threshold:   90,
		BreachStart: base.Add(40 * time.Minute),
		Unit:        "%",
	}
}

func TestRender_Deterministic(t *testing.T) {
	t.Parallel()

	p := fixedParams()
	a, err := Render(p)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	b, err := Render(p)
	if err != nil {
		t.Fatalf("Render() second call error = %v", err)
	}
	if hashOf(a) != hashOf(b) {
		t.Fatalf("Render() is not deterministic: hash1=%s hash2=%s", hashOf(a), hashOf(b))
	}
}

func TestRender_GoldenHash(t *testing.T) {
	t.Parallel()

	// Locks in Render's exact byte output for a fixed input. If this
	// test fails after an intentional visual change to the renderer,
	// regenerate the expected hash (run the test, copy the "got" value
	// from the failure message) after visually confirming the new
	// output still looks correct (see TestRender_WritesInspectablePNG).
	const wantHash = "61b21cda4922547279126808ca84ac44443d62db8c9745e3af905bb7a8685dfb"

	data, err := Render(fixedParams())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	got := hashOf(data)
	if got != wantHash {
		t.Fatalf("Render() hash = %s, want %s (update wantHash to the got value once the output is visually confirmed)", got, wantHash)
	}
}

func TestRender_ValidPNG(t *testing.T) {
	t.Parallel()

	data, err := Render(fixedParams())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
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

func TestRender_EmptyPoints(t *testing.T) {
	t.Parallel()

	// Must not panic or error even with zero data points (e.g. a brand
	// new host with no history yet).
	_, err := Render(Params{Title: "no data", Threshold: 90, Unit: "%"})
	if err != nil {
		t.Fatalf("Render() with no points error = %v", err)
	}
}

func TestRender_UnsortedPointsSortedDefensively(t *testing.T) {
	t.Parallel()

	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	sorted := Params{
		Title:     "t",
		Threshold: 5,
		Points: []Point{
			{Time: base, Value: 1},
			{Time: base.Add(time.Minute), Value: 2},
			{Time: base.Add(2 * time.Minute), Value: 3},
		},
	}
	shuffled := Params{
		Title:     "t",
		Threshold: 5,
		Points: []Point{
			{Time: base.Add(2 * time.Minute), Value: 3},
			{Time: base, Value: 1},
			{Time: base.Add(time.Minute), Value: 2},
		},
	}

	a, err := Render(sorted)
	if err != nil {
		t.Fatalf("Render(sorted) error = %v", err)
	}
	b, err := Render(shuffled)
	if err != nil {
		t.Fatalf("Render(shuffled) error = %v", err)
	}
	if hashOf(a) != hashOf(b) {
		t.Errorf("Render() output depends on input point order; want order-independent")
	}
}

// TestRender_WritesInspectablePNG writes a rendered chart to a
// deterministic /tmp path for manual visual review during
// development (dark theme, readable labels, dashed threshold line,
// shaded breach window) — not asserted on beyond "no error", per
// SPEC-v0.5 §B's request for a chart a human can look at.
func TestRender_WritesInspectablePNG(t *testing.T) {
	t.Parallel()

	data, err := Render(fixedParams())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	path := filepath.Join(os.TempDir(), "cloud-pulse-chart-preview.png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write preview PNG: %v", err)
	}
	t.Logf("wrote chart preview to %s (%d bytes) for manual visual review", path, len(data))
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestRender_EmptyPointsIsDeterministic(t *testing.T) {
	t.Parallel()

	params := Params{Title: "no data", Threshold: 90, Unit: "%"}
	first, err := Render(params)
	if err != nil {
		t.Fatalf("first Render() error = %v", err)
	}
	second, err := Render(params)
	if err != nil {
		t.Fatalf("second Render() error = %v", err)
	}
	if hashOf(first) != hashOf(second) {
		t.Fatalf("empty Render() is not deterministic: %s != %s", hashOf(first), hashOf(second))
	}
}

// TestRender_BreachWindowIsTranslucent guards against an invalid
// premultiplied fill color painting an opaque band: a pixel inside the
// breach window but away from the series must stay close to the dark
// background, not become solid red.
func TestRender_BreachWindowIsTranslucent(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pts := []Point{{Time: start, Value: 10}, {Time: start.Add(time.Hour), Value: 10}}
	data, err := Render(Params{Points: pts, Threshold: 90, BreachStart: start.Add(30 * time.Minute)})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Inside the breach window, between the flat series (low) and the threshold (high).
	r, g, b, _ := img.At(Width-marginRight-10, Height/2).RGBA()
	if r>>8 > 0x60 || g>>8 > 0x40 || b>>8 > 0x40 {
		t.Fatalf("breach band pixel = (%d,%d,%d), want a translucent tint over the dark background", r>>8, g>>8, b>>8)
	}
	if r <= g {
		t.Fatalf("breach band pixel = (%d,%d,%d), want a red tint", r>>8, g>>8, b>>8)
	}
}
