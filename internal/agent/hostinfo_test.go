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
		// uint64(...) keeps the untyped 100 GiB constant from defaulting to int,
		// which overflows on 32-bit targets (linux/armv7 is a release target).
		t.Errorf("EgressLimitBytes = %d, want %d", info.EgressLimitBytes, uint64(models.AWSFreeEgressBytes))
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

func TestBuildHostInfo_CloudMetadataOffLeavesCloudInstanceIDEmpty(t *testing.T) {
	t.Parallel()

	src := &fakeSource{hostInfoErr: errors.New("unavailable")}
	cfg := config.Agent{HostID: "h", Provider: "aws", CloudMetadata: "off"}

	info := BuildHostInfo(context.Background(), src, cfg, nil)
	if info.CloudInstanceID != "" {
		t.Errorf("CloudInstanceID = %q, want empty when CP_CLOUD_METADATA=off", info.CloudInstanceID)
	}
}

func TestBuildHostInfo_CloudMetadataAutoCallsDetectionStub(t *testing.T) {
	t.Parallel()

	// cloudInstanceID is currently a stub that always returns "" (see
	// cloudmeta.go's doc comment); this test documents that
	// BuildHostInfo still calls it (rather than skipping detection
	// outright) when CloudMetadata != "off", so a future real
	// implementation is exercised without any caller change.
	src := &fakeSource{hostInfoErr: errors.New("unavailable")}
	cfg := config.Agent{HostID: "h", Provider: "aws", CloudMetadata: "auto"}

	info := BuildHostInfo(context.Background(), src, cfg, nil)
	if info.CloudInstanceID != "" {
		t.Errorf("CloudInstanceID = %q, want empty (stub always returns \"\")", info.CloudInstanceID)
	}
}

func TestBuildHostInfo_RemoteUpdateCapability(t *testing.T) {
	t.Parallel()

	src := &fakeSource{hostInfoErr: errors.New("unavailable")}

	optedIn := BuildHostInfo(context.Background(), src, config.Agent{HostID: "h", Provider: "other", RemoteUpdate: true}, nil)
	notOptedIn := BuildHostInfo(context.Background(), src, config.Agent{HostID: "h", Provider: "other", RemoteUpdate: false}, nil)

	// Exact Supported value is OS-dependent (Linux-only per SPEC-v0.6
	// §2); only assert the OptedIn/Reason behavior that's fully
	// implemented regardless of platform.
	if optedIn.RemoteUpdate.Supported {
		if !optedIn.RemoteUpdate.OptedIn {
			t.Errorf("RemoteUpdate.OptedIn = false for CP_REMOTE_UPDATE=on, want true (got %+v)", optedIn.RemoteUpdate)
		}
		if optedIn.RemoteUpdate.Reason != "" {
			t.Errorf("RemoteUpdate.Reason = %q, want empty when opted in and supported", optedIn.RemoteUpdate.Reason)
		}
		if notOptedIn.RemoteUpdate.OptedIn {
			t.Error("RemoteUpdate.OptedIn = true for CP_REMOTE_UPDATE=off, want false")
		}
		if notOptedIn.RemoteUpdate.Reason != models.UpdateReasonNotEnabled {
			t.Errorf("RemoteUpdate.Reason = %q, want %q", notOptedIn.RemoteUpdate.Reason, models.UpdateReasonNotEnabled)
		}
	} else {
		if optedIn.RemoteUpdate.Reason != models.UpdateReasonUnsupported {
			t.Errorf("RemoteUpdate.Reason = %q, want %q on an unsupported OS", optedIn.RemoteUpdate.Reason, models.UpdateReasonUnsupported)
		}
	}
}
