package agent

import (
	"context"
	"errors"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// fakeSource is a scriptable Source for deterministic tests. Each field
// holding a slice is a queue: successive calls pop the next element,
// repeating the last element once the queue is exhausted.
type fakeSource struct {
	cpuTimes   [][]cpu.TimesStat
	cpuInfo    []cpu.InfoStat
	vmem       []*mem.VirtualMemoryStat
	swap       []*mem.SwapMemoryStat
	loadAvg    []*load.AvgStat
	partitions []disk.PartitionStat
	usage      map[string]*disk.UsageStat
	diskIO     []map[string]disk.IOCountersStat
	netIO      []([]net.IOCountersStat)
	hostInfo   *host.InfoStat
	uptime     uint64

	cpuTimesErr   error
	vmemErr       error
	swapErr       error
	loadAvgErr    error
	partitionsErr error
	usageErr      error
	diskIOErr     error
	netIOErr      error
	hostInfoErr   error
	uptimeErr     error

	cpuTimesIdx int
	diskIOIdx   int
	netIOIdx    int
	vmemIdx     int
}

func (f *fakeSource) CPUTimes(context.Context) ([]cpu.TimesStat, error) {
	if f.cpuTimesErr != nil {
		return nil, f.cpuTimesErr
	}
	if len(f.cpuTimes) == 0 {
		return nil, errors.New("no cpu times scripted")
	}
	idx := f.cpuTimesIdx
	if idx >= len(f.cpuTimes) {
		idx = len(f.cpuTimes) - 1
	}
	f.cpuTimesIdx++
	return f.cpuTimes[idx], nil
}

func (f *fakeSource) CPUInfo(context.Context) ([]cpu.InfoStat, error) {
	return f.cpuInfo, nil
}

func (f *fakeSource) VirtualMemory(context.Context) (*mem.VirtualMemoryStat, error) {
	if f.vmemErr != nil {
		return nil, f.vmemErr
	}
	if len(f.vmem) == 0 {
		return nil, nil
	}
	idx := f.vmemIdx
	if idx >= len(f.vmem) {
		idx = len(f.vmem) - 1
	}
	f.vmemIdx++
	return f.vmem[idx], nil
}

func (f *fakeSource) SwapMemory(context.Context) (*mem.SwapMemoryStat, error) {
	if f.swapErr != nil {
		return nil, f.swapErr
	}
	if len(f.swap) == 0 {
		return nil, nil
	}
	return f.swap[0], nil
}

func (f *fakeSource) LoadAvg(context.Context) (*load.AvgStat, error) {
	if f.loadAvgErr != nil {
		return nil, f.loadAvgErr
	}
	if len(f.loadAvg) == 0 {
		return nil, nil
	}
	return f.loadAvg[0], nil
}

func (f *fakeSource) Partitions(context.Context, bool) ([]disk.PartitionStat, error) {
	if f.partitionsErr != nil {
		return nil, f.partitionsErr
	}
	return f.partitions, nil
}

func (f *fakeSource) Usage(_ context.Context, path string) (*disk.UsageStat, error) {
	if f.usageErr != nil {
		return nil, f.usageErr
	}
	u, ok := f.usage[path]
	if !ok {
		return nil, errors.New("no usage scripted for " + path)
	}
	return u, nil
}

func (f *fakeSource) IOCounters(context.Context) (map[string]disk.IOCountersStat, error) {
	if f.diskIOErr != nil {
		return nil, f.diskIOErr
	}
	if len(f.diskIO) == 0 {
		return nil, nil
	}
	idx := f.diskIOIdx
	if idx >= len(f.diskIO) {
		idx = len(f.diskIO) - 1
	}
	f.diskIOIdx++
	return f.diskIO[idx], nil
}

func (f *fakeSource) NetIOCounters(context.Context, bool) ([]net.IOCountersStat, error) {
	if f.netIOErr != nil {
		return nil, f.netIOErr
	}
	if len(f.netIO) == 0 {
		return nil, nil
	}
	idx := f.netIOIdx
	if idx >= len(f.netIO) {
		idx = len(f.netIO) - 1
	}
	f.netIOIdx++
	return f.netIO[idx], nil
}

func (f *fakeSource) HostInfo(context.Context) (*host.InfoStat, error) {
	if f.hostInfoErr != nil {
		return nil, f.hostInfoErr
	}
	return f.hostInfo, nil
}

func (f *fakeSource) Uptime(context.Context) (uint64, error) {
	if f.uptimeErr != nil {
		return 0, f.uptimeErr
	}
	return f.uptime, nil
}
