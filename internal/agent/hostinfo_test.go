package agent

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"

	"github.com/leejeonghun001/cloud-pulse/internal/config"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestBuildHostInfo_Success(t *testing.T) {
	t.Parallel()

	src := &fakeSource{
		hostInfo: &host.InfoStat{
			Hostname:        "web-01",
			OS:              "linux",
			Platform:        "ubuntu",
			PlatformVersion: "22.04",
			KernelVersion:   "6.1.0",
			BootTime:        1700000000,
		},
		cpuInfo: []cpu.InfoStat{{ModelName: "ARM Cortex-A76"}, {ModelName: "ARM Cortex-A76"}},
	}
	cfg := config.Agent{HostID: "web-01", Provider: "aws"}

	info := BuildHostInfo(context.Background(), src, cfg, slog.New(slog.DiscardHandler))

	if info.ID != "web-01" {
		t.Errorf("ID = %q, want web-01", info.ID)
	}
	if info.Hostname != "web-01" || info.OS != "linux" || info.Platform != "ubuntu" {
		t.Errorf("host fields = %+v", info)
	}
	if info.CPUModel != "ARM Cortex-A76" {
		t.Errorf("CPUModel = %q", info.CPUModel)
	}
	if info.CPUCores != 2 {
		t.Errorf("CPUCores = %d, want 2", info.CPUCores)
	}
	if info.BootTime != 1700000000 {
		t.Errorf("BootTime = %d", info.BootTime)
	}
	if info.Provider != models.ProviderAWS {
		t.Errorf("Provider = %q, want aws", info.Provider)
	}
	if info.EgressLimitBytes != models.AWSFreeEgressBytes {
		t.Errorf("EgressLimitBytes = %d, want %d", info.EgressLimitBytes, models.AWSFreeEgressBytes)
	}
	if info.AgentVersion == "" {
		t.Error("AgentVersion should not be empty")
	}
	if info.Arch == "" {
		t.Error("Arch should not be empty")
	}
}

func TestBuildHostInfo_ExplicitEgressLimitOverridesProviderDefault(t *testing.T) {
	t.Parallel()

	src := &fakeSource{hostInfoErr: errors.New("unavailable")}
	limit := uint64(5 * models.GiB)
	cfg := config.Agent{HostID: "h", Provider: "aws", EgressLimitBytes: &limit}

	info := BuildHostInfo(context.Background(), src, cfg, nil)
	if info.EgressLimitBytes != limit {
		t.Errorf("EgressLimitBytes = %d, want %d", info.EgressLimitBytes, limit)
	}
}

func TestBuildHostInfo_OtherProviderUnlimited(t *testing.T) {
	t.Parallel()

	src := &fakeSource{hostInfoErr: errors.New("unavailable")}
	cfg := config.Agent{HostID: "h", Provider: "other"}

	info := BuildHostInfo(context.Background(), src, cfg, nil)
	if info.EgressLimitBytes != 0 {
		t.Errorf("EgressLimitBytes = %d, want 0 (unlimited)", info.EgressLimitBytes)
	}
}

func TestBuildHostInfo_CPUCoreFallbackWhenInfoUnavailable(t *testing.T) {
	t.Parallel()

	src := &fakeSource{hostInfoErr: errors.New("unavailable")}
	cfg := config.Agent{HostID: "h", Provider: "other"}

	info := BuildHostInfo(context.Background(), src, cfg, nil)
	if info.CPUCores <= 0 {
		t.Errorf("CPUCores = %d, want > 0", info.CPUCores)
	}
}
