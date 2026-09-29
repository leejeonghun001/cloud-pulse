package models

// VersionInfo is the response body for GET /api/v1/version: build
// metadata plus the hub's self-update check status.
type VersionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`

	// LatestVersion is the latest release tag known to the hub (e.g.
	// "v0.3.1"), or "" if never successfully checked.
	LatestVersion string `json:"latest_version"`
	// UpdateAvailable reports whether LatestVersion is newer than
	// Version.
	UpdateAvailable bool `json:"update_available"`
	// UpdateCheckEnabled reflects CP_UPDATE_CHECK; false means the hub
	// never contacts GitHub for release information.
	UpdateCheckEnabled bool `json:"update_check_enabled"`
	// CheckedAt is the unix-seconds time of the last check attempt
	// (successful or not), or 0 if no check has run yet.
	CheckedAt int64 `json:"checked_at"`
	// CheckError is the most recent check failure's message, omitted
	// when the last check succeeded or none has run yet.
	CheckError string `json:"check_error,omitempty"`
	// ReleaseURL is the GitHub release page for LatestVersion, or "" if
	// LatestVersion is unknown.
	ReleaseURL string `json:"release_url"`
	// UpdateCommand is the command an operator runs to self-update the
	// hub, e.g. "sudo cloud-pulse-hub update".
	UpdateCommand string `json:"update_command"`
}

// AgentUpdate describes the update available (if any) for one agent, as
// shown on that host's dashboard card/detail panel.
type AgentUpdate struct {
	// Available reports whether a newer cloud-pulse-agent version than
	// the one this host is running is known.
	Available bool `json:"available"`
	// Latest is the latest known version tag (e.g. "v0.3.1").
	Latest string `json:"latest"`
	// SelfUpdate reports whether the host's agent version is new enough
	// to have the built-in `update` subcommand (>= version.SelfUpdateSince).
	SelfUpdate bool `json:"self_update"`
	// Command is the ready-to-run command an operator uses to update
	// this agent.
	Command string `json:"command"`
}
