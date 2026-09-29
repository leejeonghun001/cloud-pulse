package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// CollectorOptions configures a Collector.
type CollectorOptions struct {
	// NetExclude lists glob patterns of network interface names to
	// exclude from network accounting.
	NetExclude []string
	// Logger receives debug logs for individual metric failures. If nil,
	// a discard logger is used.
	Logger *slog.Logger
	// Now returns the current time. If nil, time.Now is used.
	Now func() time.Time
}

// diskIOPrev tracks previous disk I/O counters for rate calculation.
type diskIOPrev struct {
	readBytes  uint64
	writeBytes uint64
}

// netIOPrev tracks previous network I/O counters for rate calculation.
type netIOPrev struct {
	bytesSent uint64
	bytesRecv uint64
}

// Collector collects periodic Sample snapshots from a Source, computing
// rates from counter deltas against the previous Collect call.
type Collector struct {
	src    Source
	opts   CollectorOptions
	logger *slog.Logger
	now    func() time.Time

	// prevTime is the timestamp of the previous successful Collect call
	// (zero value means no previous call yet).
	prevTime time.Time

	prevCPU     *cpu.TimesStat
	prevDiskIO  map[string]diskIOPrev
	prevNetIO   map[string]netIOPrev
	hasPrevDisk bool
	hasPrevNet  bool
}

// NewCollector constructs a Collector reading from src with the given
// options.
func NewCollector(src Source, opts CollectorOptions) *Collector {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Collector{
		src:    src,
		opts:   opts,
		logger: logger,
		now:    now,
	}
}

// Collect gathers a single Sample snapshot timestamped at the current
// time (c.now()). It only returns an error if ctx is done; individual
// metric failures are logged at debug level and result in zero values for
// that metric, never a failed Sample. Rates and deltas are 0 on the first
// call.
func (c *Collector) Collect(ctx context.Context) (models.Sample, error) {
	if err := ctx.Err(); err != nil {
		return models.Sample{}, fmt.Errorf("agent: collect: %w", err)
	}
	now := c.now()
	return c.collect(ctx, now, now), nil
}

// CollectAt gathers a single Sample snapshot, stamping it with ts instead
// of the current time. This lets Run align every sample's Timestamp to a
// shared hub-clock boundary while rate/delta math still uses the real
// monotonic time elapsed since the previous call (via c.now(), not ts),
// so rates remain accurate even if ts values are adjusted for clock
// alignment. It only returns an error if ctx is done; individual metric
// failures are logged at debug level and result in zero values for that
// metric, never a failed Sample. Rates and deltas are 0 on the first call.
func (c *Collector) CollectAt(ctx context.Context, ts time.Time) (models.Sample, error) {
	if err := ctx.Err(); err != nil {
		return models.Sample{}, fmt.Errorf("agent: collect: %w", err)
	}
	return c.collect(ctx, c.now(), ts), nil
}

// collect is the shared implementation behind Collect and CollectAt. now
// is read exactly once from c.now() by the caller and used for
// elapsed-time rate math (real monotonic time since the previous call);
// ts is the value stamped onto the Sample's Timestamp field, which may
// differ from now when called via CollectAt for hub-clock alignment.
func (c *Collector) collect(ctx context.Context, now, ts time.Time) models.Sample {
	var elapsed float64
	if !c.prevTime.IsZero() {
		elapsed = now.Sub(c.prevTime).Seconds()
	}

	sample := models.Sample{Timestamp: ts.Unix()}

	c.collectCPU(ctx, &sample)
	c.collectLoad(ctx, &sample)
	c.collectMem(ctx, &sample)
	c.collectSwap(ctx, &sample)
	c.collectDisk(ctx, &sample, elapsed)
	c.collectNet(ctx, &sample, elapsed)
	c.collectUptime(ctx, &sample)

	c.prevTime = now
	return sample
}

func (c *Collector) collectCPU(ctx context.Context, sample *models.Sample) {
	times, err := c.src.CPUTimes(ctx)
	if err != nil || len(times) == 0 {
		c.logger.DebugContext(ctx, "cpu times unavailable", "error", err)
		return
	}
	cur := times[0]
	if c.prevCPU != nil {
		sample.CPUPercent = cpuPercent(*c.prevCPU, cur)
	}
	c.prevCPU = &cur
}

func (c *Collector) collectLoad(ctx context.Context, sample *models.Sample) {
	avg, err := c.src.LoadAvg(ctx)
	if err != nil || avg == nil {
		c.logger.DebugContext(ctx, "load average unavailable", "error", err)
		return
	}
	sample.Load1 = avg.Load1
	sample.Load5 = avg.Load5
	sample.Load15 = avg.Load15
}

func (c *Collector) collectMem(ctx context.Context, sample *models.Sample) {
	vm, err := c.src.VirtualMemory(ctx)
	if err != nil || vm == nil {
		c.logger.DebugContext(ctx, "virtual memory unavailable", "error", err)
		return
	}
	sample.MemTotal = vm.Total
	sample.MemAvailable = vm.Available
	if vm.Total >= vm.Available {
		sample.MemUsed = vm.Total - vm.Available
	}
	if vm.Total > 0 {
		sample.MemUsedPercent = float64(sample.MemUsed) / float64(vm.Total) * 100
	}
	sample.MemCached = vm.Cached
}

func (c *Collector) collectSwap(ctx context.Context, sample *models.Sample) {
	sw, err := c.src.SwapMemory(ctx)
	if err != nil || sw == nil {
		c.logger.DebugContext(ctx, "swap memory unavailable", "error", err)
		return
	}
	sample.SwapTotal = sw.Total
	sample.SwapUsed = sw.Used
}

func (c *Collector) collectUptime(ctx context.Context, sample *models.Sample) {
	uptime, err := c.src.Uptime(ctx)
	if err != nil {
		c.logger.DebugContext(ctx, "uptime unavailable", "error", err)
		return
	}
	sample.UptimeSeconds = uptime
}

// collectDisk fills disk usage and I/O rate fields, deduping partitions by
// device (keeping the shortest mountpoint) and applying pseudo-filesystem
// and mountpoint filters.
func (c *Collector) collectDisk(ctx context.Context, sample *models.Sample, elapsed float64) {
	c.collectDiskUsage(ctx, sample)
	c.collectDiskIO(ctx, sample, elapsed)
}

func (c *Collector) collectDiskUsage(ctx context.Context, sample *models.Sample) {
	parts, err := c.src.Partitions(ctx, false)
	if err != nil {
		c.logger.DebugContext(ctx, "disk partitions unavailable", "error", err)
		return
	}

	// Filter and dedupe by device, keeping the shortest mountpoint.
	byDevice := make(map[string]disk.PartitionStat)
	for _, p := range parts {
		if !includePartition(p.Mountpoint, p.Device, p.Fstype) {
			continue
		}
		existing, ok := byDevice[p.Device]
		if !ok || len(p.Mountpoint) < len(existing.Mountpoint) {
			byDevice[p.Device] = p
		}
	}

	// Sort devices for deterministic output order.
	devices := make([]string, 0, len(byDevice))
	for d := range byDevice {
		devices = append(devices, d)
	}
	sort.Strings(devices)

	var disks []models.DiskUsage
	var totalBytes, usedBytes uint64
	for _, d := range devices {
		p := byDevice[d]
		usage, err := c.src.Usage(ctx, p.Mountpoint)
		if err != nil || usage == nil {
			c.logger.DebugContext(ctx, "disk usage unavailable", "mountpoint", p.Mountpoint, "error", err)
			continue
		}
		if usage.Total == 0 {
			continue
		}
		disks = append(disks, models.DiskUsage{
			Mountpoint:  p.Mountpoint,
			Device:      p.Device,
			FSType:      p.Fstype,
			TotalBytes:  usage.Total,
			UsedBytes:   usage.Used,
			UsedPercent: usage.UsedPercent,
		})
		totalBytes += usage.Total
		usedBytes += usage.Used
	}

	sample.Disks = disks
	sample.DiskTotal = totalBytes
	sample.DiskUsed = usedBytes
	if totalBytes > 0 {
		sample.DiskUsedPercent = float64(usedBytes) / float64(totalBytes) * 100
	}
}

func (c *Collector) collectDiskIO(ctx context.Context, sample *models.Sample, elapsed float64) {
	counters, err := c.src.IOCounters(ctx)
	if err != nil {
		c.logger.DebugContext(ctx, "disk io counters unavailable", "error", err)
		return
	}

	cur := make(map[string]diskIOPrev, len(counters))
	var readDelta, writeDelta uint64
	for name, stat := range counters {
		if !includeDiskIODevice(name) {
			continue
		}
		cur[name] = diskIOPrev{readBytes: stat.ReadBytes, writeBytes: stat.WriteBytes}
		if c.hasPrevDisk {
			prev, ok := c.prevDiskIO[name]
			if ok {
				readDelta += counterDelta(prev.readBytes, stat.ReadBytes)
				writeDelta += counterDelta(prev.writeBytes, stat.WriteBytes)
			}
		}
	}

	if c.hasPrevDisk && elapsed > 0 {
		sample.DiskReadBps = float64(readDelta) / elapsed
		sample.DiskWriteBps = float64(writeDelta) / elapsed
	}

	c.prevDiskIO = cur
	c.hasPrevDisk = true
}

func (c *Collector) collectNet(ctx context.Context, sample *models.Sample, elapsed float64) {
	counters, err := c.src.NetIOCounters(ctx, true)
	if err != nil {
		c.logger.DebugContext(ctx, "net io counters unavailable", "error", err)
		return
	}

	cur := make(map[string]netIOPrev, len(counters))
	var rxDelta, txDelta uint64
	for _, stat := range counters {
		if matchesAnyGlob(stat.Name, c.opts.NetExclude) {
			continue
		}
		cur[stat.Name] = netIOPrev{bytesSent: stat.BytesSent, bytesRecv: stat.BytesRecv}
		if c.hasPrevNet {
			prev, ok := c.prevNetIO[stat.Name]
			if ok {
				rxDelta += counterDelta(prev.bytesRecv, stat.BytesRecv)
				txDelta += counterDelta(prev.bytesSent, stat.BytesSent)
			}
		}
	}

	sample.NetRxBytes = rxDelta
	sample.NetTxBytes = txDelta
	if c.hasPrevNet && elapsed > 0 {
		sample.NetRxBps = float64(rxDelta) / elapsed
		sample.NetTxBps = float64(txDelta) / elapsed
	}

	c.prevNetIO = cur
	c.hasPrevNet = true
}
