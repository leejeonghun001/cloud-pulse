package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// OCIConfig configures the OCI Usage API collector.
type OCIConfig struct {
	// TenancyID is the tenancy OCID to query; required (SPEC-v0.6 §1:
	// from CP_OCI_TENANCY_ID, falling back to the CLI's own config file
	// tenancy value when empty — resolving that fallback is the
	// caller's job since it requires reading the OCI config file, not
	// this collector's).
	TenancyID string
	// Env is the exact environment passed to every `oci` invocation.
	Env []string
	// HomeDir is the sandboxed HOME directory `oci` is run with.
	HomeDir string
}

// BuildOCIEnv constructs the minimal environment passed to every `oci`
// CLI invocation (SPEC-v0.6 §1): HOME (sandboxed), a minimal PATH, and
// only OCI_CLI_CONFIG_FILE/OCI_CLI_PROFILE when configured.
func BuildOCIEnv(homeDir, pathEnv, configFile, profile string) []string {
	env := []string{
		"HOME=" + homeDir,
		"PATH=" + pathEnv,
	}
	if configFile != "" {
		env = append(env, "OCI_CLI_CONFIG_FILE="+configFile)
	}
	if profile != "" {
		env = append(env, "OCI_CLI_PROFILE="+profile)
	}
	return env
}

// ociUsageOutput mirrors the slice of `oci usage-api usage-summary
// request-summarized-usages` JSON output cloud-pulse reads: a "data"
// list of usage-summary items, each carrying a computedAmount and
// (since we always group by resourceId) a resourceId field. See
// notes/v06-billing.md for the citation.
type ociUsageOutput struct {
	Data []struct {
		ComputedAmount float64 `json:"computedAmount"`
		ResourceID     string  `json:"resourceId"`
		Currency       string  `json:"currency"`
	} `json:"data"`
}

// CollectOCI runs `oci usage-api usage-summary request-summarized-usages`
// for the current month-to-date (SPEC-v0.6 §1), grouped by resourceId,
// and returns the resulting snapshot. The OCI Usage API has no forecast
// endpoint (confirmed in notes/v06-billing.md), so ForecastMethod is
// always "linear". now is injected for deterministic tests.
func CollectOCI(ctx context.Context, runner CommandRunner, cfg OCIConfig, now time.Time) models.CloudCostSnapshot {
	if cfg.TenancyID == "" {
		return models.CloudCostSnapshot{
			Provider:     models.CloudBillingOCI,
			Status:       models.CloudBillingNotConfigured,
			StatusDetail: "no tenancy OCID configured",
		}
	}

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	tomorrow := now.AddDate(0, 0, 1).Truncate(24 * time.Hour)

	args := []string{
		"usage-api", "usage-summary", "request-summarized-usages",
		"--tenant-id", cfg.TenancyID,
		"--time-usage-started", monthStart.Format("2006-01-02T15:04:05Z"),
		"--time-usage-ended", tomorrow.Format("2006-01-02T15:04:05Z"),
		"--granularity", "DAILY",
		"--query-type", "COST",
		"--group-by", `["resourceId"]`,
		"--output", "json",
	}
	res, err := runner.Run(ctx, cfg.HomeDir, cfg.Env, "oci", args...)
	if err != nil {
		return errSnapshot(models.CloudBillingOCI, fmt.Sprintf("run oci usage-api: %v", err))
	}
	if res.NotFound || res.TimedOut || res.ExitCode != 0 {
		c := classify(res)
		return models.CloudCostSnapshot{Provider: models.CloudBillingOCI, Status: c.Status, StatusDetail: c.Detail}
	}

	var out ociUsageOutput
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return errSnapshot(models.CloudBillingOCI, "parse usage-summary output")
	}

	var mtdCost float64
	currency := "USD"
	perResource := make(map[string]float64)
	for _, item := range out.Data {
		mtdCost += item.ComputedAmount
		if item.Currency != "" {
			currency = item.Currency
		}
		if item.ResourceID != "" {
			perResource[item.ResourceID] += item.ComputedAmount
		}
	}

	snap := models.CloudCostSnapshot{
		Provider:       models.CloudBillingOCI,
		Status:         models.CloudBillingOK,
		Currency:       currency,
		MTDCost:        mtdCost,
		ForecastCost:   linearProjection(mtdCost, now),
		ForecastMethod: "linear",
		AccountLevel:   len(perResource) == 0,
	}
	if len(perResource) > 0 {
		snap.PerResource = perResource
	}
	return snap
}
