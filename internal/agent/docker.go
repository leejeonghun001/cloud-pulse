package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// dockerAPIVersion is the Docker Engine API version path segment this
// client speaks, matching SPEC-v0.5 §C's "v1.41+" requirement. Podman's
// Docker-compatible socket also implements this API surface.
const dockerAPIVersion = "v1.41"

// dockerRequestTimeout bounds each individual request the client makes
// against the daemon's unix socket (well below
// inventoryCollectTimeout, which wraps two such requests).
const dockerRequestTimeout = 5 * time.Second

// defaultDockerSocket is the socket path CP_DOCKER=auto probes on
// Linux/macOS/BSD.
const defaultDockerSocket = "/var/run/docker.sock"

// composeProjectLabel and composeService Label are the well-known
// Docker Compose labels used to populate Container.ComposeProject/
// ComposeService.
const (
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
)

// dockerClient collects DockerInfo from a local Docker (or
// Podman-compatible) Engine API over a unix socket, using plain
// net/http with a custom DialContext — no third-party Docker SDK.
type dockerClient struct {
	// mode is "off", "auto", or an explicit socket path/URL, per
	// InventoryCollectorOptions.Docker.
	mode string
	// httpClient dials sockPath for every request; nil when mode ==
	// "off" or the platform doesn't support the configured transport
	// (e.g. an npipe:// value on windows, unsupported for now).
	httpClient *http.Client
	// sockPath is the resolved unix socket path used only for the
	// permission_denied stat check in collect(); "" when mode == "off"
	// or unsupported.
	sockPath string
	// unsupported is true when mode names a transport this client
	// cannot use (e.g. windows npipe), reported as
	// DockerStatusUnsupported without ever dialing anything.
	unsupported bool
}

// newDockerClient resolves mode (CP_DOCKER's value: "off" | "auto" | an
// explicit socket path/URL) into a dockerClient. mode == "off" produces
// a client whose collect always returns DockerStatusUnavailable with no
// network access attempted at all — this is the default-safe agent
// behavior when Docker monitoring isn't wanted.
func newDockerClient(mode string) *dockerClient {
	c := &dockerClient{mode: mode}
	if mode == "off" {
		return c
	}

	sockPath := mode
	if mode == "auto" || mode == "" {
		sockPath = defaultDockerSocket
	}

	// Accept a bare path or a "unix://" URL; anything else (e.g.
	// "npipe://...", "tcp://...") is not implemented by this client.
	sockPath = strings.TrimPrefix(sockPath, "unix://")
	if strings.Contains(sockPath, "://") {
		c.unsupported = true
		return c
	}

	c.sockPath = sockPath
	c.httpClient = &http.Client{
		Timeout: dockerRequestTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{}
				return d.DialContext(ctx, "unix", sockPath)
			},
		},
	}
	return c
}

// collect performs one Docker Engine API collection attempt, degrading
// to a non-"ok" DockerInfo.Status on every failure mode rather than
// returning an error (there is no error return at all — every caller
// treats this as a best-effort snapshot).
func (c *dockerClient) collect(ctx context.Context) models.DockerInfo {
	if c.mode == "off" {
		return models.DockerInfo{Status: models.DockerStatusUnavailable, Error: "Docker collection disabled (CP_DOCKER=off)", Containers: []models.Container{}}
	}
	if c.unsupported {
		return models.DockerInfo{Status: models.DockerStatusUnsupported, Error: "CP_DOCKER transport not supported by this agent build", Containers: []models.Container{}}
	}

	if _, err := os.Stat(c.sockPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return models.DockerInfo{Status: models.DockerStatusUnavailable, Error: "Docker socket not found: " + c.sockPath, Containers: []models.Container{}}
		}
		if errors.Is(err, os.ErrPermission) {
			return models.DockerInfo{Status: models.DockerStatusPermissionDenied, Error: dockerPermissionHint(c.sockPath), Containers: []models.Container{}}
		}
		return models.DockerInfo{Status: models.DockerStatusError, Error: err.Error(), Containers: []models.Container{}}
	}

	version, err := c.apiVersion(ctx)
	if err != nil {
		return dockerInfoFromError(err)
	}

	containers, err := c.listContainers(ctx)
	if err != nil {
		return dockerInfoFromError(err)
	}

	return models.DockerInfo{Status: models.DockerStatusOK, Version: version, Containers: containers}
}

// dockerPermissionHint builds DockerInfo.Error for the EACCES case,
// naming the installer flag that fixes it (SPEC-v0.5 §C).
func dockerPermissionHint(sockPath string) string {
	return fmt.Sprintf("permission denied opening %s; re-run install-agent.sh with --docker to add this agent to the docker group (root-equivalent access)", sockPath)
}

// dockerInfoFromError maps a request-level error (connection refused,
// EACCES surfaced during Dial rather than Stat, timeout, non-2xx
// status) to the closest DockerStatus.
func dockerInfoFromError(err error) models.DockerInfo {
	if errors.Is(err, os.ErrPermission) {
		return models.DockerInfo{Status: models.DockerStatusPermissionDenied, Error: err.Error(), Containers: []models.Container{}}
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && errors.Is(opErr.Err, os.ErrPermission) {
		return models.DockerInfo{Status: models.DockerStatusPermissionDenied, Error: err.Error(), Containers: []models.Container{}}
	}
	if errors.Is(err, os.ErrNotExist) {
		return models.DockerInfo{Status: models.DockerStatusUnavailable, Error: err.Error(), Containers: []models.Container{}}
	}
	return models.DockerInfo{Status: models.DockerStatusError, Error: err.Error(), Containers: []models.Container{}}
}

// apiVersion calls GET /version and returns the daemon's reported API
// version.
func (c *dockerClient) apiVersion(ctx context.Context) (string, error) {
	var resp struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := c.getJSON(ctx, "/version", &resp); err != nil {
		return "", err
	}
	return resp.APIVersion, nil
}

// dockerContainerJSON mirrors the fields this client reads from GET
// /containers/json?all=1; only a subset of the full Docker response
// schema.
type dockerContainerJSON struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Ports   []dockerPortJSON  `json:"Ports"`
	Labels  map[string]string `json:"Labels"`
}

// dockerPortJSON mirrors one entry of a container's "Ports" array.
type dockerPortJSON struct {
	IP          string `json:"IP,omitempty"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort,omitempty"`
	Type        string `json:"Type"`
}

// listContainers calls GET /containers/json?all=1 and converts the
// response into models.Container, capped at
// models.MaxInventoryContainers.
func (c *dockerClient) listContainers(ctx context.Context) ([]models.Container, error) {
	var raw []dockerContainerJSON
	if err := c.getJSON(ctx, "/containers/json?all=1", &raw); err != nil {
		return nil, err
	}

	out := make([]models.Container, 0, len(raw))
	for _, rc := range raw {
		if len(out) >= models.MaxInventoryContainers {
			break
		}
		out = append(out, convertContainer(rc))
	}
	return out, nil
}

// convertContainer maps one dockerContainerJSON into a models.Container.
func convertContainer(rc dockerContainerJSON) models.Container {
	ports := make([]models.ContainerPort, 0, len(rc.Ports))
	for _, p := range rc.Ports {
		ports = append(ports, models.ContainerPort{
			IP:          p.IP,
			PrivatePort: p.PrivatePort,
			PublicPort:  p.PublicPort,
			Type:        p.Type,
		})
	}

	id := rc.ID
	if len(id) > 12 {
		id = id[:12]
	}

	return models.Container{
		ID:             id,
		Name:           containerDisplayName(rc.Names),
		Image:          rc.Image,
		State:          rc.State,
		Status:         rc.Status,
		Health:         parseHealthFromStatus(rc.Status),
		CreatedAt:      rc.Created,
		ComposeProject: rc.Labels[composeProjectLabel],
		ComposeService: rc.Labels[composeServiceLabel],
		Ports:          ports,
	}
}

// containerDisplayName returns the first name in names with its
// leading '/' stripped (Docker always prefixes container names with
// '/' in this API), or "" if names is empty.
func containerDisplayName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// parseHealthFromStatus extracts a health suffix like "(healthy)" or
// "(unhealthy)" from Docker's human-readable Status text (e.g. "Up 3
// days (healthy)"), returning "" when Status carries no such suffix
// (the container has no configured healthcheck).
func parseHealthFromStatus(status string) string {
	open := strings.LastIndexByte(status, '(')
	close := strings.LastIndexByte(status, ')')
	if open < 0 || close <= open {
		return ""
	}
	inner := status[open+1 : close]
	switch inner {
	case "healthy", "unhealthy", "starting":
		return inner
	default:
		return ""
	}
}

// getJSON performs a GET request against path (relative to the
// versioned API root) over the client's unix-socket transport and
// decodes a 2xx JSON response into out. Non-2xx responses produce an
// error including the status code and a truncated response body.
func (c *dockerClient) getJSON(ctx context.Context, path string, out any) error {
	url := "http://unix/" + dockerAPIVersion + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("agent: docker: build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent: docker: request %s: %w", path, err)
	}
	defer func() {
		_ = resp.Body.Close() // body fully drained below; close error not actionable
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap: a very large container list is still well under this
	if err != nil {
		return fmt.Errorf("agent: docker: read response %s: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(body)
		if len(snippet) > 256 {
			snippet = snippet[:256]
		}
		return fmt.Errorf("agent: docker: %s returned status %s: %s", path, strconv.Itoa(resp.StatusCode), snippet)
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("agent: docker: decode response %s: %w", path, err)
	}
	return nil
}
