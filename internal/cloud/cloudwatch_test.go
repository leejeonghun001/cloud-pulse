package cloud

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fixedNow returns a fixed clock function for deterministic tests.
func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// TestCloudWatch_GetMetricData_FormAndSignature verifies the POST
// form parameters and that a signature header is present.
func TestCloudWatch_GetMetricData_FormAndSignature(t *testing.T) {
	t.Parallel()

	var gotBody string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body) // test server: best-effort read, ContentLength is exact here
		gotBody = string(body)
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/xml")
		_, _ = fmt.Fprint(w, `<GetMetricDataResponse><GetMetricDataResult>
			<MetricDataResults>
				<member>
					<Id>q1</Id>
					<StatusCode>Complete</StatusCode>
					<Timestamps><member>2024-01-01T00:00:00Z</member></Timestamps>
					<Values><member>42</member></Values>
				</member>
			</MetricDataResults>
		</GetMetricDataResult></GetMetricDataResponse>`) // best-effort: fixed test response body, write error not actionable in test server
	}))
	defer srv.Close()

	cw := &CloudWatch{
		Region:   "us-east-1",
		Creds:    Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		Endpoint: srv.URL,
		Now:      fixedNow(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)),
	}

	queries := []MetricQuery{{
		ID: "q1",
		Metric: Metric{
			Namespace:  "AWS/S3",
			MetricName: "BucketSizeBytes",
			Dimensions: []Dimension{{Name: "BucketName", Value: "my-bucket"}},
		},
		Period: 86400,
		Stat:   "Average",
	}}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	results, err := cw.GetMetricData(context.Background(), queries, start, end)
	if err != nil {
		t.Fatalf("GetMetricData: %v", err)
	}

	if gotAuth == "" || !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("missing/invalid Authorization header: %q", gotAuth)
	}
	for _, want := range []string{
		"Action=GetMetricData",
		"Version=2010-08-01",
		"MetricDataQueries.member.1.Id=q1",
		"MetricDataQueries.member.1.MetricStat.Metric.Namespace=AWS%2FS3",
		"MetricDataQueries.member.1.MetricStat.Metric.MetricName=BucketSizeBytes",
		"MetricDataQueries.member.1.MetricStat.Metric.Dimensions.member.1.Name=BucketName",
		"MetricDataQueries.member.1.MetricStat.Metric.Dimensions.member.1.Value=my-bucket",
		"MetricDataQueries.member.1.MetricStat.Period=86400",
		"MetricDataQueries.member.1.MetricStat.Stat=Average",
		"MetricDataQueries.member.1.ReturnData=true",
		"ScanBy=TimestampDescending",
	} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("form body missing %q; body=%s", want, gotBody)
		}
	}

	dps, ok := results["q1"]
	if !ok || len(dps) != 1 {
		t.Fatalf("results[q1] = %+v, want one datapoint", dps)
	}
	if dps[0].Value != 42 {
		t.Errorf("value = %v, want 42", dps[0].Value)
	}
}

// TestCloudWatch_GetMetricData_Pagination verifies NextToken-based
// pagination is followed until exhausted, merging all pages.
func TestCloudWatch_GetMetricData_Pagination(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/xml")
		if n == 1 {
			_, _ = fmt.Fprint(w, `<GetMetricDataResponse><GetMetricDataResult>
				<MetricDataResults>
					<member>
						<Id>q1</Id>
						<Timestamps><member>2024-01-01T00:00:00Z</member></Timestamps>
						<Values><member>1</member></Values>
					</member>
				</MetricDataResults>
				<NextToken>page2</NextToken>
			</GetMetricDataResult></GetMetricDataResponse>`) // best-effort: fixed test response body, write error not actionable in test server
			return
		}
		_, _ = fmt.Fprint(w, `<GetMetricDataResponse><GetMetricDataResult>
			<MetricDataResults>
				<member>
					<Id>q1</Id>
					<Timestamps><member>2024-01-02T00:00:00Z</member></Timestamps>
					<Values><member>2</member></Values>
				</member>
			</MetricDataResults>
		</GetMetricDataResult></GetMetricDataResponse>`) // best-effort: fixed test response body, write error not actionable in test server
	}))
	defer srv.Close()

	cw := &CloudWatch{
		Region:   "us-east-1",
		Creds:    Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		Endpoint: srv.URL,
		Now:      fixedNow(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)),
	}

	queries := []MetricQuery{{ID: "q1", Metric: Metric{Namespace: "AWS/S3", MetricName: "NumberOfObjects"}, Period: 86400, Stat: "Sum"}}
	results, err := cw.GetMetricData(context.Background(), queries, time.Now(), time.Now())
	if err != nil {
		t.Fatalf("GetMetricData: %v", err)
	}

	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (pagination not followed)", calls.Load())
	}
	if len(results["q1"]) != 2 {
		t.Fatalf("results[q1] len = %d, want 2", len(results["q1"]))
	}
}

// TestCloudWatch_GetMetricData_ErrorResponse verifies XML
// ErrorResponse bodies are decoded into *APIError.
func TestCloudWatch_GetMetricData_ErrorResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `<ErrorResponse><Error><Code>InvalidParameterValue</Code><Message>bad param</Message></Error></ErrorResponse>`) // best-effort: fixed test response body, write error not actionable in test server
	}))
	defer srv.Close()

	cw := &CloudWatch{
		Region:   "us-east-1",
		Creds:    Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		Endpoint: srv.URL,
		Now:      fixedNow(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)),
	}

	queries := []MetricQuery{{ID: "q1", Metric: Metric{Namespace: "AWS/S3", MetricName: "NumberOfObjects"}, Period: 86400, Stat: "Sum"}}
	_, err := cw.GetMetricData(context.Background(), queries, time.Now(), time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Code != "InvalidParameterValue" || apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("got %+v", apiErr)
	}
}

// TestCloudWatch_GetMetricData_Chunking verifies queries are chunked
// into batches of at most 500 per call.
func TestCloudWatch_GetMetricData_Chunking(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/xml")
		_, _ = fmt.Fprint(w, `<GetMetricDataResponse><GetMetricDataResult><MetricDataResults></MetricDataResults></GetMetricDataResult></GetMetricDataResponse>`) // best-effort: fixed test response body, write error not actionable in test server
	}))
	defer srv.Close()

	cw := &CloudWatch{
		Region:   "us-east-1",
		Creds:    Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		Endpoint: srv.URL,
		Now:      fixedNow(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)),
	}

	queries := make([]MetricQuery, 501)
	for i := range queries {
		queries[i] = MetricQuery{ID: fmt.Sprintf("q%d", i), Metric: Metric{Namespace: "AWS/S3", MetricName: "NumberOfObjects"}, Period: 86400, Stat: "Sum"}
	}

	if _, err := cw.GetMetricData(context.Background(), queries, time.Now(), time.Now()); err != nil {
		t.Fatalf("GetMetricData: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (501 queries should chunk into 500+1)", calls.Load())
	}
}
