package main

import (
	"github.com/leejeonghun001/cloud-pulse/internal/billing"
	"github.com/leejeonghun001/cloud-pulse/internal/config"
	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
)

// Compile-time assertion that the real ExecRunner satisfies
// billing.CommandRunner, and that storage.DB (passed to
// buildBillingRuntime as billing.Store) satisfies billing's narrow
// persistence interface.
var (
	_ billing.CommandRunner = billing.ExecRunner{}
	_ billing.Store         = (*storage.DB)(nil)
)

// buildBillingRuntime constructs the hub.BillingRuntime passed as
// hub.Options.Billing, or nil when cfg.BillingEnabled() is false
// (CP_BILLING=off) — see SPEC-v0.6 §1. store only needs to satisfy
// billing.Store's narrow GetCloudCostSnapshot/SetCloudCostSnapshot
// subset; storage.DB (passed as hub.Store from runHub) already does.
// The AWS/OCI collector always tries both providers on every tick
// regardless of which credentials are actually configured; each
// provider quietly reports not_configured/not_installed on its own
// when it has nothing to work with (internal/billing's own
// responsibility, not this wiring function's).
func buildBillingRuntime(cfg config.Hub, store billing.Store) *hub.BillingRuntime {
	if !cfg.BillingEnabled() {
		return nil
	}

	collector := billing.New(store, billing.Options{
		Runner:              billing.ExecRunner{},
		DataDir:             cfg.DataDir,
		PATH:                cfg.BillingPATH,
		AWSAccessKeyID:      cfg.AWSAccessKeyID,
		AWSSecretAccessKey:  cfg.AWSSecretAccessKey,
		AWSSessionToken:     cfg.AWSSessionToken,
		AWSResourcesEnabled: cfg.BillingAWSResources,
		OCITenancyID:        cfg.OCITenancyID,
		OCIProfile:          cfg.OCIProfile,
		OCIConfigFile:       cfg.OCIConfigFile,
	})

	return &hub.BillingRuntime{
		Collector:   collector,
		EnvInterval: cfg.BillingInterval,
	}
}
