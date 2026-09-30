package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// AWSConfig configures the AWS Cost Explorer collector.
type AWSConfig struct {
	// Resources enables the per-resource
	// get-cost-and-usage-with-resources call (SPEC-v0.6 §1,
	// CP_BILLING_AWS_RESOURCES) in addition to the account-level MTD/
	// forecast calls.
	Resources bool
	// Env is the exact environment passed to every `aws` invocation
	// (see BuildAWSEnv).
	Env []string
	// HomeDir is the sandboxed HOME directory `aws` is run with (also
	// used as the process's working directory).
	HomeDir string
}

// BuildAWSEnv constructs the minimal environment passed to every `aws`
// CLI invocation (SPEC-v0.6 §1): HOME (sandboxed), a minimal PATH, and
// only the specific AWS_* variables the hub was configured with.
func BuildAWSEnv(homeDir, pathEnv string, accessKeyID, secretAccessKey, sessionToken, profile, configFile, credsFile string) []string {
	env := []string{
		"HOME=" + homeDir,
		"PATH=" + pathEnv,
	}
	add := func(key, value string) {
		if value != "" {
			env = append(env, key+"="+value)
		}
	}
	add("AWS_ACCESS_KEY_ID", accessKeyID)
	add("AWS_SECRET_ACCESS_KEY", secretAccessKey)
	add("AWS_SESSION_TOKEN", sessionToken)
	add("AWS_PROFILE", profile)
	add("AWS_CONFIG_FILE", configFile)
	add("AWS_SHARED_CREDENTIALS_FILE", credsFile)
	return env
}

// awsCostAmount/awsCostForecast mirror the small slice of `aws ce
// get-cost-and-usage`/`get-cost-forecast` JSON output cloud-pulse reads
// (see notes/v06-billing.md for the full documented shape); every other
// field in the real response is ignored.
type awsCostAndUsageOutput struct {
	ResultsByTime []struct {
		Total map[string]awsMetricValue `json:"Total"`
	} `json:"ResultsByTime"`
}

type awsMetricValue struct {
	Amount string `json:"Amount"`
	Unit   string `json:"Unit"`
}

type awsCostForecastOutput struct {
	Total struct {
		Amount string `json:"Amount"`
		Unit   string `json:"Unit"`
	} `json:"Total"`
}

type awsCostAndUsageWithResourcesOutput struct {
	ResultsByTime []struct {
		Groups []struct {
			Keys    []string                  `json:"Keys"`
			Metrics map[string]awsMetricValue `json:"Metrics"`
		} `json:"Groups"`
	} `json:"ResultsByTime"`
}

// CollectAWS runs the AWS Cost Explorer CLI calls described in
// SPEC-v0.6 §1 (MTD via get-cost-and-usage, month-end forecast via
// get-cost-forecast unless now is the last day of the month, and
// optionally per-resource via get-cost-and-usage-with-resources) using
// runner, returning the resulting snapshot. now is injected for
// deterministic tests. The returned snapshot never has LastSuccessAt/
// LastAttemptAt/Stale populated — the caller (Collector) merges those
// against the previously persisted snapshot.
func CollectAWS(ctx context.Context, runner CommandRunner, cfg AWSConfig, now time.Time) models.CloudCostSnapshot {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	monthStartStr := monthStart.Format("2006-01-02")

	mtdArgs := []string{
		"ce", "get-cost-and-usage",
		"--time-period", "Start=" + monthStartStr + ",End=" + tomorrow,
		"--granularity", "MONTHLY",
		"--metrics", "UnblendedCost",
		"--output", "json",
	}
	res, err := runner.Run(ctx, cfg.HomeDir, cfg.Env, "aws", mtdArgs...)
	if err != nil {
		return errSnapshot(models.CloudBillingAWS, fmt.Sprintf("run aws ce get-cost-and-usage: %v", err))
	}
	if res.NotFound || res.TimedOut || res.ExitCode != 0 {
		c := classify(res)
		return models.CloudCostSnapshot{Provider: models.CloudBillingAWS, Status: c.Status, StatusDetail: c.Detail}
	}

	var mtdOut awsCostAndUsageOutput
	if err := json.Unmarshal(res.Stdout, &mtdOut); err != nil {
		return errSnapshot(models.CloudBillingAWS, "parse get-cost-and-usage output")
	}
	mtdCost, currency := sumAWSTotal(mtdOut)

	snap := models.CloudCostSnapshot{
		Provider:     models.CloudBillingAWS,
		Status:       models.CloudBillingOK,
		Currency:     currency,
		MTDCost:      mtdCost,
		AccountLevel: true,
	}

	// Month-end forecast, skipped on the last calendar day of the month
	// per SPEC-v0.6 §1 (Start must be <= today; End must be > Start, and
	// "today" through "1st of next month" is otherwise always a valid,
	// non-empty window except when today already IS the last day and
	// tomorrow is the 1st — in which case there is nothing left to
	// forecast for this month anyway).
	if !isLastDayOfMonth(now) {
		forecastArgs := []string{
			"ce", "get-cost-forecast",
			"--time-period", "Start=" + now.Format("2006-01-02") + ",End=" + firstOfNextMonth(now).Format("2006-01-02"),
			"--metric", "UNBLENDED_COST",
			"--granularity", "MONTHLY",
			"--output", "json",
		}
		fres, ferr := runner.Run(ctx, cfg.HomeDir, cfg.Env, "aws", forecastArgs...)
		if ferr == nil && fres.ExitCode == 0 && !fres.NotFound && !fres.TimedOut {
			var fOut awsCostForecastOutput
			if err := json.Unmarshal(fres.Stdout, &fOut); err == nil {
				if amt, err := strconv.ParseFloat(fOut.Total.Amount, 64); err == nil {
					snap.ForecastCost = amt
					snap.ForecastMethod = "api"
					if snap.Currency == "" {
						snap.Currency = fOut.Total.Unit
					}
				}
			}
		}
		// A failed/unparsable forecast call is not fatal to the whole
		// snapshot (MTD already succeeded) — fall back to a linear
		// projection from MTD instead of leaving ForecastCost at 0.
		if snap.ForecastMethod == "" {
			snap.ForecastCost = linearProjection(mtdCost, now)
			snap.ForecastMethod = "linear"
		}
	} else {
		snap.ForecastCost = mtdCost
		snap.ForecastMethod = "linear"
	}

	if cfg.Resources {
		perResource, ok := collectAWSPerResource(ctx, runner, cfg, monthStartStr, tomorrow)
		if ok {
			snap.PerResource = perResource
			snap.AccountLevel = false
		}
	}

	return snap
}

// collectAWSPerResource runs get-cost-and-usage-with-resources grouped
// by RESOURCE_ID. A failure here is quiet (returns ok=false) — SPEC-v0.6
// §1 treats per-resource data as strictly optional/best-effort on top of
// the account-level totals already captured.
func collectAWSPerResource(ctx context.Context, runner CommandRunner, cfg AWSConfig, start, end string) (map[string]float64, bool) {
	args := []string{
		"ce", "get-cost-and-usage-with-resources",
		"--time-period", "Start=" + start + ",End=" + end,
		"--granularity", "MONTHLY",
		"--metrics", "UnblendedCost",
		"--group-by", "Type=DIMENSION,Key=RESOURCE_ID",
		"--output", "json",
	}
	res, err := runner.Run(ctx, cfg.HomeDir, cfg.Env, "aws", args...)
	if err != nil || res.ExitCode != 0 || res.NotFound || res.TimedOut {
		return nil, false
	}
	var out awsCostAndUsageWithResourcesOutput
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, false
	}
	perResource := make(map[string]float64)
	for _, r := range out.ResultsByTime {
		for _, g := range r.Groups {
			if len(g.Keys) == 0 {
				continue
			}
			resourceID := g.Keys[0]
			mv, ok := g.Metrics["UnblendedCost"]
			if !ok {
				continue
			}
			amt, err := strconv.ParseFloat(mv.Amount, 64)
			if err != nil {
				continue
			}
			perResource[resourceID] += amt
		}
	}
	if len(perResource) == 0 {
		return nil, false
	}
	return perResource, true
}

// sumAWSTotal sums UnblendedCost.Amount across every ResultsByTime entry
// (there is exactly one for a single-month MONTHLY query, but summing is
// harmless and future-proof) and returns the currency unit seen.
func sumAWSTotal(out awsCostAndUsageOutput) (cost float64, currency string) {
	for _, r := range out.ResultsByTime {
		mv, ok := r.Total["UnblendedCost"]
		if !ok {
			continue
		}
		if amt, err := strconv.ParseFloat(mv.Amount, 64); err == nil {
			cost += amt
		}
		if currency == "" {
			currency = mv.Unit
		}
	}
	if currency == "" {
		currency = "USD"
	}
	return cost, currency
}

// isLastDayOfMonth reports whether t's date is the final calendar day of
// its month (UTC).
func isLastDayOfMonth(t time.Time) bool {
	return t.AddDate(0, 0, 1).Day() == 1
}

// firstOfNextMonth returns the first day of the month following t's,
// truncated to a date at UTC midnight.
func firstOfNextMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
}

// linearProjection extrapolates mtdCost (accrued from the 1st of now's
// month through now) to a full-month estimate, used as the
// forecast_method "linear" fallback for both AWS (forecast call failed)
// and OCI (no forecast API at all).
func linearProjection(mtdCost float64, now time.Time) float64 {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	daysElapsed := now.Sub(monthStart).Hours()/24 + 1 // inclusive of today
	if daysElapsed <= 0 {
		return mtdCost
	}
	daysInMonth := float64(firstOfNextMonth(now).Sub(monthStart).Hours() / 24)
	if daysInMonth <= 0 {
		return mtdCost
	}
	return mtdCost / daysElapsed * daysInMonth
}

// errSnapshot builds a models.CloudBillingError snapshot with detail,
// used for conditions classify() doesn't cover (e.g. Run itself
// returning a non-nil error, or a JSON parse failure on an exit-0 call).
func errSnapshot(provider models.CloudBillingProvider, detail string) models.CloudCostSnapshot {
	return models.CloudCostSnapshot{Provider: provider, Status: models.CloudBillingError, StatusDetail: detail}
}
