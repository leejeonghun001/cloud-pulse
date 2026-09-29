package hub

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/listen"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// fakeListenController is a hub.ListenController test double. It
// simulates binding: an address in failAddrs reports "error", otherwise
// "listening". It never actually opens a socket.
type fakeListenController struct {
	mu        chan struct{} // simple mutex via buffered channel to avoid importing sync in every test file
	failAddrs map[string]bool
	statuses  []models.ListenerStatus
	applyErr  error
	applyLog  [][]string
}

func newFakeListenController(failAddrs ...string) *fakeListenController {
	fail := make(map[string]bool, len(failAddrs))
	for _, a := range failAddrs {
		fail[a] = true
	}
	return &fakeListenController{mu: make(chan struct{}, 1), failAddrs: fail}
}

func (f *fakeListenController) lock()   { f.mu <- struct{}{} }
func (f *fakeListenController) unlock() { <-f.mu }

func (f *fakeListenController) Apply(_ context.Context, addrs []string) ([]models.ListenerStatus, error) {
	f.lock()
	defer f.unlock()
	f.applyLog = append(f.applyLog, addrs)
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	out := make([]models.ListenerStatus, 0, len(addrs))
	for _, a := range addrs {
		status := "listening"
		errMsg := ""
		if f.failAddrs[a] {
			status = "error"
			errMsg = "bind: address in use (fake)"
		}
		out = append(out, models.ListenerStatus{Addr: a, Status: status, Error: errMsg, Since: time.Now().Unix()})
	}
	f.statuses = out
	return out, nil
}

func (f *fakeListenController) Status() []models.ListenerStatus {
	f.lock()
	defer f.unlock()
	return f.statuses
}

// fakeTimer is a hub.timer test double letting tests fire the
// auto-revert callback deterministically instead of waiting on a real
// 120s deadline.
type fakeTimer struct {
	stopped bool
}

func (t *fakeTimer) Stop() bool {
	wasRunning := !t.stopped
	t.stopped = true
	return wasRunning
}

// fakeAfterFunc returns an afterFunc that records the scheduled callback
// (via the returned recorder) instead of using a real timer, so tests
// can invoke it synchronously to simulate the deadline firing.
type afterFuncRecorder struct {
	fn    func()
	timer *fakeTimer
}

func newFakeAfterFunc(rec *afterFuncRecorder) afterFunc {
	return func(_ time.Duration, f func()) timer {
		rec.fn = f
		rec.timer = &fakeTimer{}
		return rec.timer
	}
}

func networkTestOptions(listener ListenController) Options {
	opts := testOptions()
	opts.UIToken = "test-ui-token-1234"
	opts.Listener = listener
	opts.EnvListen = ":8090"
	return opts
}

const networkAdminToken = "test-ui-token-1234"

func TestGetNetwork_EnvSource(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.NetworkState](t, rec.Body)
	if got.Source != "env" {
		t.Errorf("Source = %q, want env", got.Source)
	}
	if got.Config.Mode != "all" || got.Config.Port != 8090 {
		t.Errorf("Config = %+v, want Mode=all Port=8090", got.Config)
	}
	if got.EnvListen != ":8090" {
		t.Errorf("EnvListen = %q, want :8090", got.EnvListen)
	}
	if got.Agents == nil {
		t.Error("Agents should encode as [] not null")
	}
	if got.Interfaces == nil {
		t.Error("Interfaces should encode as [] not null")
	}
}

func TestGetNetwork_RequiresAdmin(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/settings/network", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPutNetwork_AppliesAndPersistsWhenStillServed(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	body := []byte(`{"mode":"all","port":8090,"allowed_cidrs":["*"]}`)
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.NetworkState](t, rec.Body)
	if got.Pending != nil {
		t.Errorf("Pending = %+v, want nil (mode all always still serves every client)", got.Pending)
	}

	value, ok, err := store.GetSetting(context.Background(), SettingNetworkConfig)
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if !ok || value == "" {
		t.Fatal("expected network_config setting to be persisted")
	}
}

func TestPutNetwork_LockOut409(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	// Requesting client is 203.0.113.1; allowlist only covers 10.0.0.0/8.
	body := []byte(`{"mode":"all","port":8090,"allowed_cidrs":["10.0.0.0/8"]}`)
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "would_lock_out" {
		t.Errorf("Code = %q, want would_lock_out", got.Code)
	}
}

func TestPutNetwork_BindFailedRollback(t *testing.T) {
	t.Parallel()
	// Every custom address fails to bind.
	listener := newFakeListenController("192.168.1.5:8090")
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	body := []byte(`{"mode":"custom","addresses":["192.168.1.5"],"port":8090,"allowed_cidrs":["*"]}`)
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "bind_failed" {
		t.Errorf("Code = %q, want bind_failed", got.Code)
	}

	// The rollback Apply call should have been made with the previous
	// (env-derived) config's addresses.
	if len(listener.applyLog) < 2 {
		t.Fatalf("applyLog = %v, want at least 2 calls (attempt + rollback)", listener.applyLog)
	}
	rollback := listener.applyLog[len(listener.applyLog)-1]
	if len(rollback) != 1 || rollback[0] != "[::]:8090" {
		t.Errorf("rollback addrs = %v, want [[::]:8090]", rollback)
	}

	// No setting should have been persisted.
	_, ok, err := store.GetSetting(context.Background(), SettingNetworkConfig)
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if ok {
		t.Error("expected no network_config setting to be persisted after bind_failed")
	}
}

// TestPutNetwork_PendingRetainsOldListenerUntilConfirm exercises the pending
// safety window against listen.Manager's real close-on-removal behavior. A
// simple fake cannot catch this: the old address must remain reachable longer
// than Manager's one-second removed-listener response-flush delay.
func TestPutNetwork_PendingRetainsOldListenerUntilConfirm(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("127.0.0.2 loopback alias binding is Linux-specific")
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	oldAddr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	_, port, err := net.SplitHostPort(oldAddr)
	if err != nil {
		t.Fatalf("split old listener address: %v", err)
	}
	newAddr := net.JoinHostPort("127.0.0.2", port)

	httpServer := &http.Server{}
	manager := listen.NewManager(httpServer, testLogger())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown listener manager: %v", err)
		}
	})

	opts := networkTestOptions(manager)
	opts.EnvListen = oldAddr
	store := newFakeStore()
	s := newTestServer(t, opts, store)
	httpServer.Handler = s.Handler()
	if _, err := manager.Apply(context.Background(), []string{oldAddr}); err != nil {
		t.Fatalf("apply initial listener: %v", err)
	}

	timer := &afterFuncRecorder{}
	s.netAfterFunc = newFakeAfterFunc(timer)
	body := fmt.Sprintf(`{"mode":"custom","addresses":["127.0.0.2"],"port":%s,"allowed_cidrs":["*"]}`, port)
	req, err := http.NewRequest(http.MethodPut, "http://"+oldAddr+"/api/v1/settings/network", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("new pending-change request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+networkAdminToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("put pending network change: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close() // response cannot be reused after a failed status assertion
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close PUT response: %v", err)
	}

	// The old implementation dropped this listener one second after the
	// PUT. Wait past that grace delay, then verify the old-origin admin can
	// still reach the API during the whole confirmation window.
	time.Sleep(1200 * time.Millisecond)
	oldResp, err := client.Get("http://" + oldAddr + "/healthz")
	if err != nil {
		t.Fatalf("old listener was not retained during pending window: %v", err)
	}
	defer func() {
		_ = oldResp.Body.Close() // response body is only used for its status code
	}()
	if oldResp.StatusCode != http.StatusOK {
		t.Fatalf("old listener health status = %d, want 200", oldResp.StatusCode)
	}

	statuses := manager.Status()
	if len(statuses) != 2 {
		t.Fatalf("listener statuses = %+v, want old and new listeners", statuses)
	}
	for _, addr := range []string{oldAddr, newAddr} {
		found := false
		for _, status := range statuses {
			if status.Addr == addr && status.Status == "listening" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("listener status missing listening %s: %+v", addr, statuses)
		}
	}
	if timer.timer == nil || timer.timer.stopped {
		t.Error("pending change should retain its auto-revert timer")
	}

	// The old origin can now perform the confirmation itself. Confirmation
	// narrows the manager back to the intended new listener set.
	confirmReq, err := http.NewRequest(http.MethodPost, "http://"+oldAddr+"/api/v1/settings/network/confirm", nil)
	if err != nil {
		t.Fatalf("new confirmation request: %v", err)
	}
	confirmReq.Header.Set("Authorization", "Bearer "+networkAdminToken)
	confirmResp, err := client.Do(confirmReq)
	if err != nil {
		t.Fatalf("confirm from old listener: %v", err)
	}
	if confirmResp.StatusCode != http.StatusOK {
		_ = confirmResp.Body.Close() // response cannot be reused after a failed status assertion
		t.Fatalf("confirm status = %d, want 200", confirmResp.StatusCode)
	}
	if err := confirmResp.Body.Close(); err != nil {
		t.Fatalf("close confirmation response: %v", err)
	}
	statuses = manager.Status()
	if len(statuses) != 1 || statuses[0].Addr != newAddr || statuses[0].Status != "listening" {
		t.Errorf("listener statuses after confirmation = %+v, want only listening %s", statuses, newAddr)
	}
	if !timer.timer.stopped {
		t.Error("confirmation should stop the auto-revert timer")
	}
}

func TestPutNetwork_InvalidModeRejected(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()

	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	body := []byte(`{"mode":"bogus","port":8090,"allowed_cidrs":["*"]}`)
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestPutNetwork_InvalidPortRejected(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	for _, port := range []int{80, 65536, -1, 1023} {
		body := []byte(`{"mode":"all","port":` + itoa(port) + `,"allowed_cidrs":["*"]}`)
		rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("port %d: status = %d, want 400", port, rec.Code)
		}
	}
}

func TestPutNetwork_CustomModeRequiresAddresses(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	body := []byte(`{"mode":"custom","addresses":[],"port":8090,"allowed_cidrs":["*"]}`)
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestPutNetwork_UnspecifiedOrMulticastAddressRejected(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	for _, addr := range []string{"0.0.0.0", "224.0.0.1"} {
		body := []byte(`{"mode":"custom","addresses":["` + addr + `"],"port":8090,"allowed_cidrs":["*"]}`)
		rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("addr %s: status = %d, want 400", addr, rec.Code)
		}
	}
}

func TestPutNetwork_ChangePendingRejectsSecondPUT(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	rec := &afterFuncRecorder{}
	s.netAfterFunc = newFakeAfterFunc(rec)

	// First PUT: client is on 203.0.113.1 but the new config listens
	// only on 192.168.1.5, so the client would not still be served ⇒
	// pending.
	body := []byte(`{"mode":"custom","addresses":["192.168.1.5"],"port":8090,"allowed_cidrs":["*"]}`)
	first := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first PUT status = %d, want 200 (body=%s)", first.Code, first.Body.String())
	}
	firstState := decodeJSON[models.NetworkState](t, first.Body)
	if firstState.Pending == nil {
		t.Fatal("expected Pending to be set after first PUT")
	}

	second := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if second.Code != http.StatusConflict {
		t.Fatalf("second PUT status = %d, want 409 (body=%s)", second.Code, second.Body.String())
	}
	got := decodeJSON[models.APIError](t, second.Body)
	if got.Code != "change_pending" {
		t.Errorf("Code = %q, want change_pending", got.Code)
	}
}

func TestPutNetwork_PendingThenConfirmPersists(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	rec := &afterFuncRecorder{}
	s.netAfterFunc = newFakeAfterFunc(rec)

	body := []byte(`{"mode":"custom","addresses":["192.168.1.5"],"port":8090,"allowed_cidrs":["*"]}`)
	put := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body=%s)", put.Code, put.Body.String())
	}

	confirm := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/settings/network/confirm", "203.0.113.1:1234", networkAdminToken, nil)
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, want 200 (body=%s)", confirm.Code, confirm.Body.String())
	}
	confirmedState := decodeJSON[models.NetworkState](t, confirm.Body)
	if confirmedState.Pending != nil {
		t.Errorf("Pending = %+v, want nil after confirm", confirmedState.Pending)
	}

	value, ok, err := store.GetSetting(context.Background(), SettingNetworkConfig)
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if !ok || value == "" {
		t.Fatal("expected network_config setting to be persisted after confirm")
	}

	// The fake timer should have been stopped by confirm.
	if rec.timer == nil || !rec.timer.stopped {
		t.Error("expected the auto-revert timer to be stopped by confirm")
	}
}

func TestPutNetwork_ConfirmWithNoPending409(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/settings/network/confirm", "203.0.113.1:1234", networkAdminToken, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "no_pending" {
		t.Errorf("Code = %q, want no_pending", got.Code)
	}
}

func TestPutNetwork_RevertNow(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	rec := &afterFuncRecorder{}
	s.netAfterFunc = newFakeAfterFunc(rec)

	body := []byte(`{"mode":"custom","addresses":["192.168.1.5"],"port":8090,"allowed_cidrs":["*"]}`)
	put := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body=%s)", put.Code, put.Body.String())
	}

	revert := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/settings/network/revert", "203.0.113.1:1234", networkAdminToken, nil)
	if revert.Code != http.StatusOK {
		t.Fatalf("revert status = %d, want 200 (body=%s)", revert.Code, revert.Body.String())
	}
	revertedState := decodeJSON[models.NetworkState](t, revert.Body)
	if revertedState.Pending != nil {
		t.Errorf("Pending = %+v, want nil after revert", revertedState.Pending)
	}
	if revertedState.Config.Mode != "all" {
		t.Errorf("Config.Mode = %q, want all (reverted to env default)", revertedState.Config.Mode)
	}

	// No setting should have been persisted (env source, so revert
	// clears rather than writes).
	_, ok, err := store.GetSetting(context.Background(), SettingNetworkConfig)
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if ok {
		t.Error("expected no network_config setting after reverting an env-sourced pending change")
	}
}

func TestPutNetwork_AutoRevertOnDeadline(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	rec := &afterFuncRecorder{}
	s.netAfterFunc = newFakeAfterFunc(rec)

	body := []byte(`{"mode":"custom","addresses":["192.168.1.5"],"port":8090,"allowed_cidrs":["*"]}`)
	put := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body=%s)", put.Code, put.Body.String())
	}

	if rec.fn == nil {
		t.Fatal("expected an auto-revert callback to have been scheduled")
	}
	// Simulate the 120s deadline firing.
	rec.fn()

	s.networkMu.Lock()
	pending := s.networkPending
	s.networkMu.Unlock()
	if pending != nil {
		t.Errorf("expected pending to be cleared after auto-revert, got %+v", pending)
	}

	state, err := s.buildNetworkState(context.Background(), httpRequestFrom(t, "203.0.113.1:1234"))
	if err != nil {
		t.Fatalf("buildNetworkState: %v", err)
	}
	if state.Config.Mode != "all" {
		t.Errorf("Config.Mode = %q, want all after auto-revert", state.Config.Mode)
	}
}

func TestDeleteNetwork_ClearsOverride(t *testing.T) {
	t.Parallel()
	listener := newFakeListenController()
	store := newFakeStore()
	s := newTestServer(t, networkTestOptions(listener), store)

	// Persist a hub override first.
	body := []byte(`{"mode":"all","port":9090,"allowed_cidrs":["*"]}`)
	put := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, body)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body=%s)", put.Code, put.Body.String())
	}

	del := doRequest(t, s.Handler(), http.MethodDelete, "/api/v1/settings/network", "203.0.113.1:1234", networkAdminToken, nil)
	if del.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200 (body=%s)", del.Code, del.Body.String())
	}
	got := decodeJSON[models.NetworkState](t, del.Body)
	if got.Source != "env" {
		t.Errorf("Source = %q, want env after DELETE", got.Source)
	}

	_, ok, err := store.GetSetting(context.Background(), SettingNetworkConfig)
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if ok {
		t.Error("expected network_config setting to be cleared after DELETE")
	}
}

func TestAgentConnTracker_RecordAndList(t *testing.T) {
	t.Parallel()
	tracker := newAgentConnTracker()
	tracker.record("host-b", "beta", "10.0.0.2:8090")
	tracker.record("host-a", "alpha", "10.0.0.1:8090")
	tracker.record("host-a", "alpha", "10.0.0.9:8090") // update

	got := tracker.list()
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Hostname != "alpha" || got[0].LocalAddr != "10.0.0.9:8090" {
		t.Errorf("got[0] = %+v, want alpha with updated addr", got[0])
	}
	if got[1].Hostname != "beta" {
		t.Errorf("got[1] = %+v, want beta", got[1])
	}
}

// itoa avoids importing strconv into the test file solely for these
// small integer-to-string conversions in table literals above.
func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// httpRequestFrom builds a minimal *http.Request with the given
// RemoteAddr, for tests that call buildNetworkState directly rather
// than through the full handler stack.
func httpRequestFrom(t *testing.T, remoteAddr string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "/api/v1/settings/network", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.RemoteAddr = remoteAddr
	return req
}
