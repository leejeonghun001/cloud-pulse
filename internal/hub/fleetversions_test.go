package hub

import (
	"net/http"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestBuildFleetVersionSummary_TableTests(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		hosts           []fleetVersionHostInput
		latest          string
		wantUpToDate    int
		wantOutdated    int
		wantUnknown     int
		wantCapable     int
		wantNotOptIn    int
		wantUnsup       int
		wantOutdatedIDs []string
	}{
		{
			name: "up_to_date_and_outdated_mix",
			hosts: []fleetVersionHostInput{
				{HostID: "h1", Version: "v0.7.0", OS: "linux", Capability: models.RemoteUpdateCapability{Supported: true, OptedIn: true}},
				{HostID: "h2", Version: "v0.6.0", OS: "linux", Capability: models.RemoteUpdateCapability{Supported: true, OptedIn: false}},
			},
			latest:          "v0.7.0",
			wantUpToDate:    1,
			wantOutdated:    1,
			wantCapable:     1,
			wantNotOptIn:    1,
			wantOutdatedIDs: []string{"h2"},
		},
		{
			name: "prerelease_host_version_vs_release_latest",
			hosts: []fleetVersionHostInput{
				{HostID: "h1", Version: "v0.7.0-rc.1", OS: "linux", Capability: models.RemoteUpdateCapability{Supported: true, OptedIn: true}},
			},
			latest: "v0.7.0",
			// v0.7.0-rc.1 parses and is strictly older than v0.7.0
			// under plain semver precedence (pre-release < release) —
			// so IsNewer(latest, hostVersion) is true and this counts
			// as outdated, not unknown.
			wantOutdated:    1,
			wantCapable:     1,
			wantOutdatedIDs: []string{"h1"},
		},
		{
			name: "dev_build_host_version_is_unknown",
			hosts: []fleetVersionHostInput{
				{HostID: "h1", Version: "dev", OS: "linux", Capability: models.RemoteUpdateCapability{Supported: true, OptedIn: true}},
				{HostID: "h2", Version: "v0.2.0-3-gabc1234", OS: "linux", Capability: models.RemoteUpdateCapability{Supported: false}},
			},
			latest:      "v0.7.0",
			wantUnknown: 2,
			wantCapable: 1,
			wantUnsup:   1,
		},
		{
			name: "unknown_latest_never_marks_outdated",
			hosts: []fleetVersionHostInput{
				{HostID: "h1", Version: "v0.1.0", OS: "linux", Capability: models.RemoteUpdateCapability{Supported: true, OptedIn: true}},
			},
			latest:      "",
			wantUnknown: 1,
			wantCapable: 1,
		},
		{
			name: "empty_host_version_is_unknown",
			hosts: []fleetVersionHostInput{
				{HostID: "h1", Version: "", OS: "linux", Capability: models.RemoteUpdateCapability{Supported: false}},
			},
			latest:      "v0.7.0",
			wantUnknown: 1,
			wantUnsup:   1,
		},
		{
			name: "missing_os_still_aggregates",
			hosts: []fleetVersionHostInput{
				{HostID: "h1", Version: "v0.7.0", OS: "", Capability: models.RemoteUpdateCapability{Supported: true, OptedIn: true}},
			},
			latest:       "v0.7.0",
			wantUpToDate: 1,
			wantCapable:  1,
		},
		{
			name: "unsupported_host_is_never_capable_even_with_reason_unset",
			hosts: []fleetVersionHostInput{
				{HostID: "h1", Version: "v0.7.0", OS: "windows", Capability: models.RemoteUpdateCapability{Supported: false, OptedIn: true}},
			},
			latest:       "v0.7.0",
			wantUpToDate: 1,
			wantUnsup:    1,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BuildFleetVersionSummary(tc.hosts, tc.latest, nil)

			if got.UpToDateCount != tc.wantUpToDate {
				t.Errorf("UpToDateCount = %d, want %d", got.UpToDateCount, tc.wantUpToDate)
			}
			if got.OutdatedCount != tc.wantOutdated {
				t.Errorf("OutdatedCount = %d, want %d", got.OutdatedCount, tc.wantOutdated)
			}
			if got.UnknownCount != tc.wantUnknown {
				t.Errorf("UnknownCount = %d, want %d", got.UnknownCount, tc.wantUnknown)
			}
			if got.CapableCount != tc.wantCapable {
				t.Errorf("CapableCount = %d, want %d", got.CapableCount, tc.wantCapable)
			}
			if got.NotOptedInCount != tc.wantNotOptIn {
				t.Errorf("NotOptedInCount = %d, want %d", got.NotOptedInCount, tc.wantNotOptIn)
			}
			if got.UnsupportedCount != tc.wantUnsup {
				t.Errorf("UnsupportedCount = %d, want %d", got.UnsupportedCount, tc.wantUnsup)
			}
			if got.TotalHosts != len(tc.hosts) {
				t.Errorf("TotalHosts = %d, want %d", got.TotalHosts, len(tc.hosts))
			}

			var gotOutdatedIDs []string
			for _, h := range got.Hosts {
				if h.Outdated {
					gotOutdatedIDs = append(gotOutdatedIDs, h.HostID)
				}
			}
			if len(gotOutdatedIDs) != len(tc.wantOutdatedIDs) {
				t.Errorf("outdated host IDs = %v, want %v", gotOutdatedIDs, tc.wantOutdatedIDs)
			}
		})
	}
}

// TestBuildFleetVersionSummary_OutdatedSinceThreadedThrough verifies the
// hostOutdatedSince map is carried through verbatim for outdated hosts
// and left zero for others.
func TestBuildFleetVersionSummary_OutdatedSinceThreadedThrough(t *testing.T) {
	t.Parallel()
	hosts := []fleetVersionHostInput{
		{HostID: "h1", Version: "v0.6.0", OS: "linux", Capability: models.RemoteUpdateCapability{Supported: true, OptedIn: true}},
		{HostID: "h2", Version: "v0.7.0", OS: "linux", Capability: models.RemoteUpdateCapability{Supported: true, OptedIn: true}},
	}
	since := map[string]int64{"h1": 1700000000, "h2": 999999} // h2 is up to date; its since must be ignored
	got := BuildFleetVersionSummary(hosts, "v0.7.0", since)

	byID := make(map[string]models.FleetVersionHost)
	for _, h := range got.Hosts {
		byID[h.HostID] = h
	}
	if byID["h1"].OutdatedSince != 1700000000 {
		t.Errorf("h1 OutdatedSince = %d, want 1700000000", byID["h1"].OutdatedSince)
	}
	if byID["h2"].OutdatedSince != 0 {
		t.Errorf("h2 OutdatedSince = %d, want 0 (not outdated)", byID["h2"].OutdatedSince)
	}
}

// TestBuildFleetVersionSummary_DistributionSortedByOSThenVersion checks
// the version x OS histogram's ordering: OS ascending, then descending
// semver within an OS, with unparseable labels sorted last.
func TestBuildFleetVersionSummary_DistributionSortedByOSThenVersion(t *testing.T) {
	t.Parallel()
	hosts := []fleetVersionHostInput{
		{HostID: "h1", Version: "v0.6.0", OS: "linux"},
		{HostID: "h2", Version: "v0.7.0", OS: "linux"},
		{HostID: "h3", Version: "dev", OS: "linux"},
		{HostID: "h4", Version: "v0.7.0", OS: "darwin"},
		{HostID: "h5", Version: "v0.6.0", OS: "linux"},
	}
	got := BuildFleetVersionSummary(hosts, "v0.7.0", nil)

	if len(got.Distribution) != 4 {
		t.Fatalf("Distribution len = %d, want 4, got %+v", len(got.Distribution), got.Distribution)
	}
	// darwin sorts before linux.
	if got.Distribution[0].OS != "darwin" {
		t.Errorf("Distribution[0].OS = %q, want darwin", got.Distribution[0].OS)
	}
	// Within linux: v0.7.0 (newest) then v0.6.0 then "dev" (unparseable, last).
	linuxBuckets := got.Distribution[1:]
	if linuxBuckets[0].Version != "v0.7.0" || linuxBuckets[1].Version != "v0.6.0" || linuxBuckets[2].Version != "dev" {
		t.Errorf("linux buckets order = %+v, want v0.7.0, v0.6.0, dev", linuxBuckets)
	}
	if linuxBuckets[1].Count != 2 {
		t.Errorf("v0.6.0/linux count = %d, want 2 (h1+h5)", linuxBuckets[1].Count)
	}
}

func TestClassifyRemoteUpdateCapability(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cap  models.RemoteUpdateCapability
		want models.RemoteUpdateCapabilityClass
	}{
		{"capable", models.RemoteUpdateCapability{Supported: true, OptedIn: true}, models.RemoteUpdateClassCapable},
		{"not_opted_in", models.RemoteUpdateCapability{Supported: true, OptedIn: false}, models.RemoteUpdateClassNotOptedIn},
		{"unsupported", models.RemoteUpdateCapability{Supported: false, OptedIn: true}, models.RemoteUpdateClassUnsupported},
		{"unsupported_zero_value", models.RemoteUpdateCapability{}, models.RemoteUpdateClassUnsupported},
	}
	for _, tc := range cases {
		if got := classifyRemoteUpdateCapability(tc.cap); got != tc.want {
			t.Errorf("%s: classifyRemoteUpdateCapability() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// --- HTTP handler tests ---

func TestHandleFleetVersions_RequireUser(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/fleet/versions", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (no token)", rec.Code)
	}

	rec2 := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/fleet/versions", "203.0.113.1:1234", testUIToken, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (with token), body=%s", rec2.Code, rec2.Body.String())
	}
	got := decodeJSON[models.FleetVersionSummary](t, rec2.Body)
	if got.Hosts == nil {
		// zero hosts is fine, but the field should still decode without error
	}
}

func TestHandleFleetVersions_AggregatesRealHosts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	s := newTestServer(t, opts, store)

	now := opts.now()
	if err := store.UpsertHost(t.Context(), models.HostInfo{
		ID: "h1", Hostname: "host-one", OS: "linux", AgentVersion: "v0.6.0",
		RemoteUpdate: models.RemoteUpdateCapability{Supported: true, OptedIn: true},
	}, now.Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	// Record a successful update check so LatestVersion is known.
	s.recordUpdateCheck(now, "v0.7.0", nil)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/fleet/versions", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.FleetVersionSummary](t, rec.Body)
	if got.LatestVersion != "v0.7.0" {
		t.Errorf("LatestVersion = %q, want v0.7.0", got.LatestVersion)
	}
	if got.TotalHosts != 1 {
		t.Fatalf("TotalHosts = %d, want 1", got.TotalHosts)
	}
	if got.OutdatedCount != 1 {
		t.Errorf("OutdatedCount = %d, want 1", got.OutdatedCount)
	}
	if len(got.Hosts) != 1 || got.Hosts[0].HostID != "h1" {
		t.Fatalf("Hosts = %+v, want one host h1", got.Hosts)
	}
	if !got.Hosts[0].Outdated {
		t.Error("Hosts[0].Outdated = false, want true")
	}
	if got.Hosts[0].OutdatedSince == 0 {
		t.Error("Hosts[0].OutdatedSince = 0, want non-zero once observed outdated")
	}
}

func TestFleetOutdatedTracker_SinceSurvivesAndRegressionResets(t *testing.T) {
	t.Parallel()
	tr := newFleetVersionTracker()

	t1 := time.Unix(1000, 0)
	since1 := tr.update(map[string]bool{"h1": true}, t1)
	if since1["h1"] != 1000 {
		t.Fatalf("since1[h1] = %d, want 1000", since1["h1"])
	}

	// Still outdated later: since must be preserved, not reset to t2.
	t2 := time.Unix(2000, 0)
	since2 := tr.update(map[string]bool{"h1": true}, t2)
	if since2["h1"] != 1000 {
		t.Errorf("since2[h1] = %d, want 1000 (preserved)", since2["h1"])
	}

	// Host catches up: entry removed.
	t3 := time.Unix(3000, 0)
	since3 := tr.update(map[string]bool{}, t3)
	if _, ok := since3["h1"]; ok {
		t.Error("since3 should not contain h1 once no longer outdated")
	}

	// Regresses again later: fresh "since" window starts at t4, not t1.
	t4 := time.Unix(4000, 0)
	since4 := tr.update(map[string]bool{"h1": true}, t4)
	if since4["h1"] != 4000 {
		t.Errorf("since4[h1] = %d, want 4000 (fresh window after regression)", since4["h1"])
	}
}
