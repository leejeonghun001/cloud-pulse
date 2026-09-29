package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	gonet "github.com/shirou/gopsutil/v4/net"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// newFakeDockerServer starts an httptest-style server listening on a
// unix socket at sockPath (inside t.TempDir()), serving handler, and
// registers cleanup to close it. Docker Engine API tests use this
// instead of a real daemon — this host has no Docker installed, and
// even if it did, tests must not depend on it.
func newFakeDockerServer(t *testing.T, sockPath string, handler http.Handler) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not exercised on windows")
	}

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen on %s: %v", sockPath, err)
	}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: handler}} //nolint:gosec // test-only fixed timeouts are fine
	srv.Start()
	t.Cleanup(srv.Close)
}

// fakeDockerHandler serves the two Engine API endpoints
// dockerClient.collect calls, from canned responses.
func fakeDockerHandler(t *testing.T, version string, containers []dockerContainerJSON) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+dockerAPIVersion+"/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ApiVersion": version})
	})
	mux.HandleFunc("/"+dockerAPIVersion+"/containers/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(containers)
	})
	return mux
}

func TestDockerClient_Collect_OK(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "docker.sock")
	containers := []dockerContainerJSON{
		{
			ID:      "abcdef0123456789",
			Names:   []string{"/my-app"},
			Image:   "nginx:latest",
			State:   "running",
			Status:  "Up 3 days (healthy)",
			Created: 1700000000,
			Ports: []dockerPortJSON{
				{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
			},
			Labels: map[string]string{
				composeProjectLabel: "myproj",
				composeServiceLabel: "web",
			},
		},
	}
	newFakeDockerServer(t, sockPath, fakeDockerHandler(t, "1.41", containers))

	c := newDockerClient(sockPath)
	info := c.collect(context.Background())

	if info.Status != models.DockerStatusOK {
		t.Fatalf("Status = %q, want ok (Error: %s)", info.Status, info.Error)
	}
	if info.Version != "1.41" {
		t.Errorf("Version = %q, want 1.41", info.Version)
	}
	if len(info.Containers) != 1 {
		t.Fatalf("got %d containers, want 1", len(info.Containers))
	}
	ctr := info.Containers[0]
	if ctr.ID != "abcdef012345" {
		t.Errorf("ID = %q, want 12-char truncation %q", ctr.ID, "abcdef012345")
	}
	if ctr.Name != "my-app" {
		t.Errorf("Name = %q, want %q", ctr.Name, "my-app")
	}
	if ctr.Health != "healthy" {
		t.Errorf("Health = %q, want %q", ctr.Health, "healthy")
	}
	if ctr.ComposeProject != "myproj" || ctr.ComposeService != "web" {
		t.Errorf("compose project/service = %q/%q, want myproj/web", ctr.ComposeProject, ctr.ComposeService)
	}
	if len(ctr.Ports) != 1 || ctr.Ports[0].PublicPort != 8080 {
		t.Errorf("Ports = %+v, want one entry with PublicPort 8080", ctr.Ports)
	}
}

func TestDockerClient_Collect_SocketMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not exercised on windows")
	}
	sockPath := filepath.Join(t.TempDir(), "does-not-exist.sock")
	c := newDockerClient(sockPath)
	info := c.collect(context.Background())
	if info.Status != models.DockerStatusUnavailable {
		t.Errorf("Status = %q, want %q", info.Status, models.DockerStatusUnavailable)
	}
}

func TestDockerClient_Collect_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not exercised on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission bits; EACCES mapping cannot be exercised")
	}

	sockPath := filepath.Join(t.TempDir(), "docker.sock")
	newFakeDockerServer(t, sockPath, fakeDockerHandler(t, "1.41", nil))

	if err := os.Chmod(sockPath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sockPath, 0o666) })

	c := newDockerClient(sockPath)
	info := c.collect(context.Background())
	if info.Status != models.DockerStatusPermissionDenied {
		t.Fatalf("Status = %q, want %q (Error: %s)", info.Status, models.DockerStatusPermissionDenied, info.Error)
	}
	if info.Error == "" {
		t.Error("Error should name the --docker installer flag hint")
	}
}

func TestDockerClient_Collect_Off(t *testing.T) {
	c := newDockerClient("off")
	info := c.collect(context.Background())
	if info.Status != models.DockerStatusUnavailable {
		t.Errorf("Status = %q, want %q", info.Status, models.DockerStatusUnavailable)
	}
}

func TestDockerClient_Collect_UnsupportedTransport(t *testing.T) {
	c := newDockerClient("npipe:////./pipe/docker_engine")
	info := c.collect(context.Background())
	if info.Status != models.DockerStatusUnsupported {
		t.Errorf("Status = %q, want %q", info.Status, models.DockerStatusUnsupported)
	}
}

func TestDockerClient_Collect_UnixURLPrefix(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "docker.sock")
	newFakeDockerServer(t, sockPath, fakeDockerHandler(t, "1.41", nil))

	c := newDockerClient("unix://" + sockPath)
	info := c.collect(context.Background())
	if info.Status != models.DockerStatusOK {
		t.Fatalf("Status = %q, want ok (Error: %s)", info.Status, info.Error)
	}
}

func TestDockerClient_Collect_ContainerCap(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "docker.sock")
	var containers []dockerContainerJSON
	for i := 0; i < models.MaxInventoryContainers+20; i++ {
		containers = append(containers, dockerContainerJSON{ID: "id", Names: []string{"/c"}, State: "running"})
	}
	newFakeDockerServer(t, sockPath, fakeDockerHandler(t, "1.41", containers))

	c := newDockerClient(sockPath)
	info := c.collect(context.Background())
	if info.Status != models.DockerStatusOK {
		t.Fatalf("Status = %q, want ok (Error: %s)", info.Status, info.Error)
	}
	if len(info.Containers) != models.MaxInventoryContainers {
		t.Fatalf("got %d containers, want cap %d", len(info.Containers), models.MaxInventoryContainers)
	}
}

func TestDockerClient_Collect_ServerErrorStatus(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "docker.sock")
	mux := http.NewServeMux()
	mux.HandleFunc("/"+dockerAPIVersion+"/version", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})
	newFakeDockerServer(t, sockPath, mux)

	c := newDockerClient(sockPath)
	info := c.collect(context.Background())
	if info.Status != models.DockerStatusError {
		t.Errorf("Status = %q, want %q", info.Status, models.DockerStatusError)
	}
}

func TestParseHealthFromStatus(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"Up 3 days (healthy)", "healthy"},
		{"Up 2 minutes (unhealthy)", "unhealthy"},
		{"Up 5 seconds (health: starting)", ""},
		{"Up 5 seconds (starting)", "starting"},
		{"Exited (0) 2 hours ago", ""},
		{"Up 3 days", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := parseHealthFromStatus(tt.status); got != tt.want {
			t.Errorf("parseHealthFromStatus(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestContainerDisplayName(t *testing.T) {
	if got := containerDisplayName([]string{"/foo"}); got != "foo" {
		t.Errorf("got %q, want %q", got, "foo")
	}
	if got := containerDisplayName(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestInventoryCollector_DockerIntegration exercises the full
// InventoryCollector (not just dockerClient) against a fake Docker
// socket, ensuring the Docker-published-port -> ListeningPort match
// wiring end to end.
func TestInventoryCollector_DockerIntegration(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "docker.sock")
	containers := []dockerContainerJSON{
		{
			ID:     "abcdef0123456789",
			Names:  []string{"/web"},
			Image:  "nginx",
			State:  "running",
			Status: "Up 1 minute",
			Ports: []dockerPortJSON{
				{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
			},
		},
	}
	newFakeDockerServer(t, sockPath, fakeDockerHandler(t, "1.41", containers))

	c := NewInventoryCollector(InventoryCollectorOptions{Docker: sockPath})
	c.connections = fakeConnections([]gonet.ConnectionStat{
		{Type: syscall.SOCK_STREAM, Status: "LISTEN", Laddr: gonet.Addr{IP: "0.0.0.0", Port: 8080}},
	}, nil)
	c.processName = func(int32) string { return "" }

	inv, err := c.Collect(context.Background(), time.Unix(1, 0))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if inv.Docker.Status != models.DockerStatusOK {
		t.Fatalf("Docker.Status = %q, want ok (Error: %s)", inv.Docker.Status, inv.Docker.Error)
	}
	if len(inv.Ports) != 1 {
		t.Fatalf("got %d ports, want 1", len(inv.Ports))
	}
	if inv.Ports[0].ContainerID != "abcdef012345" {
		t.Errorf("ContainerID = %q, want %q", inv.Ports[0].ContainerID, "abcdef012345")
	}
}
