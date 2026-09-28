package agent

import (
	"context"
	"log/slog"
	"os"
	"runtime"

	"github.com/leejeonghun001/cloud-pulse/internal/config"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// BuildHostInfo constructs a models.HostInfo for the current host, using
// src for CPU/host metadata and cfg for identity/provider/egress
// configuration. logger receives debug entries for individual metric
// failures (nil is treated as a discard logger); it never returns an
// error.
func BuildHostInfo(ctx context.Context, src Source, cfg config.Agent, logger *slog.Logger) models.HostInfo {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	info := models.HostInfo{
		ID:           cfg.HostID,
		Arch:         runtime.GOARCH,
		AgentVersion: version.Version,
	}

	if hi, err := src.HostInfo(ctx); err == nil && hi != nil {
		info.Hostname = hi.Hostname
		info.OS = hi.OS
		info.Platform = hi.Platform
		info.PlatformVersion = hi.PlatformVersion
		info.KernelVersion = hi.KernelVersion
		info.BootTime = int64(hi.BootTime) //nolint:gosec // boot time fits int64 until year 2262
	} else {
		logger.DebugContext(ctx, "host info unavailable", "error", err)
	}

	cpuInfo, err := src.CPUInfo(ctx)
	if err != nil {
		logger.DebugContext(ctx, "cpu info unavailable", "error", err)
	}
	if len(cpuInfo) > 0 {
		info.CPUModel = cpuInfo[0].ModelName
		info.CPUCores = len(cpuInfo)
	} else {
		info.CPUCores = runtime.NumCPU()
	}

	info.Provider = resolveProvider(cfg.Provider)
	info.EgressLimitBytes = resolveEgressLimit(cfg.EgressLimitBytes, info.Provider)

	return info
}

// resolveProvider converts a config provider string ("auto"|"aws"|"oci"|
// "other") into a models.Provider, running DetectProvider when "auto".
func resolveProvider(configured string) models.Provider {
	switch configured {
	case "aws":
		return models.ProviderAWS
	case "oci":
		return models.ProviderOCI
	case "other":
		return models.ProviderOther
	default: // "auto" or unrecognized
		return DetectProvider(os.ReadFile)
	}
}

// resolveEgressLimit returns explicit if non-nil, otherwise the provider's
// default egress limit.
func resolveEgressLimit(explicit *uint64, provider models.Provider) uint64 {
	if explicit != nil {
		return *explicit
	}
	return models.DefaultEgressLimit(provider)
}
