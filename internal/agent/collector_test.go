package agent

import (
	"context"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/leejeonghun001/cloud-pulse/internal/config"
)

// fakeClock returns successive times from a scripted list, repeating the
// last one once exhausted.
func fakeClock(times []time.Time) func() time.Time {
	i := 0
	return func() time.Time {
		t := times[i]
		if i < len(times)-1 {
			i++
		}
		return t
	}
}

func TestCollector_FirstCollect_ZeroRates(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		cpuTimes: [][]cpu.TimesStat{{{CPU: "cpu-total", User: 100, Idle: 900}}},
		vmem:     []*mem.VirtualMemoryStat{{Total: 1000, Available: 400, Cached: 100}},
		diskIO:   []map[string]disk.IOCountersStat{{"sda": {ReadBytes: 1000, WriteBytes: 2000}}},
		netIO:    []([]net.IOCountersStat){{{Name: "eth0", BytesRecv: 500, BytesSent: 600}}},
		uptime:   12345,
	}
	c := NewCollector(src, CollectorOptions{Now: fakeClock([]time.Time{base})})

	sample, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if sample.CPUPercent != 0 {
		t.Errorf("first collect CPUPercent = %v, want 0", sample.CPUPercent)
	}
	if sample.DiskReadBps != 0 || sample.DiskWriteBps != 0 {
		t.Errorf("first collect disk rates = %v/%v, want 0/0", sample.DiskReadBps, sample.DiskWriteBps)
	}
	if sample.NetRxBps != 0 || sample.NetTxBps != 0 {
		t.Errorf("first collect net rates = %v/%v, want 0/0", sample.NetRxBps, sample.NetTxBps)
	}
	if sample.NetRxBytes != 0 || sample.NetTxBytes != 0 {
		t.Errorf("first collect net byte deltas = %v/%v, want 0/0", sample.NetRxBytes, sample.NetTxBytes)
	}
	if sample.UptimeSeconds != 12345 {
		t.Errorf("UptimeSeconds = %d, want 12345", sample.UptimeSeconds)
	}
	if sample.Timestamp != base.Unix() {
		t.Errorf("Timestamp = %d, want %d", sample.Timestamp, base.Unix())
	}
}

func TestCollector_CPUPercentAcrossCalls(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		cpuTimes: [][]cpu.TimesStat{
			{{CPU: "cpu-total", User: 0, Idle: 0}},
			{{CPU: "cpu-total", User: 50, Idle: 50}},
		},
	}
	c := NewCollector(src, CollectorOptions{Now: fakeClock([]time.Time{base, base.Add(time.Second)})})

	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	sample, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if sample.CPUPercent != 50 {
		t.Errorf("CPUPercent = %v, want 50", sample.CPUPercent)
	}
}

func TestCollector_CounterReset(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		cpuTimes: [][]cpu.TimesStat{{{User: 100}}, {{User: 100}}},
		diskIO: []map[string]disk.IOCountersStat{
			{"sda": {ReadBytes: 5000, WriteBytes: 5000}},
			{"sda": {ReadBytes: 100, WriteBytes: 100}}, // reset (counter went down)
		},
		netIO: []([]net.IOCountersStat){
			{{Name: "eth0", BytesRecv: 5000, BytesSent: 5000}},
			{{Name: "eth0", BytesRecv: 100, BytesSent: 100}}, // reset
		},
	}
	c := NewCollector(src, CollectorOptions{Now: fakeClock([]time.Time{base, base.Add(time.Second)})})

	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	sample, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if sample.DiskReadBps != 0 || sample.DiskWriteBps != 0 {
		t.Errorf("disk rates after reset = %v/%v, want 0/0", sample.DiskReadBps, sample.DiskWriteBps)
	}
	if sample.NetRxBytes != 0 || sample.NetTxBytes != 0 {
		t.Errorf("net byte deltas after reset = %v/%v, want 0/0", sample.NetRxBytes, sample.NetTxBytes)
	}
}

func TestCollector_NetExclusionGlobs(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		netIO: []([]net.IOCountersStat){
			{
				{Name: "lo", BytesRecv: 0, BytesSent: 0},
				{Name: "docker0", BytesRecv: 0, BytesSent: 0},
				{Name: "eth0", BytesRecv: 1000, BytesSent: 2000},
			},
			{
				{Name: "lo", BytesRecv: 999999, BytesSent: 999999}, // excluded, should not count
				{Name: "docker0", BytesRecv: 999999, BytesSent: 999999},
				{Name: "eth0", BytesRecv: 1500, BytesSent: 2600},
			},
		},
	}
	c := NewCollector(src, CollectorOptions{
		NetExclude: []string{"lo", "docker*"},
		Now:        fakeClock([]time.Time{base, base.Add(time.Second)}),
	})

	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	sample, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if sample.NetRxBytes != 500 {
		t.Errorf("NetRxBytes = %d, want 500 (only eth0 delta)", sample.NetRxBytes)
	}
	if sample.NetTxBytes != 600 {
		t.Errorf("NetTxBytes = %d, want 600 (only eth0 delta)", sample.NetTxBytes)
	}
	if sample.NetRxBps != 500 {
		t.Errorf("NetRxBps = %v, want 500", sample.NetRxBps)
	}
}

// diskDedupeFixture uses mountpoints that includePartition admits on the
// current OS. Windows has no mountpoint filter beyond pseudo filesystems, but
// using drive-letter paths keeps the fixture representative of gopsutil data.
func diskDedupeFixture() (dataDeep, data, backup, dataDevice, backupDevice, fstype string) {
	switch runtime.GOOS {
	case "darwin":
		return "/Volumes/data/deep", "/Volumes/data", "/", "/dev/disk2s1", "/dev/disk1s1", "apfs"
	case "windows":
		return `C:\data\deep`, `C:\data`, `D:\`, "C:", "D:", "ntfs"
	default:
		return "/mnt/data/deep", "/data", "/backup", "/dev/sda1", "/dev/sdb1", "ext4"
	}
}

func TestCollector_DiskDedupeAndAggregate(t *testing.T) {
	t.Parallel()

	dataDeep, data, backup, dataDevice, backupDevice, fstype := diskDedupeFixture()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		partitions: []disk.PartitionStat{
			{Device: dataDevice, Mountpoint: dataDeep, Fstype: fstype},
			{Device: dataDevice, Mountpoint: data, Fstype: fstype}, // shorter, should win dedupe
			{Device: backupDevice, Mountpoint: backup, Fstype: fstype},
			{Device: "tmpfs", Mountpoint: "/run/lock", Fstype: "tmpfs"}, // excluded pseudo fs
		},
		usage: map[string]*disk.UsageStat{
			data:   {Total: 1000, Used: 400, UsedPercent: 40},
			backup: {Total: 2000, Used: 1000, UsedPercent: 50},
		},
	}
	c := NewCollector(src, CollectorOptions{Now: fakeClock([]time.Time{base})})

	sample, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(sample.Disks) != 2 {
		t.Fatalf("Disks = %+v, want 2 entries", sample.Disks)
	}
	if sample.DiskTotal != 3000 {
		t.Errorf("DiskTotal = %d, want 3000", sample.DiskTotal)
	}
	if sample.DiskUsed != 1400 {
		t.Errorf("DiskUsed = %d, want 1400", sample.DiskUsed)
	}
	wantPct := 1400.0 / 3000.0 * 100
	if sample.DiskUsedPercent != wantPct {
		t.Errorf("DiskUsedPercent = %v, want %v", sample.DiskUsedPercent, wantPct)
	}

	foundData, foundBackup := false, false
	for _, d := range sample.Disks {
		switch d.Device {
		case dataDevice:
			foundData = true
			if d.Mountpoint != data {
				t.Errorf("shortest mountpoint for %s = %q, want %q", dataDevice, d.Mountpoint, data)
			}
		case backupDevice:
			foundBackup = true
			if d.Mountpoint != backup {
				t.Errorf("mountpoint for %s = %q, want %q", backupDevice, d.Mountpoint, backup)
			}
		}
	}
	if !foundData || !foundBackup {
		t.Errorf("Disks = %+v, want devices %q and %q", sample.Disks, dataDevice, backupDevice)
	}
}

// diskZeroTotalFixture keeps both the included disk and the zero-total disk
// within each platform's accepted mountpoint set, so the test exercises the
// zero-total branch rather than a platform filter.
func diskZeroTotalFixture() (root, empty, rootDevice, emptyDevice, fstype string) {
	switch runtime.GOOS {
	case "darwin":
		return "/", "/Volumes/empty", "/dev/disk1s1", "/dev/disk2s1", "apfs"
	case "windows":
		return `C:\`, `D:\empty`, "C:", "D:", "ntfs"
	default:
		return "/", "/empty", "/dev/sda1", "/dev/zero0", "ext4"
	}
}

func TestCollector_DiskSkipsZeroTotal(t *testing.T) {
	t.Parallel()

	root, empty, rootDevice, emptyDevice, fstype := diskZeroTotalFixture()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		partitions: []disk.PartitionStat{
			{Device: rootDevice, Mountpoint: root, Fstype: fstype},
			{Device: emptyDevice, Mountpoint: empty, Fstype: fstype},
		},
		usage: map[string]*disk.UsageStat{
			root:  {Total: 1000, Used: 100},
			empty: {Total: 0, Used: 0},
		},
	}
	c := NewCollector(src, CollectorOptions{Now: fakeClock([]time.Time{base})})

	sample, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(sample.Disks) != 1 {
		t.Fatalf("Disks = %+v, want 1 entry (zero-total skipped)", sample.Disks)
	}
	if sample.Disks[0].Device != rootDevice || sample.Disks[0].Mountpoint != root {
		t.Errorf("remaining disk = %+v, want device %q at %q", sample.Disks[0], rootDevice, root)
	}
}

func TestCollector_MemTrueAvailable(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		vmem: []*mem.VirtualMemoryStat{{Total: 8000, Available: 3000, Cached: 1500}},
	}
	c := NewCollector(src, CollectorOptions{Now: fakeClock([]time.Time{base})})

	sample, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if sample.MemUsed != 5000 {
		t.Errorf("MemUsed = %d, want 5000 (Total-Available)", sample.MemUsed)
	}
	wantPct := 5000.0 / 8000.0 * 100
	if sample.MemUsedPercent != wantPct {
		t.Errorf("MemUsedPercent = %v, want %v", sample.MemUsedPercent, wantPct)
	}
	if sample.MemCached != 1500 {
		t.Errorf("MemCached = %d, want 1500", sample.MemCached)
	}
}

func TestCollector_IndividualMetricFailureZeroesButDoesNotFailSample(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		vmemErr:       assertErr,
		swapErr:       assertErr,
		loadAvgErr:    assertErr,
		partitionsErr: assertErr,
		diskIOErr:     assertErr,
		netIOErr:      assertErr,
		uptimeErr:     assertErr,
	}
	logger := slog.New(slog.NewTextHandler(&discardWriter{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := NewCollector(src, CollectorOptions{Now: fakeClock([]time.Time{base}), Logger: logger})

	sample, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect returned error, want nil (individual failures should not fail the sample): %v", err)
	}
	if sample.CPUPercent != 0 || sample.MemTotal != 0 || sample.UptimeSeconds != 0 {
		t.Errorf("expected zero values on metric failure, got %+v", sample)
	}
}

func TestCollector_ErrorsOnlyWhenContextDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	src := &fakeSource{}
	c := NewCollector(src, CollectorOptions{Now: fakeClock([]time.Time{time.Now()})})

	_, err := c.Collect(ctx)
	if err == nil {
		t.Fatal("expected error when context is done")
	}
}

func TestCollector_HostInfoUnavailableAndCPUCoreFallback(t *testing.T) {
	t.Parallel()

	src := &fakeSource{
		hostInfoErr: assertErr,
		cpuInfo:     nil,
	}
	logger := slog.New(slog.DiscardHandler)
	info := BuildHostInfo(context.Background(), src, config.Agent{HostID: "h1", Provider: "other"}, logger)
	if info.CPUCores <= 0 {
		t.Errorf("CPUCores = %d, want > 0 (fallback to runtime.NumCPU)", info.CPUCores)
	}
	if info.Hostname != "" {
		t.Errorf("Hostname = %q, want empty when host info unavailable", info.Hostname)
	}
}

// assertErr is a stand-in error used to simulate individual metric
// collection failures in fakeSource.
var assertErr = context.DeadlineExceeded

// discardWriter discards everything written to it (used to exercise
// debug-log code paths without asserting on log content).
type discardWriter struct{}

func (*discardWriter) Write(p []byte) (int, error) { return len(p), nil }
