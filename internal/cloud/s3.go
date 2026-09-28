package cloud

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// defaultS3Window is the default collection window used to compute
// BucketStats.RequestsWindow when S3Collector.Window is unset.
const defaultS3Window = 15 * time.Minute

// defaultFilterID is the default S3 request metrics filter ID.
const defaultFilterID = "EntireBucket"

// s3StorageLookback bounds how far back BucketSizeBytes/NumberOfObjects
// are queried; S3 storage metrics are daily, so the latest datapoint
// may lag by up to a day or two.
const s3StorageLookback = 3 * 24 * time.Hour

// classAMetrics are S3 request metrics billed as Class A (mutating:
// write/list) operations. PutRequests covers PUT/COPY, PostRequests
// covers POST, ListRequests covers LIST. DeleteRequests are free on
// S3 and intentionally excluded. See notes/cloud.md for the pricing
// citation.
var classAMetrics = []string{"PutRequests", "ListRequests", "PostRequests"}

// classBMetrics are S3 request metrics billed as Class B (read)
// operations: GET/SELECT/HEAD-tier reads.
var classBMetrics = []string{"GetRequests", "HeadRequests", "SelectRequests"}

// requestMetricNames are all S3 per-class request metrics queried per
// bucket, in addition to BytesDownloaded and AllRequests.
func requestMetricNames() []string {
	names := make([]string, 0, len(classAMetrics)+len(classBMetrics))
	names = append(names, classAMetrics...)
	names = append(names, classBMetrics...)
	return names
}

// isClassA reports whether the named S3 request metric is billed as a
// Class A (mutating write/list) operation.
func isClassA(metricName string) bool {
	for _, n := range classAMetrics {
		if n == metricName {
			return true
		}
	}
	return false
}

// S3Bucket identifies one S3 bucket to collect statistics for.
type S3Bucket struct {
	Name   string
	Region string
}

// S3Collector implements the hub BucketCollector interface for Amazon
// S3, using CloudWatch metrics (no AWS SDK; hand-written SigV4).
type S3Collector struct {
	// Buckets is the set of buckets to collect. Required.
	Buckets []S3Bucket
	// FilterID is the S3 request-metrics filter ID configured on
	// each bucket. Defaults to "EntireBucket".
	FilterID string
	// Creds are the AWS credentials used to sign CloudWatch calls.
	Creds Credentials
	// Client is the HTTP client used for CloudWatch calls. If nil,
	// http.DefaultClient is used.
	Client *http.Client
	// Now returns the current time. If nil, time.Now is used.
	Now func() time.Time
	// Window is the trailing collection window used for
	// RequestsWindow/WindowSeconds. Defaults to 15 minutes.
	Window time.Duration
	// Endpoint overrides the CloudWatch endpoint used per region;
	// used in tests. If nil, the default regional endpoint is used.
	Endpoint func(region string) string
}

// Name returns the collector name, "s3".
func (c *S3Collector) Name() string { return "s3" }

// Collect gathers BucketStats for every configured bucket, grouping
// CloudWatch calls by region (one GetMetricData batch per region).
// Per-bucket failures are recorded in BucketStats.Error and do not
// prevent other buckets from being collected; Collect returns a
// non-nil error only if every bucket failed.
func (c *S3Collector) Collect(ctx context.Context) ([]models.BucketStats, error) {
	if len(c.Buckets) == 0 {
		return nil, nil
	}

	now := c.now()
	window := c.window()
	filterID := c.filterID()

	type regionGroup struct {
		region  string
		buckets []S3Bucket
	}
	var groups []regionGroup
	groupIndex := map[string]int{}
	for _, b := range c.Buckets {
		gi, ok := groupIndex[b.Region]
		if !ok {
			gi = len(groups)
			groupIndex[b.Region] = gi
			groups = append(groups, regionGroup{region: b.Region})
		}
		groups[gi].buckets = append(groups[gi].buckets, b)
	}

	stats := make(map[string]models.BucketStats, len(c.Buckets))
	failures := 0

	for _, g := range groups {
		regionStats, err := c.collectRegion(ctx, g.region, g.buckets, now, window, filterID)
		if err != nil {
			// The whole region's CloudWatch calls failed; record the
			// same error against every bucket in that region.
			for _, b := range g.buckets {
				stats[bucketKey(b)] = models.BucketStats{
					Provider:    models.StorageS3,
					Bucket:      b.Name,
					Region:      b.Region,
					CollectedAt: now.Unix(),
					Error:       err.Error(),
				}
				failures++
			}
			continue
		}
		for _, b := range g.buckets {
			s := regionStats[bucketKey(b)]
			if s.Error != "" {
				failures++
			}
			stats[bucketKey(b)] = s
		}
	}

	out := make([]models.BucketStats, 0, len(c.Buckets))
	for _, b := range c.Buckets {
		out = append(out, stats[bucketKey(b)])
	}

	if failures == len(c.Buckets) {
		return out, fmt.Errorf("cloud: s3: collect: all %d bucket(s) failed", len(c.Buckets))
	}
	return out, nil
}

// bucketKey returns a map key uniquely identifying b within the
// configured bucket set (region+name, since bucket names are globally
// unique in S3 but we key defensively by region too).
func bucketKey(b S3Bucket) string {
	return b.Region + "/" + b.Name
}

// collectRegion issues the CloudWatch GetMetricData batch for all
// buckets in one region and assembles per-bucket BucketStats.
func (c *S3Collector) collectRegion(ctx context.Context, region string, buckets []S3Bucket, now time.Time, window time.Duration, filterID string) (map[string]models.BucketStats, error) {
	cw := &CloudWatch{
		Region: region,
		Creds:  c.Creds,
		Client: c.Client,
		Now:    c.Now,
	}
	if c.Endpoint != nil {
		cw.Endpoint = c.Endpoint(region)
	}

	queries := buildS3Queries(buckets, filterID)

	monthStart := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	start := now.Add(-s3StorageLookback)
	if monthStart.Before(start) {
		start = monthStart
	}
	if windowStart := now.Add(-window); windowStart.Before(start) {
		start = windowStart
	}

	results, err := cw.GetMetricData(ctx, queries, start, now)
	if err != nil {
		return nil, fmt.Errorf("cloud: s3: get metric data: %w", err)
	}

	out := make(map[string]models.BucketStats, len(buckets))
	for i, b := range buckets {
		out[bucketKey(b)] = buildBucketStats(b, i, results, now, window)
	}
	return out, nil
}

// buildS3Queries constructs the full set of MetricQuery entries for
// all buckets in one region: storage size, object count, per-class
// request counts, egress bytes, and the trailing-window request
// count. Query IDs are indexed positionally (bucket index i within
// this call), matched by buildBucketStats.
func buildS3Queries(buckets []S3Bucket, filterID string) []MetricQuery {
	var queries []MetricQuery
	for i, b := range buckets {
		bucketDim := Dimension{Name: "BucketName", Value: b.Name}

		queries = append(queries,
			MetricQuery{
				ID: fmt.Sprintf("size%d", i),
				Metric: Metric{
					Namespace: "AWS/S3", MetricName: "BucketSizeBytes",
					Dimensions: []Dimension{bucketDim, {Name: "StorageType", Value: "StandardStorage"}},
				},
				Period: 86400, Stat: "Average",
			},
			MetricQuery{
				ID: fmt.Sprintf("objs%d", i),
				Metric: Metric{
					Namespace: "AWS/S3", MetricName: "NumberOfObjects",
					Dimensions: []Dimension{bucketDim, {Name: "StorageType", Value: "AllStorageTypes"}},
				},
				Period: 86400, Stat: "Average",
			},
			MetricQuery{
				ID: fmt.Sprintf("egress%d", i),
				Metric: Metric{
					Namespace: "AWS/S3", MetricName: "BytesDownloaded",
					Dimensions: []Dimension{bucketDim, {Name: "FilterId", Value: filterID}},
				},
				Period: 86400, Stat: "Sum",
			},
			MetricQuery{
				ID: fmt.Sprintf("allreq%d", i),
				Metric: Metric{
					Namespace: "AWS/S3", MetricName: "AllRequests",
					Dimensions: []Dimension{bucketDim, {Name: "FilterId", Value: filterID}},
				},
				Period: 60, Stat: "Sum",
			},
		)

		for mi, name := range requestMetricNames() {
			queries = append(queries, MetricQuery{
				ID: fmt.Sprintf("req%d_%d", i, mi),
				Metric: Metric{
					Namespace: "AWS/S3", MetricName: name,
					Dimensions: []Dimension{bucketDim, {Name: "FilterId", Value: filterID}},
				},
				Period: 86400, Stat: "Sum",
			})
		}
	}
	return queries
}

// buildBucketStats assembles one bucket's BucketStats from the merged
// CloudWatch results, where i is the bucket's positional index used
// when building query IDs in buildS3Queries.
func buildBucketStats(b S3Bucket, i int, results map[string][]Datapoint, now time.Time, window time.Duration) models.BucketStats {
	stats := models.BucketStats{
		Provider:    models.StorageS3,
		Bucket:      b.Name,
		Region:      b.Region,
		CollectedAt: now.Unix(),
	}

	stats.SizeBytes = uint64(latestValue(results[fmt.Sprintf("size%d", i)]))
	stats.ObjectCount = uint64(latestValue(results[fmt.Sprintf("objs%d", i)]))
	stats.EgressBytesMTD = uint64(sumValues(results[fmt.Sprintf("egress%d", i)]))
	stats.RequestsWindow = uint64(sumValues(results[fmt.Sprintf("allreq%d", i)]))
	stats.WindowSeconds = int64(window.Seconds())

	anyRequestData := len(results[fmt.Sprintf("allreq%d", i)]) > 0
	for mi, name := range requestMetricNames() {
		dps := results[fmt.Sprintf("req%d_%d", i, mi)]
		if len(dps) > 0 {
			anyRequestData = true
		}
		sum := uint64(sumValues(dps))
		if isClassA(name) {
			stats.ClassAOpsMTD += sum
		} else {
			stats.ClassBOpsMTD += sum
		}
	}
	stats.RequestMetricsAvailable = anyRequestData

	return stats
}

// latestValue returns the value of the most recent datapoint in dps
// (by Timestamp), or 0 if dps is empty.
func latestValue(dps []Datapoint) float64 {
	if len(dps) == 0 {
		return 0
	}
	latest := dps[0]
	for _, dp := range dps[1:] {
		if dp.Timestamp.After(latest.Timestamp) {
			latest = dp
		}
	}
	return latest.Value
}

// sumValues sums the values of all datapoints in dps.
func sumValues(dps []Datapoint) float64 {
	var total float64
	for _, dp := range dps {
		total += dp.Value
	}
	return total
}

// now returns c.Now(), or time.Now() if unset.
func (c *S3Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// window returns c.Window, or defaultS3Window if unset.
func (c *S3Collector) window() time.Duration {
	if c.Window > 0 {
		return c.Window
	}
	return defaultS3Window
}

// filterID returns c.FilterID, or defaultFilterID if unset.
func (c *S3Collector) filterID() string {
	if c.FilterID != "" {
		return c.FilterID
	}
	return defaultFilterID
}
