package agent

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// TestGopsutilSource_Live exercises the real GopsutilSource against the
// running machine. It is skipped under -short since it depends on actual
// OS facilities and timing, and only asserts baseline plausibility so it
// passes on linux/darwin/windows/freebsd without per-OS branching beyond
// the memory total check (skipped on OSes gopsutil doesn't fully support
// for VirtualMemory).
func TestGopsutilSource_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live gopsutil test in -short mode")
	}
	t.Parallel()

	src := NewGopsutilSource()
	c := NewCollector(src, CollectorOptions{Now: time.Now})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sample, err := c.Collect(ctx)
	if err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	time.Sleep(1 * time.Second)

	sample, err = c.Collect(ctx)
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		if sample.MemTotal == 0 {
			t.Errorf("MemTotal = 0 on %s, want > 0", runtime.GOOS)
		}
	}
}
