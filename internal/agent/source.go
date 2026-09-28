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

// Source abstracts the system metrics gopsutil exposes, so Collector can be
// tested with a fake implementation. All methods take a context as their
// first argument and should respect its deadline/cancellation.
type Source interface {
	// CPUTimes returns aggregate (all-CPU) CPU time counters.
	CPUTimes(ctx context.Context) ([]cpu.TimesStat, error)
	// CPUInfo returns per-logical-CPU static information (model, etc).
	CPUInfo(ctx context.Context) ([]cpu.InfoStat, error)
	// VirtualMemory returns current RAM usage statistics.
	VirtualMemory(ctx context.Context) (*mem.VirtualMemoryStat, error)
	// SwapMemory returns current swap usage statistics.
	SwapMemory(ctx context.Context) (*mem.SwapMemoryStat, error)
	// LoadAvg returns the 1/5/15 minute load averages.
	LoadAvg(ctx context.Context) (*load.AvgStat, error)
	// Partitions returns mounted filesystem partitions. all mirrors
	// gopsutil's disk.Partitions all parameter.
	Partitions(ctx context.Context, all bool) ([]disk.PartitionStat, error)
	// Usage returns usage statistics for the filesystem mounted at path.
	Usage(ctx context.Context, path string) (*disk.UsageStat, error)
	// IOCounters returns per-device disk I/O counters.
	IOCounters(ctx context.Context) (map[string]disk.IOCountersStat, error)
	// NetIOCounters returns per-NIC network I/O counters.
	NetIOCounters(ctx context.Context, pernic bool) ([]net.IOCountersStat, error)
	// HostInfo returns static host information (OS, platform, hostname).
	HostInfo(ctx context.Context) (*host.InfoStat, error)
	// Uptime returns the host uptime in seconds.
	Uptime(ctx context.Context) (uint64, error)
}
