package agent

import (
	"context"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// GopsutilSource is the real Source implementation backed by
// github.com/shirou/gopsutil/v4.
type GopsutilSource struct{}

// NewGopsutilSource returns a Source backed by gopsutil.
func NewGopsutilSource() GopsutilSource {
	return GopsutilSource{}
}

// CPUTimes returns aggregate CPU time counters via cpu.TimesWithContext.
func (GopsutilSource) CPUTimes(ctx context.Context) ([]cpu.TimesStat, error) {
	return cpu.TimesWithContext(ctx, false)
}

// CPUInfo returns per-logical-CPU information via cpu.InfoWithContext.
func (GopsutilSource) CPUInfo(ctx context.Context) ([]cpu.InfoStat, error) {
	return cpu.InfoWithContext(ctx)
}

// VirtualMemory returns RAM usage via mem.VirtualMemoryWithContext.
func (GopsutilSource) VirtualMemory(ctx context.Context) (*mem.VirtualMemoryStat, error) {
	return mem.VirtualMemoryWithContext(ctx)
}

// SwapMemory returns swap usage via mem.SwapMemoryWithContext.
func (GopsutilSource) SwapMemory(ctx context.Context) (*mem.SwapMemoryStat, error) {
	return mem.SwapMemoryWithContext(ctx)
}

// LoadAvg returns load averages via load.AvgWithContext.
func (GopsutilSource) LoadAvg(ctx context.Context) (*load.AvgStat, error) {
	return load.AvgWithContext(ctx)
}

// Partitions returns mounted filesystems via disk.PartitionsWithContext.
func (GopsutilSource) Partitions(ctx context.Context, all bool) ([]disk.PartitionStat, error) {
	return disk.PartitionsWithContext(ctx, all)
}

// Usage returns filesystem usage via disk.UsageWithContext.
func (GopsutilSource) Usage(ctx context.Context, path string) (*disk.UsageStat, error) {
	return disk.UsageWithContext(ctx, path)
}

// IOCounters returns per-device disk I/O counters via
// disk.IOCountersWithContext.
func (GopsutilSource) IOCounters(ctx context.Context) (map[string]disk.IOCountersStat, error) {
	return disk.IOCountersWithContext(ctx)
}

// NetIOCounters returns per-NIC network I/O counters via
// net.IOCountersWithContext.
func (GopsutilSource) NetIOCounters(ctx context.Context, pernic bool) ([]net.IOCountersStat, error) {
	return net.IOCountersWithContext(ctx, pernic)
}

// HostInfo returns static host information via host.InfoWithContext.
func (GopsutilSource) HostInfo(ctx context.Context) (*host.InfoStat, error) {
	return host.InfoWithContext(ctx)
}

// Uptime returns host uptime in seconds via host.UptimeWithContext.
func (GopsutilSource) Uptime(ctx context.Context) (uint64, error) {
	return host.UptimeWithContext(ctx)
}
