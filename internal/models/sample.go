package models

// DiskUsage is the usage snapshot of a single mounted filesystem.
type DiskUsage struct {
	Mountpoint  string  `json:"mountpoint"`
	Device      string  `json:"device"`
	FSType      string  `json:"fstype"`
	TotalBytes  uint64  `json:"total_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

// Sample is a single point-in-time resource measurement for a host.
type Sample struct {
	// Timestamp is unix seconds UTC.
	Timestamp int64 `json:"ts"`

	CPUPercent float64 `json:"cpu_percent"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`

	// MemUsed is Total - Available ("true available"), not Total - Free.
	MemTotal       uint64  `json:"mem_total"`
	MemAvailable   uint64  `json:"mem_available"`
	MemUsed        uint64  `json:"mem_used"`
	MemUsedPercent float64 `json:"mem_used_percent"`
	MemCached      uint64  `json:"mem_cached"`

	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`

	// DiskTotal/DiskUsed/DiskUsedPercent are aggregates across all
	// non-pseudo filesystems, deduped by device.
	DiskTotal       uint64      `json:"disk_total"`
	DiskUsed        uint64      `json:"disk_used"`
	DiskUsedPercent float64     `json:"disk_used_percent"`
	Disks           []DiskUsage `json:"disks,omitempty"`

	DiskReadBps  float64 `json:"disk_read_bps"`
	DiskWriteBps float64 `json:"disk_write_bps"`

	NetRxBps float64 `json:"net_rx_bps"`
	NetTxBps float64 `json:"net_tx_bps"`
	// NetRxBytes/NetTxBytes are the byte deltas accumulated during THIS
	// sample's interval.
	NetRxBytes uint64 `json:"net_rx_bytes"`
	NetTxBytes uint64 `json:"net_tx_bytes"`

	UptimeSeconds uint64 `json:"uptime_seconds"`
}

// AgentReport is the payload an agent pushes to the hub's ingest endpoint.
type AgentReport struct {
	Host    HostInfo `json:"host"`
	Samples []Sample `json:"samples"`
}

// IngestResponse is returned by the hub after processing an AgentReport.
type IngestResponse struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Rejected   int `json:"rejected"`
	// ServerTimeMs is the hub's wall clock (unix milliseconds) at the
	// moment the response was produced, used by agents to synchronize
	// their clock to the hub's.
	ServerTimeMs int64 `json:"server_time_ms"`
	// LatestVersion is the latest cloud-pulse-agent release tag known to
	// the hub, omitted when unknown. Agents use it to log a one-time
	// notice when a newer version is available.
	LatestVersion string `json:"latest_version,omitempty"`
}

// TimeResponse is returned by the hub's time-sync endpoint so agents can
// estimate their clock offset from the hub without submitting a report.
type TimeResponse struct {
	// ServerTimeMs is the hub's wall clock (unix milliseconds) at the
	// moment the response was produced.
	ServerTimeMs int64 `json:"server_time_ms"`
}

// APIError is the JSON body of a hub error response.
type APIError struct {
	Error string `json:"error"`
	// Code is a machine-readable error identifier (e.g.
	// "admin_disabled"), omitted when there is no specific code.
	Code string `json:"code,omitempty"`
	// RetryAfterSeconds accompanies a 429 rate-limited response,
	// mirroring the Retry-After header in the JSON body so browser
	// clients don't need to read response headers. Omitted for
	// responses that aren't rate-limited.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
}
