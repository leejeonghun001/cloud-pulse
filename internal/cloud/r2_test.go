package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// r2FakeRow is a helper for building fake operations/storage rows.
type r2OpRow struct {
	actionType string
	bucketName string
	requests   float64
}

type r2StorageRow struct {
	bucketName   string
	objectCount  float64
	payloadSize  float64
	metadataSize float64
}

// newR2FakeServer returns an httptest server that serves a fixed
// GraphQL response for any request whose Authorization header
// matches wantToken (or any token if wantToken is empty), reflecting
// the given operations/storage rows into all three aliased dataset
// queries (operationsMTD, operationsWindow use the same rows for
// simplicity in most tests; override via separate params when
// needed).
func newR2FakeServer(t *testing.T, wantToken string, mtd, window []r2OpRow, storage []r2StorageRow) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantToken != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+wantToken {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"unauthorized"}]}`)) // fixed test body, error ignorable
				return
			}
		}

		toGroups := func(rows []r2OpRow) []r2OperationsGroup {
			groups := make([]r2OperationsGroup, 0, len(rows))
			for _, row := range rows {
				groups = append(groups, r2OperationsGroup{
					Sum:        r2OperationsSum{Requests: row.requests},
					Dimensions: r2OperationsDimensions{ActionType: row.actionType, BucketName: row.bucketName},
				})
			}
			return groups
		}
		storageGroups := make([]r2StorageGroup, 0, len(storage))
		for _, row := range storage {
			storageGroups = append(storageGroups, r2StorageGroup{
				Max:        r2StorageMax{ObjectCount: row.objectCount, PayloadSize: row.payloadSize, MetadataSize: row.metadataSize},
				Dimensions: r2StorageDimensions{BucketName: row.bucketName},
			})
		}

		resp := r2GraphQLResponse{
			Data: r2GraphQLData{
				Viewer: r2Viewer{
					Accounts: []r2Account{{
						OperationsMTD:    toGroups(mtd),
						OperationsWindow: toGroups(window),
						Storage:          storageGroups,
					}},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp) // best-effort: fixed test payload, encode cannot fail
	}))
}

func TestR2Collector_Collect_Classification(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)

	mtd := []r2OpRow{
		{actionType: "PutObject", bucketName: "my-bucket", requests: 10},
		{actionType: "ListObjects", bucketName: "my-bucket", requests: 2},
		{actionType: "GetObject", bucketName: "my-bucket", requests: 100},
		{actionType: "DeleteObject", bucketName: "my-bucket", requests: 5}, // free, excluded
	}
	window := []r2OpRow{
		{actionType: "GetObject", bucketName: "my-bucket", requests: 7},
		{actionType: "PutObject", bucketName: "my-bucket", requests: 1},
	}
	storage := []r2StorageRow{
		{bucketName: "my-bucket", objectCount: 42, payloadSize: 1000, metadataSize: 40},
	}

	srv := newR2FakeServer(t, "", mtd, window, storage)
	defer srv.Close()

	col := &R2Collector{
		AccountID: "acct123",
		APIToken:  "test-token",
		Endpoint:  srv.URL,
		Now:       fixedNow(now),
		Window:    15 * time.Minute,
	}

	if got := col.Name(); got != "r2" {
		t.Fatalf("Name() = %q, want r2", got)
	}

	stats, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("len(stats) = %d, want 1", len(stats))
	}

	s := stats[0]
	if s.Bucket != "my-bucket" {
		t.Fatalf("Bucket = %q, want my-bucket", s.Bucket)
	}
	if s.Provider != models.StorageR2 {
		t.Errorf("Provider = %v, want r2", s.Provider)
	}
	if s.ClassAOpsMTD != 12 { // 10 Put + 2 List
		t.Errorf("ClassAOpsMTD = %d, want 12", s.ClassAOpsMTD)
	}
	if s.ClassBOpsMTD != 100 { // 100 Get
		t.Errorf("ClassBOpsMTD = %d, want 100", s.ClassBOpsMTD)
	}
	if s.RequestsWindow != 8 { // 7 Get + 1 Put in window
		t.Errorf("RequestsWindow = %d, want 8", s.RequestsWindow)
	}
	if s.SizeBytes != 1040 { // payloadSize + metadataSize
		t.Errorf("SizeBytes = %d, want 1040", s.SizeBytes)
	}
	if s.ObjectCount != 42 {
		t.Errorf("ObjectCount = %d, want 42", s.ObjectCount)
	}
	if s.WindowSeconds != int64((15 * time.Minute).Seconds()) {
		t.Errorf("WindowSeconds = %d, want 900", s.WindowSeconds)
	}
	if !s.RequestMetricsAvailable {
		t.Errorf("RequestMetricsAvailable = false, want true")
	}
	if s.CollectedAt != now.Unix() {
		t.Errorf("CollectedAt = %d, want %d", s.CollectedAt, now.Unix())
	}
}

func TestR2Collector_BucketFiltering(t *testing.T) {
	t.Parallel()

	mtd := []r2OpRow{
		{actionType: "PutObject", bucketName: "bucket-a", requests: 1},
		{actionType: "PutObject", bucketName: "bucket-b", requests: 2},
	}
	storage := []r2StorageRow{
		{bucketName: "bucket-a", objectCount: 1, payloadSize: 10},
		{bucketName: "bucket-b", objectCount: 2, payloadSize: 20},
	}

	srv := newR2FakeServer(t, "", mtd, nil, storage)
	defer srv.Close()

	col := &R2Collector{
		AccountID: "acct123",
		APIToken:  "test-token",
		Buckets:   []string{"bucket-a"},
		Endpoint:  srv.URL,
		Now:       fixedNow(time.Now()),
	}

	stats, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(stats) != 1 || stats[0].Bucket != "bucket-a" {
		t.Fatalf("stats = %+v, want only bucket-a", stats)
	}
}

func TestR2Collector_DiscoversBucketsWhenUnfiltered(t *testing.T) {
	t.Parallel()

	mtd := []r2OpRow{
		{actionType: "PutObject", bucketName: "bucket-a", requests: 1},
		{actionType: "PutObject", bucketName: "bucket-b", requests: 2},
	}

	srv := newR2FakeServer(t, "", mtd, nil, nil)
	defer srv.Close()

	col := &R2Collector{
		AccountID: "acct123",
		APIToken:  "test-token",
		Endpoint:  srv.URL,
		Now:       fixedNow(time.Now()),
	}

	stats, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("len(stats) = %d, want 2 (buckets discovered from response)", len(stats))
	}
}

func TestR2Collector_GraphQLErrorsArray(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":{"viewer":{"accounts":[]}},"errors":[{"message":"rate limited"}]}`) // best-effort: fixed test response body, write error not actionable in test server
	}))
	defer srv.Close()

	col := &R2Collector{
		AccountID: "acct123",
		APIToken:  "test-token",
		Endpoint:  srv.URL,
		Now:       fixedNow(time.Now()),
	}

	_, err := col.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("err = %v, want it to mention 'rate limited'", err)
	}
}

func TestR2Collector_Unauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`) // best-effort: fixed test response body, write error not actionable in test server
	}))
	defer srv.Close()

	col := &R2Collector{
		AccountID: "acct123",
		APIToken:  "wrong-token",
		Endpoint:  srv.URL,
		Now:       fixedNow(time.Now()),
	}

	_, err := col.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want it to mention status 401", err)
	}
	// The API token must never be echoed back in the error text.
	if strings.Contains(err.Error(), "wrong-token") {
		t.Errorf("err leaked the API token: %v", err)
	}
}

func TestR2Collector_BearerTokenSent(t *testing.T) {
	t.Parallel()

	srv := newR2FakeServer(t, "secret-token", nil, nil, nil)
	defer srv.Close()

	col := &R2Collector{
		AccountID: "acct123",
		APIToken:  "secret-token",
		Endpoint:  srv.URL,
		Now:       fixedNow(time.Now()),
	}

	if _, err := col.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
}
