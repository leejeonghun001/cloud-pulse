package agent

import (
	"path"
	"regexp"
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
)

// cpuPercent computes CPU busy percentage from two cpu.TimesStat samples.
// busy = total_delta - idle_delta - iowait_delta, expressed as a percentage
// of total_delta. Returns 0 if the total delta is zero or negative (e.g.
// counter reset or no elapsed time), guarding against division by zero and
// negative results caused by counter resets.
func cpuPercent(prev, cur cpu.TimesStat) float64 {
	prevTotal := prev.Total()
	curTotal := cur.Total()
	totalDelta := curTotal - prevTotal
	if totalDelta <= 0 {
		return 0
	}
	idleDelta := (cur.Idle - prev.Idle) + (cur.Iowait - prev.Iowait)
	busy := totalDelta - idleDelta
	if busy < 0 {
		busy = 0
	}
	pct := busy / totalDelta * 100
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// counterDelta computes cur-prev for a monotonic uint64 counter, returning
// 0 if the counter appears to have reset (cur < prev).
func counterDelta(prev, cur uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

// matchesAnyGlob reports whether name matches any of the shell glob
// patterns in globs (path.Match semantics). Invalid patterns never match.
func matchesAnyGlob(name string, globs []string) bool {
	for _, g := range globs {
		ok, err := path.Match(g, name)
		if err == nil && ok {
			return true
		}
	}
	return false
}

// pseudoFSTypes is the set of virtual/pseudo filesystem types excluded
// from disk usage aggregation.
var pseudoFSTypes = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "overlay": true, "squashfs": true,
	"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true,
	"autofs": true, "devpts": true, "mqueue": true, "tracefs": true,
	"debugfs": true, "securityfs": true, "pstore": true, "bpf": true,
	"fusectl": true, "configfs": true, "hugetlbfs": true, "ramfs": true,
	"nsfs": true, "efivarfs": true, "binfmt_misc": true, "rpc_pipefs": true,
	"nfsd": true, "fuse.lxcfs": true, "fuse.gvfsd-fuse": true, "fuse.portal": true,
}

// pseudoMountPrefixes lists mountpoint path prefixes excluded from disk
// usage aggregation on Linux (pseudo/virtual/container-internal mounts).
// "/run" is excluded except for "/run/media" (removable media mounts).
var pseudoMountPrefixes = []string{
	"/snap/", "/proc", "/sys", "/dev", "/var/lib/docker", "/var/lib/kubelet",
}

// linuxPartitionDeviceExclude matches Linux partition (not whole-disk)
// device names that should be excluded, keeping only whole disks.
var linuxPartitionDeviceExclude = []*regexp.Regexp{
	regexp.MustCompile(`^/dev/(sd|vd|xvd|hd)[a-z]+[0-9]+$`),
	regexp.MustCompile(`^/dev/(nvme[0-9]+n[0-9]+|mmcblk[0-9]+)p[0-9]+$`),
}

// includePartition reports whether a partition (identified by its
// mountpoint, device, and filesystem type) should be included in disk
// usage aggregation, applying OS-specific pseudo-filesystem and
// mountpoint filters.
func includePartition(mountpoint, device, fstype string) bool {
	if pseudoFSTypes[fstype] {
		return false
	}

	switch runtime.GOOS {
	case "linux":
		if mountpoint != "/run" && strings.HasPrefix(mountpoint, "/run") && !strings.HasPrefix(mountpoint, "/run/media") {
			return false
		}
		for _, prefix := range pseudoMountPrefixes {
			if strings.HasPrefix(mountpoint, prefix) {
				return false
			}
		}
	case "darwin":
		if mountpoint != "/" && !strings.HasPrefix(mountpoint, "/Volumes/") {
			return false
		}
	}
	return true
}

// includeDiskIODevice reports whether a disk I/O device name should be
// included in disk I/O rate aggregation. On Linux, virtual/loop/ram/dm/sr/fd
// devices and numbered partitions of whole disks are excluded (md/RAID
// devices are kept). On other OSes all devices are included.
func includeDiskIODevice(name string) bool {
	if runtime.GOOS != "linux" {
		return true
	}
	excludeGlobs := []string{"loop*", "ram*", "zram*", "dm-*", "sr*", "fd*"}
	if matchesAnyGlob(name, excludeGlobs) {
		return false
	}
	devPath := name
	if !strings.HasPrefix(devPath, "/dev/") {
		devPath = "/dev/" + devPath
	}
	for _, re := range linuxPartitionDeviceExclude {
		if re.MatchString(devPath) {
			return false
		}
	}
	return true
}
