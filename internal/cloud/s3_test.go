package cloud

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// fakeCloudWatchServer builds an httptest server that answers
// GetMetricData POSTs using values keyed by (bucket name, metric
// name). Missing entries yield no datapoints for that metric.
type fakeMetricValue struct {
	value float64
	ts    time.Time
}

func fakeCloudWatchServer(t *testing.T, values map[string][]fakeMetricValue) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		form := r.Form

		type resultXML struct {
			ID         string    `xml:"Id"`
			Timestamps []string  `xml:"Timestamps>member"`
			Values     []float64 `xml:"Values>member"`
		}
		var results []resultXML

		for i := 1; ; i++ {
			prefix := fmt.Sprintf("MetricDataQueries.member.%d.", i)
			id := form.Get(prefix + "Id")
			if id == "" {
				break
			}
			metricName := form.Get(prefix + "MetricStat.Metric.MetricName")
			bucketName := form.Get(prefix + "MetricStat.Metric.Dimensions.member.1.Value")
			key := bucketName + "/" + metricName
			var timestamps []string
			var vals []float64
			for _, fv := range values[key] {
				timestamps = append(timestamps, fv.ts.UTC().Format(time.RFC3339))
				vals = append(vals, fv.value)
			}
			results = append(results, resultXML{ID: id, Timestamps: timestamps, Values: vals})
		}

		type responseXML struct {
			XMLName xml.Name `xml:"GetMetricDataResponse"`
			Result  struct {
				MetricDataResults []resultXML `xml:"MetricDataResults>member"`
			} `xml:"GetMetricDataResult"`
		}
		var resp responseXML
		resp.Result.MetricDataResults = results

		w.Header().Set("Content-Type", "text/xml")
		_ = xml.NewEncoder(w).Encode(resp) // best-effort: test server response, failures surface as client decode errors
	}))
}

func TestS3Collector_Collect_EndToEnd(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)

	values := map[string][]fakeMetricValue{
		"bucket-a/BucketSizeBytes": {{value: 1000, ts: now.Add(-2 * 24 * time.Hour)}, {value: 2000, ts: now.Add(-1 * time.Hour)}},
		"bucket-a/NumberOfObjects": {{value: 10, ts: now.Add(-1 * time.Hour)}},
		"bucket-a/BytesDownloaded": {{value: 500, ts: now.Add(-48 * time.Hour)}, {value: 300, ts: now.Add(-24 * time.Hour)}},
		"bucket-a/AllRequests":     {{value: 7, ts: now.Add(-5 * time.Minute)}},
		"bucket-a/PutRequests":     {{value: 4, ts: now.Add(-24 * time.Hour)}},
		"bucket-a/ListRequests":    {{value: 1, ts: now.Add(-24 * time.Hour)}},
		"bucket-a/GetRequests":     {{value: 9, ts: now.Add(-24 * time.Hour)}},
		// bucket-b has no request metrics at all -> RequestMetricsAvailable=false
		"bucket-b/BucketSizeBytes": {{value: 555, ts: now.Add(-1 * time.Hour)}},
		"bucket-b/NumberOfObjects": {{value: 3, ts: now.Add(-1 * time.Hour)}},
	}

	srv := fakeCloudWatchServer(t, values)
	defer srv.Close()

	col := &S3Collector{
		Buckets: []S3Bucket{
			{Name: "bucket-a", Region: "us-east-1"},
			{Name: "bucket-b", Region: "us-east-1"},
		},
		Creds:  Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		Now:    fixedNow(now),
		Window: 15 * time.Minute,
		Endpoint: func(region string) string {
			return srv.URL
		},
	}

	if got := col.Name(); got != "s3" {
		t.Fatalf("Name() = %q, want s3", got)
	}

	stats, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("len(stats) = %d, want 2", len(stats))
	}

	byName := map[string]models.BucketStats{}
	for _, s := range stats {
		byName[s.Bucket] = s
	}

	a := byName["bucket-a"]
	if a.SizeBytes != 2000 {
		t.Errorf("bucket-a SizeBytes = %d, want 2000 (latest datapoint)", a.SizeBytes)
	}
	if a.ObjectCount != 10 {
		t.Errorf("bucket-a ObjectCount = %d, want 10", a.ObjectCount)
	}
	if a.ClassAOpsMTD != 5 { // 4 Put + 1 List
		t.Errorf("bucket-a ClassAOpsMTD = %d, want 5", a.ClassAOpsMTD)
	}
	if a.ClassBOpsMTD != 9 { // 9 Get
		t.Errorf("bucket-a ClassBOpsMTD = %d, want 9", a.ClassBOpsMTD)
	}
	if a.EgressBytesMTD != 800 {
		t.Errorf("bucket-a EgressBytesMTD = %d, want 800", a.EgressBytesMTD)
	}
	if a.RequestsWindow != 7 {
		t.Errorf("bucket-a RequestsWindow = %d, want 7", a.RequestsWindow)
	}
	if a.WindowSeconds != int64((15 * time.Minute).Seconds()) {
		t.Errorf("bucket-a WindowSeconds = %d, want 900", a.WindowSeconds)
	}
	if !a.RequestMetricsAvailable {
		t.Errorf("bucket-a RequestMetricsAvailable = false, want true")
	}
	if a.Provider != models.StorageS3 || a.Region != "us-east-1" {
		t.Errorf("bucket-a Provider/Region = %v/%v", a.Provider, a.Region)
	}
	if a.CollectedAt != now.Unix() {
		t.Errorf("bucket-a CollectedAt = %d, want %d", a.CollectedAt, now.Unix())
	}

	b := byName["bucket-b"]
	if b.RequestMetricsAvailable {
		t.Errorf("bucket-b RequestMetricsAvailable = true, want false (no request datapoints)")
	}
	if b.SizeBytes != 555 || b.ObjectCount != 3 {
		t.Errorf("bucket-b size/objects = %d/%d, want 555/3", b.SizeBytes, b.ObjectCount)
	}
	if b.Error != "" {
		t.Errorf("bucket-b Error = %q, want empty", b.Error)
	}
}

// TestS3Collector_PerBucketErrorIsolation verifies that when one
// region's CloudWatch call fails, only buckets in that region get an
// Error set, and Collect returns nil error as long as not all buckets
// failed.
func TestS3Collector_PerBucketErrorIsolation(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)

	goodSrv := fakeCloudWatchServer(t, map[string][]fakeMetricValue{
		"good-bucket/BucketSizeBytes": {{value: 42, ts: now}},
		"good-bucket/NumberOfObjects": {{value: 1, ts: now}},
	})
	defer goodSrv.Close()

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalError</Code><Message>boom</Message></Error></ErrorResponse>`) // best-effort: fixed test response body, write error not actionable in test server
	}))
	defer badSrv.Close()

	col := &S3Collector{
		Buckets: []S3Bucket{
			{Name: "good-bucket", Region: "us-east-1"},
			{Name: "bad-bucket", Region: "eu-west-1"},
		},
		Creds: Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		Now:   fixedNow(now),
		Endpoint: func(region string) string {
			if region == "us-east-1" {
				return goodSrv.URL
			}
			return badSrv.URL
		},
	}

	stats, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v, want nil (not all buckets failed)", err)
	}
	if len(stats) != 2 {
		t.Fatalf("len(stats) = %d, want 2", len(stats))
	}

	byName := map[string]models.BucketStats{}
	for _, s := range stats {
		byName[s.Bucket] = s
	}

	if byName["good-bucket"].Error != "" {
		t.Errorf("good-bucket Error = %q, want empty", byName["good-bucket"].Error)
	}
	if byName["bad-bucket"].Error == "" {
		t.Errorf("bad-bucket Error is empty, want an error message")
	}
}

// TestS3Collector_AllBucketsFailed verifies Collect returns a non-nil
// error when every configured bucket failed to collect.
func TestS3Collector_AllBucketsFailed(t *testing.T) {
	t.Parallel()

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `<ErrorResponse><Error><Code>InternalError</Code><Message>boom</Message></Error></ErrorResponse>`) // best-effort: fixed test response body, write error not actionable in test server
	}))
	defer badSrv.Close()

	col := &S3Collector{
		Buckets: []S3Bucket{{Name: "b1", Region: "us-east-1"}, {Name: "b2", Region: "us-east-1"}},
		Creds:   Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		Now:     fixedNow(time.Now()),
		Endpoint: func(region string) string {
			return badSrv.URL
		},
	}

	stats, err := col.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(stats) != 2 {
		t.Fatalf("len(stats) = %d, want 2 (partial results still returned)", len(stats))
	}
	for _, s := range stats {
		if s.Error == "" {
			t.Errorf("bucket %s Error = empty, want set", s.Bucket)
		}
	}
}

// TestS3Collector_RegionGrouping verifies one GetMetricData batch is
// issued per distinct region, not per bucket.
func TestS3Collector_RegionGrouping(t *testing.T) {
	t.Parallel()

	var callsByRegion = map[string]int{}
	mkServer := func(region string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callsByRegion[region]++
			w.Header().Set("Content-Type", "text/xml")
			_, _ = fmt.Fprint(w, `<GetMetricDataResponse><GetMetricDataResult><MetricDataResults></MetricDataResults></GetMetricDataResult></GetMetricDataResponse>`) // best-effort: fixed test response body, write error not actionable in test server
		}))
	}
	srvUS := mkServer("us-east-1")
	defer srvUS.Close()
	srvEU := mkServer("eu-west-1")
	defer srvEU.Close()

	col := &S3Collector{
		Buckets: []S3Bucket{
			{Name: "us-1", Region: "us-east-1"},
			{Name: "us-2", Region: "us-east-1"},
			{Name: "eu-1", Region: "eu-west-1"},
		},
		Creds: Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		Now:   fixedNow(time.Now()),
		Endpoint: func(region string) string {
			if region == "us-east-1" {
				return srvUS.URL
			}
			return srvEU.URL
		},
	}

	if _, err := col.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if callsByRegion["us-east-1"] != 1 {
		t.Errorf("us-east-1 calls = %d, want 1 (two buckets should share one batch)", callsByRegion["us-east-1"])
	}
	if callsByRegion["eu-west-1"] != 1 {
		t.Errorf("eu-west-1 calls = %d, want 1", callsByRegion["eu-west-1"])
	}
}
