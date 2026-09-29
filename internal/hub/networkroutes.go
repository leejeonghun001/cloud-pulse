package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/config"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// SettingNetworkConfig is the settings table key for the hub-side
// network listen/allowlist configuration override (SPEC-v0.4 §2).
// Present only once an admin has confirmed a change from the dashboard;
// its absence means "derive from CP_LISTEN/CP_ALLOWED_CIDRS". Exported
// so cmd/hub's `reset-network` CLI subcommand can clear it directly.
const SettingNetworkConfig = "network_config"

// pendingConfirmWindow is how long a network change that would move the
// requesting client off its current address stays "pending" before it
// is automatically reverted.
const pendingConfirmWindow = 120 * time.Second

// maxNetworkBodyBytes bounds PUT /api/v1/settings/network request bodies.
const maxNetworkBodyBytes = 16 << 10 // 16 KiB

// minNetworkPort/maxNetworkPort bound the Port field accepted by PUT
// /api/v1/settings/network.
const (
	minNetworkPort = 1024
	maxNetworkPort = 65535
)

// afterFunc abstracts time.AfterFunc so tests can inject a fake timer
// instead of waiting on a real 120s deadline.
type afterFunc func(d time.Duration, f func()) timer

// timer is the subset of *time.Timer pendingNetworkState needs.
type timer interface {
	Stop() bool
}

// pendingNetworkState holds the in-progress network change awaiting
// confirmation, or nil when no change is pending. Guarded by
// networkMu.
type pendingNetworkState struct {
	previous       models.NetworkConfig
	previousSource string
	next           models.NetworkConfig
	deadline       time.Time
	urls           []string
	revertFn       timer
}

// networkAfterFunc returns the afterFunc used by production code:
// time.AfterFunc itself. Tests override Server.netAfterFunc directly.
func networkAfterFunc(d time.Duration, f func()) timer {
	return time.AfterFunc(d, f)
}

// networkMu, networkPending, netAfterFunc are declared as Server fields
// (see server.go) rather than here, since Go doesn't allow adding fields
// to a type from a second file in the same package without editing the
// struct itself — see the "networking fields" block added to Server.

// ResolveStartupNetworkConfig resolves the NetworkConfig cmd/hub should
// pass to its listen.Manager's initial Apply call: the persisted
// "network_config" setting if present, otherwise derived from
// s.opts.EnvListen/AllowedCIDRs (populated from config.Hub by cmd/hub
// before constructing Server). Exported for cmd/hub's startup wiring;
// internal callers use effectiveNetworkConfig directly.
func (s *Server) ResolveStartupNetworkConfig(ctx context.Context) (models.NetworkConfig, error) {
	cfg, _, err := s.effectiveNetworkConfig(ctx)
	return cfg, err
}

// registerNetworkRoutes registers the Network settings endpoints (GET
// /api/v1/settings/network, PUT /api/v1/settings/network, POST
// /api/v1/settings/network/confirm, POST /api/v1/settings/network/revert,
// DELETE /api/v1/settings/network — see SPEC-v0.4 §2) on mux.
func (s *Server) registerNetworkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/settings/network", s.requireAdmin(s.handleGetNetwork))
	mux.HandleFunc("PUT /api/v1/settings/network", s.requireAdmin(s.handlePutNetwork))
	mux.HandleFunc("POST /api/v1/settings/network/confirm", s.requireAdmin(s.handleConfirmNetwork))
	mux.HandleFunc("POST /api/v1/settings/network/revert", s.requireAdmin(s.handleRevertNetwork))
	mux.HandleFunc("DELETE /api/v1/settings/network", s.requireAdmin(s.handleDeleteNetwork))
}

// effectiveNetworkConfig resolves the current NetworkConfig: the
// persisted "network_config" setting if present ("hub" source),
// otherwise derived from CP_LISTEN/CP_ALLOWED_CIDRS ("env" source).
func (s *Server) effectiveNetworkConfig(ctx context.Context) (cfg models.NetworkConfig, source string, err error) {
	raw, ok, err := s.store.GetSetting(ctx, SettingNetworkConfig)
	if err != nil {
		return models.NetworkConfig{}, "", fmt.Errorf("hub: get network config setting: %w", err)
	}
	if ok && raw != "" {
		var stored models.NetworkConfig
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return models.NetworkConfig{}, "", fmt.Errorf("hub: parse stored network config: %w", err)
		}
		return stored, "hub", nil
	}

	envCfg, err := s.envNetworkConfig()
	if err != nil {
		return models.NetworkConfig{}, "", fmt.Errorf("hub: derive network config from env: %w", err)
	}
	return envCfg, "env", nil
}

// envNetworkConfig derives a NetworkConfig from s.opts (the
// CP_LISTEN/CP_ALLOWED_CIDRS-equivalent fields threaded through
// hub.Options), mirroring config.Hub.EnvNetworkConfig without requiring
// hub to depend on a config.Hub value directly (hub.Options already
// carries AllowedCIDRs; EnvListen carries the original CP_LISTEN
// string).
func (s *Server) envNetworkConfig() (models.NetworkConfig, error) {
	mode, addresses, port, err := config.ParseListen(s.opts.envListen())
	if err != nil {
		return models.NetworkConfig{}, err
	}
	return models.NetworkConfig{
		Mode:         mode,
		Addresses:    addresses,
		Port:         port,
		AllowedCIDRs: formatAllowedCIDRsForView(s.opts.AllowedCIDRs),
	}, nil
}

// buildNetworkState assembles the full GET /api/v1/settings/network
// response.
func (s *Server) buildNetworkState(ctx context.Context, r *http.Request) (models.NetworkState, error) {
	cfg, source, err := s.effectiveNetworkConfig(ctx)
	if err != nil {
		return models.NetworkState{}, err
	}

	var listeners []models.ListenerStatus
	if s.opts.Listener != nil {
		listeners = s.opts.Listener.Status()
	}

	ifaces, ifacesErr := listInterfaces(netInterfacesFunc)
	ifaceErrMsg := ""
	if ifacesErr != nil {
		ifaceErrMsg = ifacesErr.Error()
		ifaces = []models.NetInterface{}
	}

	state := models.NetworkState{
		Config:          cfg,
		Source:          source,
		EnvListen:       s.opts.envListen(),
		EnvAllowedCIDRs: formatAllowedCIDRsForView(s.opts.AllowedCIDRs),
		Listeners:       listeners,
		Client: models.ClientInfo{
			IP:        clientIP(r),
			LocalAddr: localAddrFromRequest(r),
		},
		Agents:          s.agentConns.list(),
		Interfaces:      ifaces,
		InterfacesError: ifaceErrMsg,
	}

	s.networkMu.Lock()
	if s.networkPending != nil {
		state.Pending = &models.PendingNetwork{
			Previous: s.networkPending.previous,
			Deadline: s.networkPending.deadline.Unix(),
			URLs:     s.networkPending.urls,
		}
	}
	s.networkMu.Unlock()

	return state, nil
}

// netInterfacesFunc is the production net.Interfaces call, indirected
// through a var so tests in this package could override it if ever
// needed; the pure classification logic itself is tested directly in
// interfaces_test.go without going through this path.
var netInterfacesFunc = defaultNetInterfaces

// clientIP extracts the request's remote IP (without port) from
// r.RemoteAddr only, never a client-supplied header.
func clientIP(r *http.Request) string {
	addrPort, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		addr, addrErr := netip.ParseAddr(r.RemoteAddr)
		if addrErr != nil {
			return r.RemoteAddr
		}
		return addr.String()
	}
	return addrPort.Addr().Unmap().String()
}

// handleGetNetwork responds with the full network settings read model:
// GET /api/v1/settings/network (admin).
func (s *Server) handleGetNetwork(w http.ResponseWriter, r *http.Request) {
	state, err := s.buildNetworkState(r.Context(), r)
	if err != nil {
		s.logger.Error("get network settings failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// putNetworkRequest is the body of PUT /api/v1/settings/network.
type putNetworkRequest struct {
	Mode         string   `json:"mode"`
	Addresses    []string `json:"addresses"`
	Port         int      `json:"port"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
}

// handlePutNetwork validates and applies a new network configuration:
// PUT /api/v1/settings/network (admin). See SPEC-v0.4 §2 for the full
// validation/lock-out/bind-failure/pending state machine.
func (s *Server) handlePutNetwork(w http.ResponseWriter, r *http.Request) { //nolint:gocyclo // the PUT handler intentionally implements the whole §2 state machine in one place per the spec; splitting it would scatter one atomic decision across many small functions with no reuse
	ctx := r.Context()

	s.networkMu.Lock()
	if s.networkPending != nil {
		s.networkMu.Unlock()
		writeJSON(w, http.StatusConflict, models.APIError{Error: "a network change is already pending confirmation", Code: "change_pending"})
		return
	}
	s.networkMu.Unlock()

	r.Body = http.MaxBytesReader(w, r.Body, maxNetworkBodyBytes)
	var req putNetworkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	newCfg, err := validateNetworkRequest(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: err.Error()})
		return
	}

	allowedPrefixes, err := config.ParseAllowedCIDRsList(newCfg.AllowedCIDRs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: err.Error()})
		return
	}

	clientAddr, clientOK := parseClientAddr(r)
	if clientOK && !cidrsContain(allowedPrefixes, clientAddr) {
		writeJSON(w, http.StatusConflict, models.APIError{
			Error: "the new access allowlist would not include your own address; refusing to apply it",
			Code:  "would_lock_out",
		})
		return
	}

	previous, previousSource, err := s.effectiveNetworkConfig(ctx)
	if err != nil {
		s.logger.Error("get current network config failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	desiredAddrs := config.FormatListen(newCfg.Mode, newCfg.Addresses, newCfg.Port)

	if s.opts.Listener == nil {
		// No real listener manager wired (e.g. some unit tests); persist
		// immediately without attempting to bind anything, mirroring the
		// "still served" branch below.
		s.applyNetworkConfigLocked(newCfg, allowedPrefixes)
		if err := s.persistNetworkConfig(ctx, newCfg); err != nil {
			s.logger.Error("persist network config failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
		state, err := s.buildNetworkState(ctx, r)
		if err != nil {
			s.logger.Error("build network state failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
		writeJSON(w, http.StatusOK, state)
		return
	}

	previousAddrs := config.FormatListen(previous.Mode, previous.Addresses, previous.Port)
	// Stage both configurations before deciding whether confirmation is
	// needed. Manager.Apply closes every address absent from its argument,
	// so applying only desiredAddrs here would silently drop the request's
	// current listener during the 120-second pending window.
	statuses, err := s.opts.Listener.Apply(ctx, unionListenAddrs(desiredAddrs, previousAddrs))
	if err != nil {
		s.logger.Error("apply listener config failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	desiredStatuses := listenerStatusesForAddrs(statuses, desiredAddrs)
	if !anyBindable(desiredStatuses) {
		// Roll back to the previous listener set. Existing listeners were
		// retained by the staging Apply, but this also removes failed new
		// entries from Manager's retry set.
		if _, rollbackErr := s.opts.Listener.Apply(ctx, previousAddrs); rollbackErr != nil {
			s.logger.Error("rollback listener config failed", "error", rollbackErr)
		}
		writeJSON(w, http.StatusConflict, models.APIError{
			Error: "no listener could be opened for the requested configuration: " + bindErrorSummary(desiredStatuses),
			Code:  "bind_failed",
		})
		return
	}

	if s.requestStillServed(r, desiredStatuses) {
		// This change does not need the safety window, so narrow to the new
		// configuration now. Manager keeps removed sockets alive briefly for
		// this response to flush.
		if _, err := s.opts.Listener.Apply(ctx, desiredAddrs); err != nil {
			s.logger.Error("finalize listener config failed", "error", err)
			if _, rollbackErr := s.opts.Listener.Apply(ctx, previousAddrs); rollbackErr != nil {
				s.logger.Error("rollback listener config failed", "error", rollbackErr)
			}
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
		s.applyNetworkConfigLocked(newCfg, allowedPrefixes)
		if err := s.persistNetworkConfig(ctx, newCfg); err != nil {
			s.logger.Error("persist network config failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
	} else {
		// Keep both old and new listeners for the entire pending window.
		// The old listener is deliberately removed only by confirm/revert.
		s.applyNetworkConfigLocked(newCfg, allowedPrefixes)
		s.startPending(ctx, previous, previousSource, desiredStatuses, newCfg, allowedPrefixes)
	}

	state, err := s.buildNetworkState(ctx, r)
	if err != nil {
		s.logger.Error("build network state failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// applyNetworkConfigLocked swaps the server's live CIDR allowlist to
// match cfg immediately (setAllowedCIDRs is itself atomic/lock-free; the
// "Locked" suffix reflects that this is called while reasoning about
// networkMu-guarded state in the caller, not that it takes networkMu
// itself).
func (s *Server) applyNetworkConfigLocked(_ models.NetworkConfig, allowedPrefixes []netip.Prefix) {
	s.setAllowedCIDRs(allowedPrefixes)
}

// persistNetworkConfig stores cfg as the "network_config" setting.
func (s *Server) persistNetworkConfig(ctx context.Context, cfg models.NetworkConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("hub: marshal network config: %w", err)
	}
	if err := s.store.SetSetting(ctx, SettingNetworkConfig, string(raw)); err != nil {
		return fmt.Errorf("hub: set network config setting: %w", err)
	}
	return nil
}

// startPending records a pending network change and schedules its
// auto-revert after pendingConfirmWindow.
func (s *Server) startPending(ctx context.Context, previous models.NetworkConfig, previousSource string, statuses []models.ListenerStatus, newCfg models.NetworkConfig, allowedPrefixes []netip.Prefix) {
	urls := candidateURLs(statuses)
	deadline := s.opts.now().Add(pendingConfirmWindow)

	s.networkMu.Lock()
	pending := &pendingNetworkState{
		previous:       previous,
		previousSource: previousSource,
		next:           newCfg,
		deadline:       deadline,
		urls:           urls,
	}
	s.networkPending = pending
	afterFn := s.netAfterFunc
	if afterFn == nil {
		afterFn = networkAfterFunc
	}
	pending.revertFn = afterFn(pendingConfirmWindow, func() {
		s.autoRevertPending(context.Background(), pending, previousSource)
	})
	s.networkMu.Unlock()

	s.logger.Warn("network config change pending confirmation; will auto-revert if not confirmed",
		"deadline", deadline.Format(time.RFC3339), "new_mode", newCfg.Mode, "new_port", newCfg.Port)
}

// autoRevertPending reverts a pending change that was never confirmed by
// its deadline, restoring the previous listener config and allowlist.
func (s *Server) autoRevertPending(ctx context.Context, pending *pendingNetworkState, previousSource string) {
	s.networkMu.Lock()
	if s.networkPending != pending {
		s.networkMu.Unlock()
		return // already confirmed or reverted
	}
	s.networkPending = nil
	s.networkMu.Unlock()

	s.logger.Warn("network config change not confirmed in time; auto-reverting")
	s.revertToLocked(ctx, pending.previous, previousSource)
}

// revertToLocked re-applies previous's listener addresses and allowlist,
// and either persists it (source "hub") or clears the override
// (source "env").
func (s *Server) revertToLocked(ctx context.Context, previous models.NetworkConfig, previousSource string) {
	if s.opts.Listener != nil {
		addrs := config.FormatListen(previous.Mode, previous.Addresses, previous.Port)
		if _, err := s.opts.Listener.Apply(ctx, addrs); err != nil {
			s.logger.Error("revert listener config failed", "error", err)
		}
	}
	prefixes, err := config.ParseAllowedCIDRsList(previous.AllowedCIDRs)
	if err != nil {
		s.logger.Error("revert: parse previous allowed_cidrs failed", "error", err)
	} else {
		s.setAllowedCIDRs(prefixes)
	}

	if previousSource == "hub" {
		if err := s.persistNetworkConfig(ctx, previous); err != nil {
			s.logger.Error("revert: persist previous network config failed", "error", err)
		}
	} else if err := s.store.SetSetting(ctx, SettingNetworkConfig, ""); err != nil {
		s.logger.Error("revert: clear network config setting failed", "error", err)
	}
}

// handleConfirmNetwork persists the pending change and removes the old
// listener set: POST /api/v1/settings/network/confirm (admin).
func (s *Server) handleConfirmNetwork(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	s.networkMu.Lock()
	pending := s.networkPending
	if pending == nil {
		s.networkMu.Unlock()
		writeJSON(w, http.StatusConflict, models.APIError{Error: "no network change is pending", Code: "no_pending"})
		return
	}
	pending.revertFn.Stop()
	s.networkPending = nil
	s.networkMu.Unlock()

	if s.opts.Listener != nil {
		addrs := config.FormatListen(pending.next.Mode, pending.next.Addresses, pending.next.Port)
		statuses, err := s.opts.Listener.Apply(ctx, addrs)
		if err != nil || !anyBindable(statuses) {
			// A confirmation must never convert the reversible pending state
			// into a broken configuration. Restore the confirmed previous
			// configuration if the final reconciliation cannot succeed.
			s.revertToLocked(ctx, pending.previous, pending.previousSource)
			if err != nil {
				s.logger.Error("finalize pending listener config failed", "error", err)
				writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			} else {
				writeJSON(w, http.StatusConflict, models.APIError{
					Error: "no listener could be kept for the requested configuration: " + bindErrorSummary(statuses),
					Code:  "bind_failed",
				})
			}
			return
		}
	}

	if err := s.persistNetworkConfig(ctx, pending.next); err != nil {
		s.logger.Error("persist confirmed network config failed", "error", err)
		s.revertToLocked(ctx, pending.previous, pending.previousSource)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	state, err := s.buildNetworkState(ctx, r)
	if err != nil {
		s.logger.Error("build network state failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// splitHostPort wraps net.SplitHostPort for local use in this file.
func splitHostPort(addr string) (host, port string, err error) {
	return net.SplitHostPort(addr)
}

// handleRevertNetwork reverts the pending change immediately: POST
// /api/v1/settings/network/revert (admin).
func (s *Server) handleRevertNetwork(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	s.networkMu.Lock()
	pending := s.networkPending
	if pending == nil {
		s.networkMu.Unlock()
		writeJSON(w, http.StatusConflict, models.APIError{Error: "no network change is pending", Code: "no_pending"})
		return
	}
	pending.revertFn.Stop()
	s.networkPending = nil
	s.networkMu.Unlock()

	_, previousSource, err := s.effectiveNetworkConfig(ctx)
	if err != nil {
		s.logger.Error("get network config source for revert failed", "error", err)
		previousSource = "env"
	}
	s.revertToLocked(ctx, pending.previous, previousSource)

	state, err := s.buildNetworkState(ctx, r)
	if err != nil {
		s.logger.Error("build network state failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleDeleteNetwork drops the hub-side network config override,
// falling back to CP_LISTEN/CP_ALLOWED_CIDRS: DELETE
// /api/v1/settings/network (admin). Applies the same lock-out/bind-
// failure safety checks as PUT.
func (s *Server) handleDeleteNetwork(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	s.networkMu.Lock()
	if s.networkPending != nil {
		s.networkMu.Unlock()
		writeJSON(w, http.StatusConflict, models.APIError{Error: "a network change is already pending confirmation", Code: "change_pending"})
		return
	}
	s.networkMu.Unlock()

	envCfg, err := s.envNetworkConfig()
	if err != nil {
		s.logger.Error("derive env network config failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	allowedPrefixes, err := config.ParseAllowedCIDRsList(envCfg.AllowedCIDRs)
	if err != nil {
		s.logger.Error("parse env allowed_cidrs failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	clientAddr, clientOK := parseClientAddr(r)
	if clientOK && !cidrsContain(allowedPrefixes, clientAddr) {
		writeJSON(w, http.StatusConflict, models.APIError{
			Error: "reverting to the env configuration would not include your own address; refusing to apply it",
			Code:  "would_lock_out",
		})
		return
	}

	if s.opts.Listener != nil {
		desiredAddrs := config.FormatListen(envCfg.Mode, envCfg.Addresses, envCfg.Port)
		statuses, err := s.opts.Listener.Apply(ctx, desiredAddrs)
		if err != nil {
			s.logger.Error("apply env listener config failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
		if !anyBindable(statuses) {
			writeJSON(w, http.StatusConflict, models.APIError{
				Error: "no listener could be opened for the env configuration: " + bindErrorSummary(statuses),
				Code:  "bind_failed",
			})
			return
		}
	}

	s.setAllowedCIDRs(allowedPrefixes)

	if err := s.store.SetSetting(ctx, SettingNetworkConfig, ""); err != nil {
		s.logger.Error("clear network config setting failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	state, err := s.buildNetworkState(ctx, r)
	if err != nil {
		s.logger.Error("build network state failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// validateNetworkRequest validates req per SPEC-v0.4 §2 and returns the
// normalized models.NetworkConfig it describes.
func validateNetworkRequest(req putNetworkRequest) (models.NetworkConfig, error) {
	switch req.Mode {
	case "all":
		req.Addresses = nil
	case "custom":
		if len(req.Addresses) == 0 {
			return models.NetworkConfig{}, fmt.Errorf("custom mode requires at least one address")
		}
		for _, a := range req.Addresses {
			addr, err := netip.ParseAddr(a)
			if err != nil {
				return models.NetworkConfig{}, fmt.Errorf("invalid address %q: %w", a, err)
			}
			if addr.IsUnspecified() || addr.IsMulticast() {
				return models.NetworkConfig{}, fmt.Errorf("address %q must not be unspecified or multicast", a)
			}
		}
	default:
		return models.NetworkConfig{}, fmt.Errorf("mode must be %q or %q", "all", "custom")
	}

	if req.Port < minNetworkPort || req.Port > maxNetworkPort {
		return models.NetworkConfig{}, fmt.Errorf("port must be between %d and %d", minNetworkPort, maxNetworkPort)
	}

	if len(req.AllowedCIDRs) == 0 {
		return models.NetworkConfig{}, fmt.Errorf("allowed_cidrs must not be empty")
	}
	starCount := 0
	for _, c := range req.AllowedCIDRs {
		if c == "*" {
			starCount++
		}
	}
	if starCount > 0 && len(req.AllowedCIDRs) > 1 {
		return models.NetworkConfig{}, fmt.Errorf(`"*" must be the only entry in allowed_cidrs when present`)
	}
	if starCount == 0 {
		if _, err := config.ParseAllowedCIDRsList(req.AllowedCIDRs); err != nil {
			return models.NetworkConfig{}, err
		}
	}

	return models.NetworkConfig{
		Mode:         req.Mode,
		Addresses:    req.Addresses,
		Port:         req.Port,
		AllowedCIDRs: req.AllowedCIDRs,
	}, nil
}

// unionListenAddrs joins address sets without duplicates. It is used while a
// configuration is pending so the listener manager continues to own both the
// previous and candidate listener sets.
func unionListenAddrs(first, second []string) []string {
	seen := make(map[string]bool, len(first)+len(second))
	out := make([]string, 0, len(first)+len(second))
	for _, addrs := range [][]string{first, second} {
		for _, addr := range addrs {
			if seen[addr] {
				continue
			}
			seen[addr] = true
			out = append(out, addr)
		}
	}
	return out
}

// listenerStatusesForAddrs returns statuses belonging to requested addrs.
// Manager reports the actual bound address, but network settings only accepts
// non-zero ports, so a requested address and its successful bound address are
// identical.
func listenerStatusesForAddrs(statuses []models.ListenerStatus, requested []string) []models.ListenerStatus {
	wanted := make(map[string]bool, len(requested))
	for _, addr := range requested {
		wanted[addr] = true
	}
	out := make([]models.ListenerStatus, 0, len(requested))
	for _, status := range statuses {
		if wanted[status.Addr] {
			out = append(out, status)
		}
	}
	return out
}

// anyBindable reports whether at least one status is "listening" or
// "waiting" (both count as a viable outcome per SPEC-v0.4 §2 — only
// "error" is a hard failure disqualifying the whole set).
func anyBindable(statuses []models.ListenerStatus) bool {
	for _, st := range statuses {
		if st.Status == "listening" || st.Status == "waiting" {
			return true
		}
	}
	return false
}

// bindErrorSummary renders a compact "addr: error; addr: error" string
// covering every failed listener, for the bind_failed error message.
func bindErrorSummary(statuses []models.ListenerStatus) string {
	parts := make([]string, 0, len(statuses))
	for _, st := range statuses {
		if st.Error != "" {
			parts = append(parts, fmt.Sprintf("%s: %s", st.Addr, st.Error))
		}
	}
	return strings.Join(parts, "; ")
}

// candidateURLs builds one "http://<ip>:<port>" URL (bracketed for
// IPv6) per listening status, for PendingNetwork.URLs.
func candidateURLs(statuses []models.ListenerStatus) []string {
	out := make([]string, 0, len(statuses))
	for _, st := range statuses {
		if st.Status != "listening" {
			continue
		}
		host, port, err := splitHostPort(st.Addr)
		if err != nil {
			continue
		}
		if host == "" || host == "::" || host == "0.0.0.0" {
			continue // "all" listeners don't produce a specific candidate URL
		}
		addr, parseErr := netip.ParseAddr(host)
		if parseErr == nil && addr.Is6() {
			out = append(out, fmt.Sprintf("http://[%s]:%s", host, port))
		} else {
			out = append(out, fmt.Sprintf("http://%s:%s", host, port))
		}
	}
	sort.Strings(out)
	return out
}

// requestStillServed reports whether r's own connection would still be
// served by the new listener set: true if any listening status's host
// part is empty/unspecified (an "all" listener always still serves every
// existing client) or matches the local address the request actually
// arrived on.
func (s *Server) requestStillServed(r *http.Request, statuses []models.ListenerStatus) bool {
	localAddr := localAddrFromRequest(r)
	localHost, _, err := splitHostPort(localAddr)
	if err != nil {
		localHost = localAddr
	}
	localIP, parseErr := netip.ParseAddr(localHost)

	for _, st := range statuses {
		if st.Status != "listening" {
			continue
		}
		host, _, err := splitHostPort(st.Addr)
		if err != nil {
			continue
		}
		if host == "" || host == "::" || host == "0.0.0.0" {
			return true
		}
		if parseErr == nil {
			if listenerIP, err := netip.ParseAddr(host); err == nil && listenerIP.Unmap() == localIP.Unmap() {
				return true
			}
		}
	}
	return false
}

// parseClientAddr parses r.RemoteAddr into a netip.Addr, reporting
// ok=false if it can't be parsed (should not occur for a real HTTP
// connection).
func parseClientAddr(r *http.Request) (netip.Addr, bool) {
	addrPort, err := netip.ParseAddrPort(r.RemoteAddr)
	if err == nil {
		return addrPort.Addr().Unmap(), true
	}
	addr, err := netip.ParseAddr(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// cidrsContain reports whether addr is covered by any prefix in
// prefixes, or prefixes is nil (meaning allow all).
func cidrsContain(prefixes []netip.Prefix, addr netip.Addr) bool {
	if prefixes == nil {
		return true
	}
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// networkRoutesMu, networkRoutesPending fields, netAfterFunc: declared
// on Server in server.go (networkMu, networkPending, netAfterFunc) —
// see that file for the field docs.
