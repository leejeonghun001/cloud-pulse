// Package agent's inventory.go implements SPEC-v0.5 §C's agent-side
// inventory collector: listening TCP/UDP ports (via gopsutil) and
// running Docker (or Podman) containers (via a hand-rolled Engine API
// client over a unix socket, std lib only).
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"syscall"
	"time"

	gonet "github.com/shirou/gopsutil/v4/net"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// inventoryCollectTimeout bounds one full InventoryCollector.Collect
// call (ports + Docker).
const inventoryCollectTimeout = 15 * time.Second

// InventoryCollector gathers a models.Inventory snapshot: the host's
// listening TCP/UDP ports and, when configured, its Docker container
// list. It has no state between calls other than its configuration, so
// a single instance can be reused across every 60s collection tick (see
// SPEC-v0.5 §C and the doc comment on models.Inventory for the exact
// cadence/change-detection rule, implemented by the caller in run.go,
// not here).
type InventoryCollector struct {
	docker *dockerClient
	logger *slog.Logger

	// connections is gonet.ConnectionsWithContext by default; tests
	// inject a fake to avoid depending on the real host's socket table.
	connections func(ctx context.Context, kind string) ([]gonet.ConnectionStat, error)
	// processName resolves a PID to a process name, best-effort ("" on
	// failure/unsupported platform); tests inject a fake.
	processName func(pid int32) string
}

// InventoryCollectorOptions configures NewInventoryCollector.
type InventoryCollectorOptions struct {
	// Docker is the resolved CP_DOCKER setting: "off" disables Docker
	// collection entirely (DockerStatusUnavailable is never even
	// attempted), "auto" probes the platform default socket, anything
	// else is treated as an explicit socket path/URL. See
	// config.LoadAgent's CP_DOCKER parsing for the exact resolution
	// rule.
	Docker string
	// Logger receives debug logs for individual collection failures. If
	// nil, a discard logger is used.
	Logger *slog.Logger
}

// NewInventoryCollector constructs an InventoryCollector from opts.
func NewInventoryCollector(opts InventoryCollectorOptions) *InventoryCollector {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &InventoryCollector{
		docker:      newDockerClient(opts.Docker),
		logger:      logger,
		connections: gonet.ConnectionsWithContext,
		processName: processNameForPID,
	}
}

// Collect gathers one models.Inventory snapshot, stamped with
// CollectedAt = now. Port/Docker collection failures are logged and
// degrade gracefully (an empty port list, or a DockerInfo with a
// non-"ok" Status) rather than failing the whole collection — Collect
// only returns an error if ctx is already done.
func (c *InventoryCollector) Collect(ctx context.Context, now time.Time) (models.Inventory, error) {
	if err := ctx.Err(); err != nil {
		return models.Inventory{}, fmt.Errorf("agent: collect inventory: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, inventoryCollectTimeout)
	defer cancel()

	docker := c.docker.collect(ctx)

	ports := c.collectPorts(ctx)
	matchContainerPorts(ports, docker.Containers)

	return models.Inventory{
		CollectedAt: now.Unix(),
		Ports:       ports,
		Docker:      docker,
	}, nil
}

// collectPorts returns the deduplicated set of TCP LISTEN and UDP bound
// sockets on the host, sorted for deterministic output, capped at
// models.MaxInventoryPorts.
func (c *InventoryCollector) collectPorts(ctx context.Context) []models.ListeningPort {
	conns, err := c.connections(ctx, "inet")
	if err != nil {
		c.logger.DebugContext(ctx, "inventory: list connections failed", "error", err)
		return []models.ListeningPort{}
	}

	type key struct {
		proto string
		ip    string
		port  int
	}
	seen := make(map[key]models.ListeningPort)

	for _, conn := range conns {
		proto, ok := listeningProto(conn)
		if !ok {
			continue
		}
		k := key{proto: proto, ip: conn.Laddr.IP, port: int(conn.Laddr.Port)}
		if _, dup := seen[k]; dup {
			continue
		}
		process := ""
		if conn.Pid > 0 {
			process = c.processName(conn.Pid)
		}
		seen[k] = models.ListeningPort{
			Proto:   proto,
			IP:      conn.Laddr.IP,
			Port:    int(conn.Laddr.Port),
			PID:     conn.Pid,
			Process: process,
		}
	}

	out := make([]models.ListeningPort, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Proto != out[j].Proto {
			return out[i].Proto < out[j].Proto
		}
		if out[i].IP != out[j].IP {
			return out[i].IP < out[j].IP
		}
		return out[i].Port < out[j].Port
	})
	if len(out) > models.MaxInventoryPorts {
		out = out[:models.MaxInventoryPorts]
	}
	return out
}

// listeningProto reports whether conn represents a listening/bound
// socket worth reporting, and if so its protocol ("tcp" or "udp").
// TCP: only the LISTEN state (an established/outbound connection is not
// a "service" the host offers). UDP has no listen state in gopsutil's
// model; a UDP socket bound to a local port with no remote address is
// treated as "bound" (the closest UDP analogue of listening).
func listeningProto(conn gonet.ConnectionStat) (proto string, ok bool) {
	switch conn.Type {
	case syscall.SOCK_STREAM:
		if conn.Status == "LISTEN" {
			return "tcp", true
		}
		return "", false
	case syscall.SOCK_DGRAM:
		if conn.Raddr.IP == "" && conn.Raddr.Port == 0 {
			return "udp", true
		}
		return "", false
	default:
		return "", false
	}
}

// matchContainerPorts fills ListeningPort.ContainerID for any port in
// ports whose (proto, PublicPort) matches a published port on one of
// containers. Docker publishes ports on all interfaces as "0.0.0.0" (or
// "::" for IPv6) regardless of what the agent's socket table reports
// for the containerd-proxy/kernel NAT listener, so matching is done on
// proto+port only, not IP.
func matchContainerPorts(ports []models.ListeningPort, containers []models.Container) {
	type portKey struct {
		proto string
		port  int
	}
	published := make(map[portKey]string, len(containers))
	for _, ctr := range containers {
		for _, cp := range ctr.Ports {
			if cp.PublicPort == 0 {
				continue
			}
			published[portKey{proto: cp.Type, port: cp.PublicPort}] = ctr.ID
		}
	}
	for i := range ports {
		if id, ok := published[portKey{proto: ports[i].Proto, port: ports[i].Port}]; ok {
			ports[i].ContainerID = id
		}
	}
}
