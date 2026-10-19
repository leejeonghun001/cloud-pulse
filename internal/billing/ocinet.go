package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// maxOCINetworkVNICs bounds how many VNICs one collection queries, two
// CLI calls each.
const maxOCINetworkVNICs = 8

// OCINetworkConfig configures CollectOCINetwork.
type OCINetworkConfig struct {
	// CompartmentID is the instance's compartment OCID; VNIC metrics
	// are queried in it.
	CompartmentID string
	// VNICIDs are the instance's VNIC OCIDs.
	VNICIDs []string
	Env     []string
	HomeDir string
	Auth    string
	CLI     string
}

// ociMetricOutput mirrors `oci monitoring metric-data
// summarize-metrics-data` JSON: "data" is a list of metric streams, each
// with "aggregated-datapoints" (the REST API's camelCase spelling is
// accepted too).
type ociMetricOutput struct {
	Data []struct {
		Points      []ociMetricPoint `json:"aggregated-datapoints"`
		PointsCamel []ociMetricPoint `json:"aggregatedDatapoints"`
	} `json:"data"`
}

type ociMetricPoint struct {
	Value float64 `json:"value"`
}

// CollectOCINetwork sums an instance's VNIC traffic for the current UTC
// month from OCI Monitoring (namespace oci_vnic): VnicFromNetworkBytes
// is inbound, VnicToNetworkBytes outbound. These are OCI's own counters
// for every byte through the VNIC, internal VCN traffic included — not
// the billed internet egress. now is injected for tests.
func CollectOCINetwork(ctx context.Context, runner CommandRunner, cfg OCINetworkConfig, now time.Time) models.CloudNetworkUsage {
	now = now.UTC()
	usage := models.CloudNetworkUsage{Provider: models.CloudBillingOCI, Month: now.Format("2006-01")}
	if cfg.CompartmentID == "" || len(cfg.VNICIDs) == 0 {
		usage.Status = models.CloudBillingNotConfigured
		usage.StatusDetail = "instance compartment or VNIC unknown"
		return usage
	}
	vnics := cfg.VNICIDs
	if len(vnics) > maxOCINetworkVNICs {
		vnics = vnics[:maxOCINetworkVNICs]
	}

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	// End on the next whole hour so the current partial day is included.
	end := now.Truncate(time.Hour).Add(time.Hour)
	cli := cfg.CLI
	if cli == "" {
		cli = "oci"
	}

	var in, out float64
	for _, vnic := range vnics {
		for _, metric := range []string{"VnicFromNetworkBytes", "VnicToNetworkBytes"} {
			args := []string{
				"monitoring", "metric-data", "summarize-metrics-data",
				"--compartment-id", cfg.CompartmentID,
				"--namespace", "oci_vnic",
				"--query-text", fmt.Sprintf(`%s[1d]{resourceId = "%s"}.sum()`, metric, vnic),
				"--start-time", monthStart.Format("2006-01-02T15:04:05Z"),
				"--end-time", end.Format("2006-01-02T15:04:05Z"),
				"--output", "json",
			}
			if cfg.Auth != "" {
				args = append(args, "--auth", cfg.Auth)
			}
			sum, failed := runOCIMetricSum(ctx, runner, cfg, cli, args)
			if failed != nil {
				failed.Provider = usage.Provider
				failed.Month = usage.Month
				return *failed
			}
			if metric == "VnicFromNetworkBytes" {
				in += sum
			} else {
				out += sum
			}
		}
	}
	usage.Status = models.CloudBillingOK
	usage.InBytes = toBytes(in)
	usage.OutBytes = toBytes(out)
	return usage
}

// runOCIMetricSum runs one summarize call and sums every datapoint, or
// returns a failed usage carrying the classified status.
func runOCIMetricSum(ctx context.Context, runner CommandRunner, cfg OCINetworkConfig, cli string, args []string) (float64, *models.CloudNetworkUsage) {
	res, err := runner.Run(ctx, cfg.HomeDir, cfg.Env, cli, args...)
	if err != nil {
		return 0, &models.CloudNetworkUsage{Status: models.CloudBillingError, StatusDetail: fmt.Sprintf("run oci monitoring: %v", err)}
	}
	if res.NotFound || res.TimedOut || res.ExitCode != 0 {
		c := classify(res)
		return 0, &models.CloudNetworkUsage{Status: c.Status, StatusDetail: c.Detail}
	}
	var parsed ociMetricOutput
	if err := json.Unmarshal(res.Stdout, &parsed); err != nil {
		return 0, &models.CloudNetworkUsage{Status: models.CloudBillingError, StatusDetail: "parse summarize-metrics-data output"}
	}
	var sum float64
	for _, stream := range parsed.Data {
		points := stream.Points
		if len(points) == 0 {
			points = stream.PointsCamel
		}
		for _, p := range points {
			sum += p.Value
		}
	}
	return sum, nil
}

func toBytes(v float64) uint64 {
	if v <= 0 || math.IsNaN(v) {
		return 0
	}
	if v >= math.MaxUint64/2 {
		return math.MaxUint64 / 2
	}
	return uint64(math.Round(v))
}
