package models

// ListeningPort is one TCP/UDP socket an agent found bound/listening on
// its host, collected via gopsutil's net.ConnectionsWithContext.
type ListeningPort struct {
	// Proto is "tcp" or "udp".
	Proto string `json:"proto"`
	IP    string `json:"ip"`
	Port  int    `json:"port"`
	// PID is the owning process id, 0 when unknown (the agent runs
	// unprivileged and couldn't resolve it).
	PID int32 `json:"pid"`
	// Process is the owning process's name, "" when unknown.
	Process string `json:"process"`
	// ContainerID is the 12-character Docker container ID this port was
	// matched to via a published-port lookup, "" when it isn't a
	// container's published port.
	ContainerID string `json:"container_id,omitempty"`
}

// ContainerPort is one published port mapping reported by the Docker
// Engine API for a container.
type ContainerPort struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"private_port"`
	PublicPort  int    `json:"public_port,omitempty"`
	// Type is "tcp" or "udp".
	Type string `json:"type"`
}

// Container is one Docker (or Podman, via its compatible socket)
// container as reported by GET /containers/json.
type Container struct {
	// ID is the container's short (12-character) ID.
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
	// State is Docker's raw state ("running", "exited", "paused", ...).
	State string `json:"state"`
	// Status is Docker's human-readable status text (e.g. "Up 3 days").
	Status string `json:"status"`
	// Health is parsed from Status when it contains a health suffix
	// (e.g. "(healthy)"); "" when the container has no healthcheck.
	Health    string `json:"health,omitempty"`
	CreatedAt int64  `json:"created_at"`
	// ComposeProject/ComposeService come from the
	// com.docker.compose.project/service labels; both "" when the
	// container wasn't created by Compose.
	ComposeProject string          `json:"compose_project,omitempty"`
	ComposeService string          `json:"compose_service,omitempty"`
	Ports          []ContainerPort `json:"ports"`
}

// DockerStatus reports the outcome of an agent's attempt to reach the
// local Docker (or Podman) Engine API.
type DockerStatus string

// Docker collection outcomes.
const (
	DockerStatusOK               DockerStatus = "ok"
	DockerStatusUnavailable      DockerStatus = "unavailable"
	DockerStatusPermissionDenied DockerStatus = "permission_denied"
	DockerStatusError            DockerStatus = "error"
	// DockerStatusUnsupported is reported on platforms the agent's
	// Docker collector does not implement yet (e.g. Windows npipe).
	DockerStatusUnsupported DockerStatus = "unsupported"
)

// DockerInfo is the result of one agent's Docker Engine API collection
// attempt.
type DockerInfo struct {
	Status DockerStatus `json:"status"`
	// Error is a short message describing why Status isn't "ok"; empty
	// when Status is "ok".
	Error string `json:"error,omitempty"`
	// Version is the Docker Engine API version string, "" when Status
	// isn't "ok".
	Version    string      `json:"version,omitempty"`
	Containers []Container `json:"containers"`
}

// Inventory caps, applied by the agent's collector before sending an
// AgentReport.
const (
	MaxInventoryPorts      = 1000
	MaxInventoryContainers = 500
)

// Inventory is a host's point-in-time snapshot of listening ports and
// Docker containers, collected by the agent every 60s (internal/agent's
// collection interval, independent of the sample interval) and attached
// to an AgentReport when it has changed or every 10 minutes, whichever
// comes first.
type Inventory struct {
	CollectedAt int64 `json:"collected_at"`
	// Ports is encoded as [] rather than null when empty, capped at
	// MaxInventoryPorts.
	Ports  []ListeningPort `json:"ports"`
	Docker DockerInfo      `json:"docker"`
}
