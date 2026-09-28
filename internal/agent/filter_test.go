package agent

import (
	"runtime"
	"testing"

	"github.com/shirou/gopsutil/v4/cpu"
)

func TestCPUPercent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		prev cpu.TimesStat
		cur  cpu.TimesStat
		want float64
	}{
		{
			name: "fifty_percent_busy",
			prev: cpu.TimesStat{User: 0, System: 0, Idle: 0, Iowait: 0},
			cur:  cpu.TimesStat{User: 50, System: 0, Idle: 50, Iowait: 0},
			want: 50,
		},
		{
			name: "fully_idle",
			prev: cpu.TimesStat{Idle: 0},
			cur:  cpu.TimesStat{Idle: 100},
			want: 0,
		},
		{
			name: "fully_busy",
			prev: cpu.TimesStat{User: 0},
			cur:  cpu.TimesStat{User: 100},
			want: 100,
		},
		{
			name: "iowait_counts_as_idle",
			prev: cpu.TimesStat{User: 0, Iowait: 0},
			cur:  cpu.TimesStat{User: 30, Iowait: 70},
			want: 30,
		},
		{
			name: "guest_ticks_are_not_double_counted",
			prev: cpu.TimesStat{},
			cur:  cpu.TimesStat{User: 100, Guest: 100, Idle: 100},
			want: 50,
		},
		{
			name: "zero_delta_returns_zero",
			prev: cpu.TimesStat{User: 100},
			cur:  cpu.TimesStat{User: 100},
			want: 0,
		},
		{
			name: "negative_delta_counter_reset_returns_zero",
			prev: cpu.TimesStat{User: 500},
			cur:  cpu.TimesStat{User: 10},
			want: 0,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cpuPercent(tc.prev, tc.cur)
			if got != tc.want {
				t.Errorf("cpuPercent(%+v, %+v) = %v, want %v", tc.prev, tc.cur, got, tc.want)
			}
		})
	}
}

func TestCounterDelta(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		prev uint64
		cur  uint64
		want uint64
	}{
		{"normal_increase", 100, 150, 50},
		{"no_change", 100, 100, 0},
		{"reset_returns_zero", 500, 10, 0},
		{"zero_to_zero", 0, 0, 0},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := counterDelta(tc.prev, tc.cur)
			if got != tc.want {
				t.Errorf("counterDelta(%d, %d) = %d, want %d", tc.prev, tc.cur, got, tc.want)
			}
		})
	}
}

func TestMatchesAnyGlob(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		iface string
		globs []string
		want  bool
	}{
		{"exact_match", "lo", []string{"lo", "docker*"}, true},
		{"glob_match", "docker0", []string{"lo", "docker*"}, true},
		{"veth_glob", "veth1234", []string{"veth*"}, true},
		{"no_match", "eth0", []string{"lo", "docker*", "veth*"}, false},
		{"empty_globs", "eth0", nil, false},
		{"br_dash_glob", "br-abc123", []string{"br-*"}, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchesAnyGlob(tc.iface, tc.globs)
			if got != tc.want {
				t.Errorf("matchesAnyGlob(%q, %v) = %v, want %v", tc.iface, tc.globs, got, tc.want)
			}
		})
	}
}

func TestIncludePartition(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		mountpoint string
		device     string
		fstype     string
		wantLinux  bool
		wantDarwin bool
	}{
		{"root", "/", "/dev/sda1", "ext4", true, true},
		{"tmpfs_excluded", "/run/lock", "tmpfs", "tmpfs", false, false},
		{"proc_excluded", "/proc", "proc", "proc", false, false},
		{"sys_excluded", "/sys", "sysfs", "sysfs", false, false},
		{"snap_excluded", "/snap/core/1234", "/dev/loop0", "squashfs", false, false},
		{"run_media_included", "/run/media/user/usb", "/dev/sdb1", "vfat", true, false},
		{"run_excluded_otherwise", "/run", "tmpfs", "tmpfs", false, false},
		{"docker_lib_excluded", "/var/lib/docker/overlay2", "overlay", "overlay", false, false},
		{"volumes_included_darwin", "/Volumes/External", "/dev/disk2s1", "apfs", true, true},
		{"home_included", "/home", "/dev/sda2", "ext4", true, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := includePartition(tc.mountpoint, tc.device, tc.fstype)
			var want bool
			switch runtime.GOOS {
			case "linux":
				want = tc.wantLinux
			case "darwin":
				want = tc.wantDarwin
			default:
				// Other OSes only filter by pseudo fstype in this
				// implementation.
				want = !pseudoFSTypes[tc.fstype]
			}
			if got != want {
				t.Errorf("includePartition(%q, %q, %q) = %v, want %v (GOOS=%s)", tc.mountpoint, tc.device, tc.fstype, got, want, runtime.GOOS)
			}
		})
	}
}

func TestIncludeDiskIODevice(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		device    string
		wantLinux bool
	}{
		{"whole_disk_sda", "sda", true},
		{"partition_sda1", "sda1", false},
		{"whole_disk_nvme", "nvme0n1", true},
		{"partition_nvme", "nvme0n1p1", false},
		{"whole_disk_mmcblk", "mmcblk0", true},
		{"partition_mmcblk", "mmcblk0p1", false},
		{"loop_excluded", "loop0", false},
		{"ram_excluded", "ram0", false},
		{"zram_excluded", "zram0", false},
		{"dm_excluded", "dm-0", false},
		{"sr_excluded", "sr0", false},
		{"fd_excluded", "fd0", false},
		{"md_kept", "md0", true},
		{"virtio_disk_kept", "vda", true},
		{"virtio_partition_excluded", "vda1", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := includeDiskIODevice(tc.device)
			want := tc.wantLinux
			if runtime.GOOS != "linux" {
				want = true // all devices included on non-Linux
			}
			if got != want {
				t.Errorf("includeDiskIODevice(%q) = %v, want %v (GOOS=%s)", tc.device, got, want, runtime.GOOS)
			}
		})
	}
}
