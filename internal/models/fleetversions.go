package models

// RemoteUpdateCapabilityClass buckets a host's RemoteUpdateCapability
// into one of three coarse states for the rollout dashboard (SPEC-v0.8
// §3-1): "capable" (supported and opted in — an "Update all" batch
// would target it), "not_opted_in" (supported, but the agent's own
// CP_REMOTE_UPDATE setting is off), or "unsupported" (the OS/agent-
// version combination cannot receive a remote update job at all).
type RemoteUpdateCapabilityClass string

// Supported classes.
const (
	RemoteUpdateClassCapable     RemoteUpdateCapabilityClass = "capable"
	RemoteUpdateClassNotOptedIn  RemoteUpdateCapabilityClass = "not_opted_in"
	RemoteUpdateClassUnsupported RemoteUpdateCapabilityClass = "unsupported"
)

// FleetVersionHost is one host's row in GET /api/v1/fleet/versions.
type FleetVersionHost struct {
	HostID   string `json:"host_id"`
	Hostname string `json:"hostname"`
	// Version is the host's reported agent version, "" if never
	// reported.
	Version string `json:"version"`
	// OS is the host's reported OS family ("linux", "darwin",
	// "windows"), or "" for a host that has never reported one.
	OS string `json:"os"`
	// Outdated is true when Version is a parseable release tag older
	// than LatestVersion (see FleetVersionSummary.LatestVersion); an
	// unparseable/empty Version, or an unknown latest, is never
	// reported outdated (there is nothing to compare against).
	Outdated bool `json:"outdated"`
	// OutdatedSince is the unix-seconds time this host was first
	// observed to be outdated, 0 if not outdated or unknown. Callers
	// populate this from persisted history the aggregation function
	// itself does not have (see BuildFleetVersionSummary's doc
	// comment); this field is carried through, not derived, by the
	// pure aggregation.
	OutdatedSince int64 `json:"outdated_since,omitempty"`
	// Capability classifies whether/why a remote update batch could
	// target this host right now.
	Capability RemoteUpdateCapabilityClass `json:"capability"`
	// ManualCommand is the ready-to-run manual update command for this
	// host, matching AgentUpdate.Command's OS-aware text.
	ManualCommand string `json:"manual_command"`
}

// FleetVersionOSBucket is one (version, OS) pair's host count within a
// FleetVersionSummary's distribution.
type FleetVersionOSBucket struct {
	OS      string `json:"os"`
	Version string `json:"version"`
	Count   int    `json:"count"`
}

// FleetVersionSummary is the response body for
// GET /api/v1/fleet/versions (SPEC-v0.8 §3-1): fleet-wide version/OS
// distribution, the latest known release, and a per-host breakdown.
type FleetVersionSummary struct {
	// LatestVersion is the latest known release tag, or "" if never
	// successfully checked (mirrors VersionInfo.LatestVersion).
	LatestVersion string `json:"latest_version"`
	// TotalHosts is len(Hosts), included for convenience.
	TotalHosts int `json:"total_hosts"`
	// UpToDateCount is the number of hosts whose Version equals
	// LatestVersion exactly.
	UpToDateCount int `json:"up_to_date_count"`
	// OutdatedCount is the number of hosts with Outdated == true.
	OutdatedCount int `json:"outdated_count"`
	// UnknownCount is the number of hosts whose Version doesn't parse
	// (empty, "dev", or a dev-build string) so up-to-date/outdated
	// cannot be determined.
	UnknownCount int `json:"unknown_count"`
	// CapableCount/NotOptedInCount/UnsupportedCount partition
	// TotalHosts by Capability.
	CapableCount     int `json:"capable_count"`
	NotOptedInCount  int `json:"not_opted_in_count"`
	UnsupportedCount int `json:"unsupported_count"`
	// Distribution is the version x OS histogram, sorted by OS then by
	// descending semver (unparseable versions sort last, grouped
	// together, in original encounter order).
	Distribution []FleetVersionOSBucket `json:"distribution"`
	// Hosts is every host's row, sorted by Hostname then HostID for a
	// stable display order.
	Hosts []FleetVersionHost `json:"hosts"`
}

// UpdateBatchFailureReasonCount is one failure reason's count within a
// UpdateBatchSummary.
type UpdateBatchFailureReasonCount struct {
	Reason UpdateJobReason `json:"reason"`
	Count  int             `json:"count"`
}

// UpdateBatchSummary is one batch's row in
// GET /api/v1/agents/updates/batches (SPEC-v0.8 §3-1): aggregate
// counts, timing, and a failure-reason breakdown, without the full
// per-host job list (see GET /api/v1/agents/updates?batch= for that).
type UpdateBatchSummary struct {
	BatchID string `json:"batch_id"`
	Target  string `json:"target"`
	// TargetCount is the number of jobs in the batch.
	TargetCount int `json:"target_count"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	// Pending counts jobs still queued or in_progress.
	Pending int `json:"pending"`
	// CreatedAt is the batch's earliest job's CreatedAt.
	CreatedAt int64 `json:"created_at"`
	// FinishedAt is the latest terminal job's UpdatedAt, 0 while the
	// batch still has a pending job.
	FinishedAt int64 `json:"finished_at,omitempty"`
	// DurationSec is FinishedAt - CreatedAt, 0 while still pending.
	DurationSec int64 `json:"duration_sec,omitempty"`
	// FailureReasons is the distribution of Reason across this batch's
	// failed jobs, sorted by descending count then by Reason for a
	// stable order. Empty (not null) when Failed == 0.
	FailureReasons []UpdateBatchFailureReasonCount `json:"failure_reasons"`
}
