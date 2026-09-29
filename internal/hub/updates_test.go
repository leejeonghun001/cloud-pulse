package hub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// TestDecideAgentUpdate covers the versions x OS x legacy matrix from
// SPEC-v0.3 D-U4.
func TestDecideAgentUpdate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		agentVersion  string
		hubVersion    string
		latestKnown   string
		goos          string
		wantNil       bool
		wantAvailable bool
		wantLatest    string
		wantSelf      bool
		wantCommand   string
	}{
		{
			name:          "up_to_date_current_release",
			agentVersion:  "v0.3.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.0",
			goos:          "linux",
			wantAvailable: false,
			wantLatest:    "v0.3.0",
			wantSelf:      true,
			wantCommand:   "sudo cloud-pulse-agent update",
		},
		{
			name:          "outdated_self_update_linux",
			agentVersion:  "v0.3.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.1",
			goos:          "linux",
			wantAvailable: true,
			wantLatest:    "v0.3.1",
			wantSelf:      true,
			wantCommand:   "sudo cloud-pulse-agent update",
		},
		{
			name:          "outdated_self_update_darwin",
			agentVersion:  "v0.3.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.1",
			goos:          "darwin",
			wantAvailable: true,
			wantLatest:    "v0.3.1",
			wantSelf:      true,
			wantCommand:   "sudo cloud-pulse-agent update",
		},
		{
			name:          "outdated_self_update_freebsd",
			agentVersion:  "v0.3.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.1",
			goos:          "freebsd",
			wantAvailable: true,
			wantLatest:    "v0.3.1",
			wantSelf:      true,
			wantCommand:   "sudo cloud-pulse-agent update",
		},
		{
			name:          "outdated_self_update_windows",
			agentVersion:  "v0.3.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.1",
			goos:          "windows",
			wantAvailable: true,
			wantLatest:    "v0.3.1",
			wantSelf:      true,
			wantCommand:   "cloud-pulse-agent update (run as Administrator)",
		},
		{
			name:          "legacy_agent_linux",
			agentVersion:  "v0.2.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.1",
			goos:          "linux",
			wantAvailable: true,
			wantLatest:    "v0.3.1",
			wantSelf:      false,
			wantCommand:   legacyInstallCommand,
		},
		{
			name:          "legacy_agent_v0_1",
			agentVersion:  "v0.1.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "",
			goos:          "linux",
			wantAvailable: true,
			wantLatest:    "v0.3.0",
			wantSelf:      false,
			wantCommand:   legacyInstallCommand,
		},
		{
			name:          "legacy_agent_darwin",
			agentVersion:  "v0.2.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.1",
			goos:          "darwin",
			wantAvailable: true,
			wantLatest:    "v0.3.1",
			wantSelf:      false,
			wantCommand:   "download the new binary from the release page",
		},
		{
			name:          "legacy_agent_windows",
			agentVersion:  "v0.1.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.1",
			goos:          "windows",
			wantAvailable: true,
			wantLatest:    "v0.3.1",
			wantSelf:      false,
			wantCommand:   "download the new binary from the release page",
		},
		{
			name:          "legacy_agent_unknown_os_defaults_to_linux_command",
			agentVersion:  "v0.2.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.1",
			goos:          "",
			wantAvailable: true,
			wantLatest:    "v0.3.1",
			wantSelf:      false,
			wantCommand:   legacyInstallCommand,
		},
		{
			name:          "exactly_at_self_update_since",
			agentVersion:  version.SelfUpdateSince,
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.0",
			goos:          "linux",
			wantAvailable: false,
			wantLatest:    "v0.3.0",
			wantSelf:      true,
			wantCommand:   "sudo cloud-pulse-agent update",
		},
		{
			name:          "reference_falls_back_to_hub_version_when_no_check_yet",
			agentVersion:  "v0.2.5",
			hubVersion:    "v0.3.2",
			latestKnown:   "",
			goos:          "linux",
			wantAvailable: true,
			wantLatest:    "v0.3.2",
			wantSelf:      false,
			wantCommand:   legacyInstallCommand,
		},
		{
			name:          "prerelease_agent_vs_release_latest",
			agentVersion:  "v0.3.0-rc.1",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.0",
			goos:          "linux",
			wantAvailable: true,
			wantLatest:    "v0.3.0",
			wantSelf:      false, // v0.3.0-rc.1 < v0.3.0 == SelfUpdateSince
			wantCommand:   legacyInstallCommand,
		},
		{
			name:         "unparsable_agent_version_no_info",
			agentVersion: "dev",
			hubVersion:   "v0.3.0",
			latestKnown:  "v0.3.1",
			goos:         "linux",
			wantNil:      true,
		},
		{
			name:         "unparsable_agent_version_git_describe",
			agentVersion: "v0.2.0-3-gabc1234",
			hubVersion:   "v0.3.0",
			latestKnown:  "v0.3.1",
			goos:         "linux",
			wantNil:      true,
		},
		{
			name:         "neither_hub_nor_latest_parse_no_info",
			agentVersion: "v0.2.0",
			hubVersion:   "dev",
			latestKnown:  "",
			goos:         "linux",
			wantNil:      true,
		},
		{
			name:          "newer_agent_than_reference_not_available",
			agentVersion:  "v0.4.0",
			hubVersion:    "v0.3.0",
			latestKnown:   "v0.3.1",
			goos:          "linux",
			wantAvailable: false,
			wantLatest:    "v0.3.1",
			wantSelf:      true,
			wantCommand:   "sudo cloud-pulse-agent update",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := decideAgentUpdate(tc.agentVersion, tc.hubVersion, tc.latestKnown, tc.goos)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("decideAgentUpdate() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("decideAgentUpdate() = nil, want non-nil")
			}
			if got.Available != tc.wantAvailable {
				t.Errorf("Available = %v, want %v", got.Available, tc.wantAvailable)
			}
			if got.Latest != tc.wantLatest {
				t.Errorf("Latest = %q, want %q", got.Latest, tc.wantLatest)
			}
			if got.SelfUpdate != tc.wantSelf {
				t.Errorf("SelfUpdate = %v, want %v", got.SelfUpdate, tc.wantSelf)
			}
			if got.Command != tc.wantCommand {
				t.Errorf("Command = %q, want %q", got.Command, tc.wantCommand)
			}
		})
	}
}

func TestNormalizeGOOS(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"linux", "linux"},
		{"darwin", "darwin"},
		{"windows", "windows"},
		{"freebsd", "freebsd"},
		{"", "linux"},
		{"plan9", "linux"},
		{"Linux", "linux"}, // case-sensitive match only; unrecognized -> default
	}
	for _, tc := range cases {
		if got := normalizeGOOS(tc.in); got != tc.want {
			t.Errorf("normalizeGOOS(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// fakeResolver is a LatestResolver test double.
type fakeResolver struct {
	mu    sync.Mutex
	tag   string
	err   error
	calls int
}

func (f *fakeResolver) Latest(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.tag, f.err
}

func (f *fakeResolver) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeResolver) set(tag string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tag, f.err = tag, err
}

func TestCheckForUpdate_Success(t *testing.T) {
	t.Parallel()

	resolver := &fakeResolver{tag: "v0.3.1"}
	store := newFakeStore()
	opts := testOptions()
	opts.UpdateSource = resolver
	s := New(opts, store, nil, nil, nil, testLogger())

	s.checkForUpdate(context.Background())

	st := s.updateStatusSnapshot()
	if st.latestVersion != "v0.3.1" {
		t.Errorf("latestVersion = %q, want v0.3.1", st.latestVersion)
	}
	if st.checkError != "" {
		t.Errorf("checkError = %q, want empty", st.checkError)
	}
	if st.checkedAt == 0 {
		t.Error("checkedAt should be non-zero after a check")
	}
	if st.consecutiveFailures != 0 {
		t.Errorf("consecutiveFailures = %d, want 0", st.consecutiveFailures)
	}
}

func TestCheckForUpdate_Failure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("network unreachable")
	resolver := &fakeResolver{err: wantErr}
	store := newFakeStore()
	opts := testOptions()
	opts.UpdateSource = resolver
	s := New(opts, store, nil, nil, nil, testLogger())

	s.checkForUpdate(context.Background())

	st := s.updateStatusSnapshot()
	if st.latestVersion != "" {
		t.Errorf("latestVersion = %q, want empty after failure", st.latestVersion)
	}
	if st.checkError != wantErr.Error() {
		t.Errorf("checkError = %q, want %q", st.checkError, wantErr.Error())
	}
	if st.consecutiveFailures != 1 {
		t.Errorf("consecutiveFailures = %d, want 1", st.consecutiveFailures)
	}

	// A second failure increments the streak but keeps the same error.
	s.checkForUpdate(context.Background())
	st2 := s.updateStatusSnapshot()
	if st2.consecutiveFailures != 2 {
		t.Errorf("consecutiveFailures after second failure = %d, want 2", st2.consecutiveFailures)
	}
}

func TestCheckForUpdate_InvalidTagRejected(t *testing.T) {
	t.Parallel()

	// A malicious/buggy resolver returning something that isn't a clean
	// tag (e.g. an injection attempt) must never be stored as
	// latestVersion.
	resolver := &fakeResolver{tag: "v1.0.0/../../etc/passwd"}
	store := newFakeStore()
	opts := testOptions()
	opts.UpdateSource = resolver
	s := New(opts, store, nil, nil, nil, testLogger())

	s.checkForUpdate(context.Background())

	st := s.updateStatusSnapshot()
	if st.latestVersion != "" {
		t.Errorf("latestVersion = %q, want empty for invalid tag", st.latestVersion)
	}
	if st.checkError == "" {
		t.Error("checkError should be set for an invalid tag")
	}
}

func TestCheckForUpdate_RecoversAfterFailure(t *testing.T) {
	t.Parallel()

	resolver := &fakeResolver{err: errors.New("temporary failure")}
	store := newFakeStore()
	opts := testOptions()
	opts.UpdateSource = resolver
	s := New(opts, store, nil, nil, nil, testLogger())

	s.checkForUpdate(context.Background())
	if st := s.updateStatusSnapshot(); st.consecutiveFailures != 1 {
		t.Fatalf("consecutiveFailures = %d, want 1", st.consecutiveFailures)
	}

	resolver.set("v0.3.1", nil)
	s.checkForUpdate(context.Background())

	st := s.updateStatusSnapshot()
	if st.latestVersion != "v0.3.1" {
		t.Errorf("latestVersion = %q, want v0.3.1", st.latestVersion)
	}
	if st.checkError != "" {
		t.Errorf("checkError = %q, want empty after recovery", st.checkError)
	}
	if st.consecutiveFailures != 0 {
		t.Errorf("consecutiveFailures = %d, want 0 after recovery", st.consecutiveFailures)
	}
}

func TestRunUpdateCheckLoop_NilResolverIsNoOp(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	opts := testOptions() // UpdateSource left nil
	s := New(opts, store, nil, nil, nil, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.runUpdateCheckLoop(ctx, time.Millisecond, time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runUpdateCheckLoop with nil UpdateSource did not return promptly")
	}

	if st := s.updateStatusSnapshot(); st.checkedAt != 0 {
		t.Errorf("checkedAt = %d, want 0 (no check should have run)", st.checkedAt)
	}
}

func TestRunUpdateCheckLoop_ChecksOnInjectedSchedule(t *testing.T) {
	t.Parallel()

	resolver := &fakeResolver{tag: "v0.3.1"}
	store := newFakeStore()
	opts := testOptions()
	opts.UpdateSource = resolver
	s := New(opts, store, nil, nil, nil, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		// Tiny injected delays so the loop fires several times within
		// the test's budget, unlike the real 30s/24h schedule.
		s.runUpdateCheckLoop(ctx, 10*time.Millisecond, 20*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runUpdateCheckLoop did not return within 3s of ctx cancellation")
	}

	if calls := resolver.callCount(); calls < 2 {
		t.Errorf("resolver called %d times, want at least 2 within 250ms at a ~10-20ms cadence", calls)
	}
	if st := s.updateStatusSnapshot(); st.latestVersion != "v0.3.1" {
		t.Errorf("latestVersion = %q, want v0.3.1", st.latestVersion)
	}
}

func TestRunBackground_RunsUpdateCheckLoop(t *testing.T) {
	t.Parallel()

	resolver := &fakeResolver{tag: "v0.3.1"}
	store := newFakeStore()
	opts := testOptions()
	opts.UpdateSource = resolver
	s := New(opts, store, nil, nil, nil, testLogger())
	s.updateFirstDelay = 10 * time.Millisecond
	s.updateInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.RunBackground(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunBackground did not return within 3s of ctx cancellation")
	}

	if resolver.callCount() < 1 {
		t.Error("expected RunBackground to invoke the update resolver at least once")
	}
}

func TestBuildVersionInfo(t *testing.T) {
	t.Parallel()

	t.Run("no_check_yet", func(t *testing.T) {
		t.Parallel()
		info := buildVersionInfo(true, updateStatus{})
		if info.UpdateAvailable {
			t.Error("UpdateAvailable = true, want false with no latestVersion known")
		}
		if info.ReleaseURL != "" {
			t.Errorf("ReleaseURL = %q, want empty", info.ReleaseURL)
		}
		if !info.UpdateCheckEnabled {
			t.Error("UpdateCheckEnabled = false, want true")
		}
		if info.UpdateCommand != "sudo cloud-pulse-hub update" {
			t.Errorf("UpdateCommand = %q", info.UpdateCommand)
		}
	})

	t.Run("check_disabled", func(t *testing.T) {
		t.Parallel()
		info := buildVersionInfo(false, updateStatus{})
		if info.UpdateCheckEnabled {
			t.Error("UpdateCheckEnabled = true, want false")
		}
	})

	t.Run("latest_known_builds_release_url", func(t *testing.T) {
		t.Parallel()
		info := buildVersionInfo(true, updateStatus{latestVersion: "v9.9.9", checkedAt: 1234})
		want := "https://github.com/leejeonghun001/cloud-pulse/releases/tag/v9.9.9"
		if info.ReleaseURL != want {
			t.Errorf("ReleaseURL = %q, want %q", info.ReleaseURL, want)
		}
		if info.CheckedAt != 1234 {
			t.Errorf("CheckedAt = %d, want 1234", info.CheckedAt)
		}
	})

	t.Run("check_error_propagated", func(t *testing.T) {
		t.Parallel()
		info := buildVersionInfo(true, updateStatus{checkError: "boom"})
		if info.CheckError != "boom" {
			t.Errorf("CheckError = %q, want boom", info.CheckError)
		}
	})
}
