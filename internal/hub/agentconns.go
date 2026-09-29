package hub

import (
	"net/http"
	"sort"
	"sync"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// agentConnTracker records each agent's most recently observed local
// address (the side of the TCP connection the agent reached the hub on),
// keyed by hostID, for display on the Network settings page: an address
// change may disconnect agents that reach the hub over an address about
// to be removed. Safe for concurrent use.
type agentConnTracker struct {
	mu   sync.Mutex
	byID map[string]models.AgentConnection
}

func newAgentConnTracker() *agentConnTracker {
	return &agentConnTracker{byID: make(map[string]models.AgentConnection)}
}

// record stores/updates hostID's latest connection info.
func (t *agentConnTracker) record(hostID, hostname, localAddr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byID[hostID] = models.AgentConnection{
		HostID:    hostID,
		Hostname:  hostname,
		LocalAddr: localAddr,
	}
}

// list returns every tracked agent connection, sorted by hostname then
// hostID, never nil (empty input yields an empty, non-nil slice).
func (t *agentConnTracker) list() []models.AgentConnection {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]models.AgentConnection, 0, len(t.byID))
	for _, c := range t.byID {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].HostID < out[j].HostID
	})
	return out
}

// localAddrFromRequest extracts the local (server-side) address a
// connection was accepted on, set by net/http via
// http.LocalAddrContextKey. Returns "" if unavailable (e.g. in a unit
// test built with httptest.NewRequest, which never populates it).
func localAddrFromRequest(r *http.Request) string {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(interface{ String() string })
	if !ok || addr == nil {
		return ""
	}
	return addr.String()
}

// recordAgentConn records hostID/hostname's most recent local address
// (the side of the connection the agent reached the hub on), for
// display on the Network settings page. Called from handleIngest after
// a successful report.
func (s *Server) recordAgentConn(hostID, hostname string, r *http.Request) {
	localAddr := localAddrFromRequest(r)
	if localAddr == "" {
		return
	}
	s.agentConns.record(hostID, hostname, localAddr)
}
