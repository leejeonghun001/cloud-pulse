package agent

import (
	"context"
	"log/slog"
	"sort"
	"syscall"
	"testing"
	"time"

	gonet "github.com/shirou/gopsutil/v4/net"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// fakeConnections is a scriptable connections func for InventoryCollector
// tests, avoiding any dependency on the real host's socket table.
func fakeConnections(conns []gonet.ConnectionStat, err error) func(context.Context, string) ([]gonet.ConnectionStat, error) {
	return func(context.Context, string) ([]gonet.ConnectionStat, error) {
		return conns, err
	}
}

func newTestInventoryCollector() *InventoryCollector {
	c := NewInventoryCollector(InventoryCollectorOptions{Docker: "off"})
	c.connections = fakeConnections(nil, nil)
	c.processName = func(int32) string { return "" }
	return c
}

func TestInventoryCollector_PortDedupe(t *testing.T) {
	c := newTestInventoryCollector()
	c.connections = fakeConnections([]gonet.ConnectionStat{
		{Type: syscall.SOCK_STREAM, Status: "LISTEN", Laddr: gonet.Addr{IP: "0.0.0.0", Port: 8080}, Pid: 100},
		// Duplicate of the above (e.g. IPv4 and IPv6 dual-stack listeners
		// gopsutil sometimes reports separately for the same logical
		// port) must collapse to one entry.
		{Type: syscall.SOCK_STREAM, Status: "LISTEN", Laddr: gonet.Addr{IP: "0.0.0.0", Port: 8080}, Pid: 100},
		{Type: syscall.SOCK_STREAM, Status: "LISTEN", Laddr: gonet.Addr{IP: "127.0.0.1", Port: 8090}, Pid: 200},
		// Non-listening TCP (established outbound) must be excluded.
		{Type: syscall.SOCK_STREAM, Status: "ESTABLISHED", Laddr: gonet.Addr{IP: "10.0.0.5", Port: 54321}, Raddr: gonet.Addr{IP: "192.0.2.4", Port: 443}},
		// Bound UDP (no remote address) must be included.
		{Type: syscall.SOCK_DGRAM, Laddr: gonet.Addr{IP: "0.0.0.0", Port: 53}, Pid: 300},
		// UDP with a remote address set (a "connected" UDP socket) must
		// be excluded — it's not a service the host offers.
		{Type: syscall.SOCK_DGRAM, Laddr: gonet.Addr{IP: "10.0.0.5", Port: 45000}, Raddr: gonet.Addr{IP: "198.51.100.8", Port: 53}},
	}, nil)

	inv, err := c.Collect(context.Background(), time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(inv.Ports) != 3 {
		t.Fatalf("got %d ports, want 3: %+v", len(inv.Ports), inv.Ports)
	}

	sort.Slice(inv.Ports, func(i, j int) bool { return inv.Ports[i].Port < inv.Ports[j].Port })

	want := []models.ListeningPort{
		{Proto: "udp", IP: "0.0.0.0", Port: 53},
		{Proto: "tcp", IP: "0.0.0.0", Port: 8080},
		{Proto: "tcp", IP: "127.0.0.1", Port: 8090},
	}
	for i, w := range want {
		got := inv.Ports[i]
		if got.Proto != w.Proto || got.IP != w.IP || got.Port != w.Port {
			t.Errorf("port[%d] = %+v, want proto/ip/port %+v", i, got, w)
		}
	}
}

func TestInventoryCollector_PortCap(t *testing.T) {
	c := newTestInventoryCollector()
	var conns []gonet.ConnectionStat
	for i := 0; i < models.MaxInventoryPorts+50; i++ {
		conns = append(conns, gonet.ConnectionStat{
			Type:   syscall.SOCK_STREAM,
			Status: "LISTEN",
			Laddr:  gonet.Addr{IP: "127.0.0.1", Port: uint32(1024 + i)},
		})
	}
	c.connections = fakeConnections(conns, nil)

	inv, err := c.Collect(context.Background(), time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(inv.Ports) != models.MaxInventoryPorts {
		t.Fatalf("got %d ports, want cap %d", len(inv.Ports), models.MaxInventoryPorts)
	}
}

func TestInventoryCollector_ConnectionsErrorDegradesToEmpty(t *testing.T) {
	c := newTestInventoryCollector()
	c.connections = fakeConnections(nil, context.DeadlineExceeded)

	inv, err := c.Collect(context.Background(), time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("Collect: %v (connections failure must degrade, not error)", err)
	}
	if len(inv.Ports) != 0 {
		t.Fatalf("got %d ports, want 0 on connections error", len(inv.Ports))
	}
}

func TestInventoryCollector_ProcessNameResolved(t *testing.T) {
	c := newTestInventoryCollector()
	c.connections = fakeConnections([]gonet.ConnectionStat{
		{Type: syscall.SOCK_STREAM, Status: "LISTEN", Laddr: gonet.Addr{IP: "127.0.0.1", Port: 9000}, Pid: 42},
	}, nil)
	c.processName = func(pid int32) string {
		if pid == 42 {
			return "myservice"
		}
		return ""
	}

	inv, err := c.Collect(context.Background(), time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(inv.Ports) != 1 {
		t.Fatalf("got %d ports, want 1", len(inv.Ports))
	}
	if inv.Ports[0].Process != "myservice" {
		t.Errorf("Process = %q, want %q", inv.Ports[0].Process, "myservice")
	}
	if inv.Ports[0].PID != 42 {
		t.Errorf("PID = %d, want 42", inv.Ports[0].PID)
	}
}

func TestInventoryCollector_CollectedAtStamped(t *testing.T) {
	c := newTestInventoryCollector()
	now := time.Unix(123456, 0)
	inv, err := c.Collect(context.Background(), now)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if inv.CollectedAt != now.Unix() {
		t.Errorf("CollectedAt = %d, want %d", inv.CollectedAt, now.Unix())
	}
}

func TestInventoryCollector_ContextCanceled(t *testing.T) {
	c := newTestInventoryCollector()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Collect(ctx, time.Now()); err == nil {
		t.Fatal("expected error for already-canceled context")
	}
}

func TestInventoryCollector_DockerOffNeverDials(t *testing.T) {
	c := NewInventoryCollector(InventoryCollectorOptions{Docker: "off", Logger: slog.New(slog.DiscardHandler)})
	c.connections = fakeConnections(nil, nil)

	inv, err := c.Collect(context.Background(), time.Unix(1, 0))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if inv.Docker.Status != models.DockerStatusUnavailable {
		t.Errorf("Docker.Status = %q, want %q", inv.Docker.Status, models.DockerStatusUnavailable)
	}
}

func TestInventoryCollector_MatchContainerPorts(t *testing.T) {
	ports := []models.ListeningPort{
		{Proto: "tcp", IP: "0.0.0.0", Port: 8080},
		{Proto: "tcp", IP: "127.0.0.1", Port: 9999},
	}
	containers := []models.Container{
		{
			ID: "abc123def456",
			Ports: []models.ContainerPort{
				{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
			},
		},
	}
	matchContainerPorts(ports, containers)

	if ports[0].ContainerID != "abc123def456" {
		t.Errorf("ports[0].ContainerID = %q, want %q", ports[0].ContainerID, "abc123def456")
	}
	if ports[1].ContainerID != "" {
		t.Errorf("ports[1].ContainerID = %q, want empty (no published port match)", ports[1].ContainerID)
	}
}
