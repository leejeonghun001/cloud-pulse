package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// TestCloudInstanceID_DMIHit verifies the fast path: a Nitro-style
// board_asset_tag short-circuits before any HTTP call is attempted (the
// test's fake AWS/OCI servers would fail the test if hit, since they're
// never started).
func TestCloudInstanceID_DMIHit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("DMI detection is Linux-only")
	}

	var got string
	withCloudMetaOverrides(cloudMetaOverrides{
		readFile: func(path string) ([]byte, error) {
			if path == awsBoardAssetTagPath {
				return []byte("i-0123456789abcdef0\n"), nil
			}
			return nil, errNoSuchFile
		},
	}, func() {
		got = cloudInstanceID(context.Background(), models.ProviderAWS)
	})

	if got != "i-0123456789abcdef0" {
		t.Errorf("cloudInstanceID = %q, want i-0123456789abcdef0", got)
	}
}

// TestCloudInstanceID_DMIMissFallsThroughToIMDS verifies that an empty/
// missing DMI file falls through to the IMDSv2 HTTP path.
func TestCloudInstanceID_DMIMissFallsThroughToIMDS(t *testing.T) {

	var tokenRequested, idRequested bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			tokenRequested = true
			if r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "" {
				t.Errorf("token request missing TTL header")
			}
			_, _ = w.Write([]byte("fake-imds-token"))
		case r.Method == http.MethodGet && r.URL.Path == "/latest/meta-data/instance-id":
			idRequested = true
			if r.Header.Get("X-aws-ec2-metadata-token") != "fake-imds-token" {
				t.Errorf("instance-id request missing/wrong token header: %q", r.Header.Get("X-aws-ec2-metadata-token"))
			}
			_, _ = w.Write([]byte("i-fedcba9876543210\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var got string
	withCloudMetaOverrides(cloudMetaOverrides{
		readFile:   func(string) ([]byte, error) { return nil, errNoSuchFile },
		awsBaseURL: srv.URL,
		ociBaseURL: "http://127.0.0.1:1", // must never be hit
	}, func() {
		got = cloudInstanceID(context.Background(), models.ProviderAWS)
	})

	if !tokenRequested || !idRequested {
		t.Fatalf("IMDS not fully exercised: tokenRequested=%v idRequested=%v", tokenRequested, idRequested)
	}
	if got != "i-fedcba9876543210" {
		t.Errorf("cloudInstanceID = %q, want i-fedcba9876543210", got)
	}
}

// TestCloudInstanceID_FallsThroughToOCI verifies that a failed IMDS
// probe falls through to the OCI metadata v2 path.
func TestCloudInstanceID_FallsThroughToOCI(t *testing.T) {

	awsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no imds here", http.StatusNotFound)
	}))
	defer awsSrv.Close()

	var ociRequested bool
	ociSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/opc/v2/instance/" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer Oracle" {
			t.Errorf("OCI metadata request missing Authorization header: %q", r.Header.Get("Authorization"))
		}
		ociRequested = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "ocid1.instance.oc1..exampleuniqueID"})
	}))
	defer ociSrv.Close()

	var got string
	withCloudMetaOverrides(cloudMetaOverrides{
		readFile:   func(string) ([]byte, error) { return nil, errNoSuchFile },
		awsBaseURL: awsSrv.URL,
		ociBaseURL: ociSrv.URL,
	}, func() {
		got = cloudInstanceID(context.Background(), models.ProviderOCI)
	})

	if !ociRequested {
		t.Fatal("OCI metadata endpoint was never requested")
	}
	if got != "ocid1.instance.oc1..exampleuniqueID" {
		t.Errorf("cloudInstanceID = %q, want ocid1.instance.oc1..exampleuniqueID", got)
	}
}

// TestCloudInstanceID_AllStepsFail verifies "" is returned (never an
// error/panic) when DMI, IMDS, and OCI metadata all fail.
func TestCloudInstanceID_AllStepsFail(t *testing.T) {

	awsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer awsSrv.Close()
	ociSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer ociSrv.Close()

	var got string
	withCloudMetaOverrides(cloudMetaOverrides{
		readFile:   func(string) ([]byte, error) { return nil, errNoSuchFile },
		awsBaseURL: awsSrv.URL,
		ociBaseURL: ociSrv.URL,
	}, func() {
		got = cloudInstanceID(context.Background(), models.ProviderOther)
	})

	if got != "" {
		t.Errorf("cloudInstanceID = %q, want \"\" when every step fails", got)
	}
}

// TestCloudInstanceID_RespectsContextCancellation is a light sanity
// check that an already-canceled context never blocks or panics —
// every HTTP call inside cloudInstanceID must fail fast rather than
// hang.
func TestCloudInstanceID_RespectsContextCancellation(t *testing.T) {

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var got string
	withCloudMetaOverrides(cloudMetaOverrides{
		readFile:   func(string) ([]byte, error) { return nil, errNoSuchFile },
		awsBaseURL: "http://127.0.0.1:1",
		ociBaseURL: "http://127.0.0.1:1",
	}, func() {
		got = cloudInstanceID(ctx, models.ProviderAWS)
	})

	if got != "" {
		t.Errorf("cloudInstanceID with canceled context = %q, want \"\"", got)
	}
}

// TestCloudInstanceID_NonLinuxSkipsDMI documents that dmiInstanceID
// never attempts a file read on non-Linux, falling straight through to
// the HTTP-based steps.
func TestCloudInstanceID_NonLinuxSkipsDMI(t *testing.T) {

	if runtime.GOOS == "linux" {
		t.Skip("this test documents non-Linux behavior only")
	}

	called := false
	got := dmiInstanceID(cloudMetaOverrides{
		readFile: func(string) ([]byte, error) {
			called = true
			return []byte("i-shouldnotbeused"), nil
		},
	})
	if called {
		t.Error("dmiInstanceID called readFile on non-Linux")
	}
	if got != "" {
		t.Errorf("dmiInstanceID on non-Linux = %q, want \"\"", got)
	}
}

// errNoSuchFile is a stand-in os.ErrNotExist-shaped error for fake
// readFile implementations in these tests.
var errNoSuchFile = &fakeNotExistError{}

type fakeNotExistError struct{}

func (*fakeNotExistError) Error() string { return "no such file" }
