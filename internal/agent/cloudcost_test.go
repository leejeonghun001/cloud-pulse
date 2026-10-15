package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/billing"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// fakeOCIRunner answers every CLI call with a fixed Result and records
// the argv it was given.
type fakeOCIRunner struct {
	res  billing.Result
	name string
	args []string
}

func (f *fakeOCIRunner) Run(_ context.Context, _ string, _ []string, name string, args ...string) (billing.Result, error) {
	f.name, f.args = name, args
	return f.res, nil
}

func TestOCICostCollector_SentOnceThenCleared(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var reports []models.AgentReport
	status := http.StatusInternalServerError
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rep models.AgentReport
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			t.Errorf("decode body: %v", err)
		}
		mu.Lock()
		reports = append(reports, rep)
		code := status
		mu.Unlock()
		w.WriteHeader(code)
	}))
	defer srv.Close()

	runner := &fakeOCIRunner{res: billing.Result{ExitCode: 0, Stdout: []byte(`{"data":[{"computedAmount":4,"resourceId":"ocid1.instance.oc1..a","currency":"USD"}]}`)}}
	cc := NewOCICostCollector(OCICostCollectorOptions{
		TenancyID: "ocid1.tenancy.oc1..t",
		Auth:      "instance_principal",
		Interval:  time.Hour,
		Runner:    runner,
		Now:       func() time.Time { return time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC) },
	})
	cc.CollectOnce(t.Context())
	if runner.name != "oci" || runner.args[len(runner.args)-1] != "instance_principal" {
		t.Fatalf("ran %q %v, want oci ... --auth instance_principal", runner.name, runner.args)
	}

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok", CloudCost: cc})
	flush := func() {
		r.Enqueue(models.Sample{Timestamp: 1})
		_ = r.Flush(t.Context(), models.HostInfo{ID: "h1"})
	}

	flush() // hub fails: the snapshot must stay pending
	mu.Lock()
	status = http.StatusOK
	mu.Unlock()
	flush() // accepted
	flush() // nothing new to send

	if len(reports) != 3 {
		t.Fatalf("reports = %d, want 3", len(reports))
	}
	for i, want := range []bool{true, true, false} {
		if got := reports[i].CloudCost != nil; got != want {
			t.Errorf("report %d carries cloud cost = %v, want %v", i, got, want)
		}
	}
	if c := reports[1].CloudCost; c.Provider != models.CloudBillingOCI || c.MTDCost != 4 {
		t.Errorf("cloud cost = %+v, want the collected OCI snapshot", c)
	}
}
