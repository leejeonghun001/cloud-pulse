package models

// UpdateJobState is the lifecycle state of one remote-update job
// (SPEC-v0.6 §2).
type UpdateJobState string

// Supported update job states.
const (
	UpdateJobQueued     UpdateJobState = "queued"
	UpdateJobInProgress UpdateJobState = "in_progress"
	UpdateJobSucceeded  UpdateJobState = "succeeded"
	UpdateJobFailed     UpdateJobState = "failed"
)

// UpdateJobReason is a machine-readable explanation for a failed (or,
// for AlreadyUpToDate, succeeded) update job.
type UpdateJobReason string

// Supported update job failure/completion reasons (SPEC-v0.6 §2).
const (
	UpdateReasonNone             UpdateJobReason = ""
	UpdateReasonNotEnabled       UpdateJobReason = "not_enabled"
	UpdateReasonUnsupported      UpdateJobReason = "unsupported"
	UpdateReasonTimeout          UpdateJobReason = "timeout"
	UpdateReasonDownloadFailed   UpdateJobReason = "download_failed"
	UpdateReasonChecksumMismatch UpdateJobReason = "checksum_mismatch"
	UpdateReasonVerifyFailed     UpdateJobReason = "verify_failed"
	UpdateReasonRestartFailed    UpdateJobReason = "restart_failed"
	UpdateReasonDowngradeRefused UpdateJobReason = "downgrade_refused"
	UpdateReasonAlreadyUpToDate  UpdateJobReason = "already_up_to_date"
	UpdateReasonUnknown          UpdateJobReason = "unknown"
)

// UpdateJob is one host's row in a remote-update batch (SPEC-v0.6 §2,
// table update_jobs).
type UpdateJob struct {
	ID      int64  `json:"id"`
	BatchID string `json:"batch_id"`
	HostID  string `json:"host_id"`
	// Target is "latest" or an exact tag ("vX.Y.Z").
	Target string          `json:"target"`
	State  UpdateJobState  `json:"state"`
	Reason UpdateJobReason `json:"reason,omitempty"`
	// Error is a short human-readable detail, empty unless State is
	// failed and more context than Reason alone is useful. Never
	// contains a secret.
	Error     string `json:"error,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	// Attempt counts retries: 1 for the original job, incremented for
	// each job created by a retry of a failed job.
	Attempt int `json:"attempt"`
}

// UpdateBatch groups the UpdateJobs created by one
// POST /api/v1/agents/updates call, for progress-summary display.
type UpdateBatch struct {
	BatchID     string      `json:"batch_id"`
	Target      string      `json:"target"`
	MaxParallel int         `json:"max_parallel"`
	CreatedAt   int64       `json:"created_at"`
	Jobs        []UpdateJob `json:"jobs"`
}

// UpdateBatchCreate is the request body for POST /api/v1/agents/updates.
type UpdateBatchCreate struct {
	HostIDs []string `json:"host_ids"`
	// Target is "latest" or an exact "vX.Y.Z" tag.
	Target string `json:"target"`
	// MaxParallel bounds how many jobs may be in_progress at once for
	// this batch; defaults to 3 when 0.
	MaxParallel int `json:"max_parallel,omitempty"`
}

// UpdateRequest is embedded in IngestResponse to hand a queued job to
// its agent on its next report (SPEC-v0.6 §2 step 2 — agents are
// push-only, so the hub can't contact them directly).
type UpdateRequest struct {
	JobID int64 `json:"job_id"`
	// Target is "latest" or an exact "vX.Y.Z" tag, copied from the
	// owning UpdateJob.
	Target string `json:"target"`
}

// AgentUpdateStatus is embedded in AgentReport by an agent reporting the
// outcome of a remote-update job it received (SPEC-v0.6 §2 step 5).
type AgentUpdateStatus struct {
	JobID int64 `json:"job_id"`
	// State is "succeeded" or "failed" — an agent never reports
	// "queued"/"in_progress" itself (the hub infers "in_progress" once
	// it sees the acknowledgement in the report that first carried
	// UpdateRequest for this job).
	State UpdateJobState `json:"state"`
	// Version is the agent's own version after attempting the update
	// (whether or not it changed), used by the hub to verify Target was
	// actually reached before recording success.
	Version string `json:"version"`
	// ErrorCode is one of UpdateJobReason's failure values, empty on
	// success.
	ErrorCode UpdateJobReason `json:"error_code,omitempty"`
	// Error is a short human-readable detail, never a secret.
	Error string `json:"error,omitempty"`
}

// RemoteUpdateCapability describes whether/why a host can receive a
// remote update job, shown on the Updates page per agent.
type RemoteUpdateCapability struct {
	// Supported is true only for Linux + systemd agents at
	// version.SelfUpdateSince or later.
	Supported bool `json:"supported"`
	// OptedIn reflects the agent's own CP_REMOTE_UPDATE=on setting, as
	// last reported.
	OptedIn bool `json:"opted_in"`
	// Reason explains why Supported/OptedIn is false, using the same
	// UpdateJobReason vocabulary ("unsupported" or "not_enabled"),
	// empty when both are true.
	Reason UpdateJobReason `json:"reason,omitempty"`
}
