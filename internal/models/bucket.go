package models

// StorageProvider identifies a cloud object storage provider.
type StorageProvider string

// Supported storage providers.
const (
	StorageS3 StorageProvider = "s3"
	StorageR2 StorageProvider = "r2"
)

// BucketStats is the latest collected statistics for one bucket.
type BucketStats struct {
	Provider    StorageProvider `json:"provider"`
	Bucket      string          `json:"bucket"`
	Region      string          `json:"region,omitempty"`
	CollectedAt int64           `json:"collected_at"`
	SizeBytes   uint64          `json:"size_bytes"`
	ObjectCount uint64          `json:"object_count"`
	// ClassAOpsMTD counts write/list (mutating) ops, month-to-date.
	ClassAOpsMTD uint64 `json:"class_a_ops_mtd"`
	// ClassBOpsMTD counts read ops, month-to-date.
	ClassBOpsMTD uint64 `json:"class_b_ops_mtd"`
	// RequestsWindow counts all requests during the last collection
	// window.
	RequestsWindow uint64 `json:"requests_window"`
	WindowSeconds  int64  `json:"window_seconds"`
	EgressBytesMTD uint64 `json:"egress_bytes_mtd"`

	RequestMetricsAvailable bool   `json:"request_metrics_available"`
	Error                   string `json:"error,omitempty"`
}

// BucketPoint is a single historical data point for a bucket.
type BucketPoint struct {
	Timestamp      int64  `json:"ts"`
	SizeBytes      uint64 `json:"size_bytes"`
	ObjectCount    uint64 `json:"object_count"`
	RequestsWindow uint64 `json:"requests_window"`
	ClassAOpsMTD   uint64 `json:"class_a_ops_mtd"`
	ClassBOpsMTD   uint64 `json:"class_b_ops_mtd"`
}

// FreeTier describes a provider's free storage/operations allowance.
type FreeTier struct {
	StorageBytes uint64 `json:"storage_bytes"`
	ClassAOps    uint64 `json:"class_a_ops"`
	ClassBOps    uint64 `json:"class_b_ops"`
}

// R2FreeTier is Cloudflare R2's free tier: 10 GB-month of storage
// (approximated here as bytes stored), 1,000,000 Class A ops, and
// 10,000,000 Class B ops.
var R2FreeTier = FreeTier{
	StorageBytes: 10 * GiB,
	ClassAOps:    1_000_000,
	ClassBOps:    10_000_000,
}

// BucketView is the read-model for a single bucket: latest stats, recent
// history, and an optional free-tier reference (set to &R2FreeTier for R2
// buckets).
type BucketView struct {
	Latest   BucketStats   `json:"latest"`
	History  []BucketPoint `json:"history"`
	FreeTier *FreeTier     `json:"free_tier,omitempty"`
}

// CollectorStatus reports the health of a background bucket collector.
type CollectorStatus struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	LastRun   int64  `json:"last_run"`
	LastError string `json:"last_error,omitempty"`
}
