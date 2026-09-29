package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func sampleHostInfo(id string) models.HostInfo {
	return models.HostInfo{
		ID:               id,
		Hostname:         id + "-host",
		OS:               "linux",
		Platform:         "debian",
		Arch:             "arm64",
		CPUCores:         4,
		Provider:         models.ProviderOther,
		EgressLimitBytes: 0,
		AgentVersion:     "v0.1.0",
	}
}

func TestIngest_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	report := models.AgentReport{
		Host: sampleHostInfo("host-a"),
		Samples: []models.Sample{
			{Timestamp: now.Add(-30 * time.Second).Unix(), CPUPercent: 10},
			{Timestamp: now.Unix(), CPUPercent: 20},
		},
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.IngestResponse](t, rec.Body)
	if got.Accepted != 2 || got.Duplicates != 0 || got.Rejected != 0 {
		t.Errorf("IngestResponse = %+v, want Accepted=2 Duplicates=0 Rejected=0", got)
	}

	hostRec, err := store.GetHost(t.Context(), "host-a")
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if hostRec.Info.Hostname != "host-a-host" {
		t.Errorf("stored hostname = %q", hostRec.Info.Hostname)
	}
}

func TestIngest_EgressLimitStored(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	host := sampleHostInfo("host-b")
	host.EgressLimitBytes = 12345
	report := models.AgentReport{Host: host, Samples: []models.Sample{{Timestamp: now.Unix()}}}
	body, _ := json.Marshal(report)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	hostRec, err := store.GetHost(t.Context(), "host-b")
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if hostRec.Info.EgressLimitBytes != 12345 {
		t.Errorf("EgressLimitBytes = %d, want 12345", hostRec.Info.EgressLimitBytes)
	}
}

func TestIngest_Duplicates(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	report := models.AgentReport{
		Host:    sampleHostInfo("host-c"),
		Samples: []models.Sample{{Timestamp: now.Unix(), CPUPercent: 10}},
	}
	body, _ := json.Marshal(report)

	rec1 := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first ingest status = %d", rec1.Code)
	}

	rec2 := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second ingest status = %d", rec2.Code)
	}
	got := decodeJSON[models.IngestResponse](t, rec2.Body)
	if got.Accepted != 0 || got.Duplicates != 1 {
		t.Errorf("second ingest = %+v, want Accepted=0 Duplicates=1", got)
	}
}

func TestIngest_RejectsOutOfWindowSamples(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	report := models.AgentReport{
		Host: sampleHostInfo("host-d"),
		Samples: []models.Sample{
			{Timestamp: now.Unix()},                       // valid
			{Timestamp: now.Add(-27 * time.Hour).Unix()},  // too old
			{Timestamp: now.Add(10 * time.Minute).Unix()}, // too far in future
		},
	}
	body, _ := json.Marshal(report)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON[models.IngestResponse](t, rec.Body)
	if got.Accepted != 1 || got.Rejected != 2 {
		t.Errorf("IngestResponse = %+v, want Accepted=1 Rejected=2", got)
	}
}

func TestIngest_Unauthorized(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	report := models.AgentReport{Host: sampleHostInfo("host-e")}
	body, _ := json.Marshal(report)

	t.Run("missing_token", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", "", body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("WWW-Authenticate = %q, want Bearer", rec.Header().Get("WWW-Authenticate"))
		}
	})

	t.Run("wrong_token", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", "wrong-token-value", body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}

func TestIngest_BadHostID(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	s := newTestServer(t, opts, store)

	host := sampleHostInfo("host-f")
	host.ID = "bad id with spaces!"
	report := models.AgentReport{Host: host}
	body, _ := json.Marshal(report)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestIngest_TooManySamples(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Now()
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	samples := make([]models.Sample, 501)
	for i := range samples {
		samples[i] = models.Sample{Timestamp: now.Unix()}
	}
	report := models.AgentReport{Host: sampleHostInfo("host-g"), Samples: samples}
	body, _ := json.Marshal(report)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestIngest_InvalidBody(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, []byte("not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestListHosts_SortedAndStatus(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	mustUpsert := func(id, hostname string, lastSeen int64) {
		info := sampleHostInfo(id)
		info.Hostname = hostname
		if err := store.UpsertHost(t.Context(), info, lastSeen); err != nil {
			t.Fatalf("UpsertHost: %v", err)
		}
	}
	mustUpsert("h1", "zeta", now.Unix())                      // up
	mustUpsert("h2", "alpha", now.Add(-5*time.Minute).Unix()) // down (offline after 60s)

	opts := testOptions()
	opts.Now = fixedNow(now)
	opts.OfflineAfter = 60 * time.Second
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.1:1234", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Hosts       []models.HostSummary `json:"hosts"`
		GeneratedAt int64                `json:"generated_at"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Hosts) != 2 {
		t.Fatalf("hosts = %+v, want 2 entries", got.Hosts)
	}
	if got.Hosts[0].Host.Hostname != "alpha" || got.Hosts[1].Host.Hostname != "zeta" {
		t.Errorf("hosts not sorted by hostname: %+v", got.Hosts)
	}
	if got.Hosts[0].Status != models.HostDown {
		t.Errorf("alpha status = %q, want down", got.Hosts[0].Status)
	}
	if got.Hosts[1].Status != models.HostUp {
		t.Errorf("zeta status = %q, want up", got.Hosts[1].Status)
	}
	if got.GeneratedAt != now.Unix() {
		t.Errorf("generated_at = %d, want %d", got.GeneratedAt, now.Unix())
	}
}

func TestGetHost_NotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts/does-not-exist", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Error == "" {
		t.Error("expected non-empty error")
	}
}

func TestGetHost_Found(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertHost(t.Context(), sampleHostInfo("host-h"), now.Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts/host-h", "203.0.113.1:1234", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON[models.HostSummary](t, rec.Body)
	if got.Host.ID != "host-h" {
		t.Errorf("host id = %q, want host-h", got.Host.ID)
	}
}

func TestHostMetrics_RangeValidation(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertHost(t.Context(), sampleHostInfo("host-i"), now.Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	cases := []struct {
		rangeParam string
		wantStatus int
	}{
		{"", http.StatusOK},
		{"1h", http.StatusOK},
		{"6h", http.StatusOK},
		{"24h", http.StatusOK},
		{"7d", http.StatusOK},
		{"30d", http.StatusOK},
		{"bogus", http.StatusBadRequest},
		{"1y", http.StatusBadRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("range=%q", tc.rangeParam), func(t *testing.T) {
			t.Parallel()
			path := "/api/v1/hosts/host-i/metrics"
			if tc.rangeParam != "" {
				path += "?range=" + tc.rangeParam
			}
			rec := doRequest(t, s.Handler(), http.MethodGet, path, "203.0.113.1:1234", opts.UIToken, nil)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestHostMetrics_UnknownHost404(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts/nope/metrics", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestEgress_DefaultsToCurrentMonth(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertHost(t.Context(), sampleHostInfo("host-j"), now.Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	store.setEgress("host-j", "2026-01", 1000, 2000)

	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/egress", "203.0.113.1:1234", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Month string            `json:"month"`
		Hosts []egressHostEntry `json:"hosts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Month != "2026-01" {
		t.Errorf("month = %q, want 2026-01", got.Month)
	}
	if len(got.Hosts) != 1 || got.Hosts[0].Egress.TxBytes != 1000 {
		t.Errorf("hosts = %+v", got.Hosts)
	}
}

func TestEgress_BadMonth400(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/egress?month=not-a-month", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestBuckets_EmptyListsNotNull(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/buckets", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"buckets":[]`) {
		t.Errorf("expected buckets:[] in body, got %s", body)
	}
	if !strings.Contains(body, `"collectors":[]`) {
		t.Errorf("expected collectors:[] in body, got %s", body)
	}
}

func TestBuckets_WithData(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if err := store.SaveBucketStats(t.Context(), []models.BucketStats{
		{Provider: models.StorageR2, Bucket: "my-bucket", CollectedAt: now.Unix(), SizeBytes: 100},
	}); err != nil {
		t.Fatalf("SaveBucketStats: %v", err)
	}

	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/buckets", "203.0.113.1:1234", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Buckets []models.BucketView `json:"buckets"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Buckets) != 1 {
		t.Fatalf("buckets = %+v, want 1", got.Buckets)
	}
	if got.Buckets[0].FreeTier == nil {
		t.Error("expected FreeTier set for r2 bucket")
	}
	if len(got.Buckets[0].History) != 1 {
		t.Errorf("history = %+v, want 1 point", got.Buckets[0].History)
	}
}

func TestUIToken_EnforcedWhenSet(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.UIToken = "ui-token-1234"
	s := newTestServer(t, opts, store)

	t.Run("missing_token_401", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.1:1234", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("correct_token_200", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.1:1234", opts.UIToken, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("wrong_token_401", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.1:1234", "wrong-value-here", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}

func TestUIToken_UnsetMeansNoStaticTokenBypass(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.UIToken = "" // no static API token configured
	s := newTestServer(t, opts, store)

	// No bearer token at all: unauthenticated.
	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (dashboard always requires sign-in per SPEC-v0.4 §1)", rec.Code)
	}

	// A valid session still works even with no static UIToken configured.
	token := loginAndGetToken(t, s, store, "correct-password-1")
	rec2 := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.1:1234", token, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a valid session (body=%s)", rec2.Code, rec2.Body.String())
	}
}

func TestHealthzAndStatic_NeverRequireUIToken(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.UIToken = "ui-token-1234"
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/healthz", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", rec.Code)
	}
}
