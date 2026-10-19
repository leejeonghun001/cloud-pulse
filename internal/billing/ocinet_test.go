package billing

import (
	"context"
	"strings"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestCollectOCINetwork_SumsVNICMetricsForTheMonth(t *testing.T) {
	runner := &fakeRunner{Rules: map[string]Result{
		"oci monitoring metric-data summarize-metrics-data": {Stdout: []byte(`{"data":[{"aggregated-datapoints":[{"timestamp":"x","value":1.5e9},{"value":5e8}]}]}`)},
	}}
	got := CollectOCINetwork(context.Background(), runner, OCINetworkConfig{
		CompartmentID: "ocid1.compartment.oc1..c",
		VNICIDs:       []string{"ocid1.vnic.oc1..v"},
		HomeDir:       t.TempDir(),
		Auth:          "instance_principal",
	}, fixedNow("2026-10-19T05:30:00Z"))

	if got.Status != models.CloudBillingOK || got.Month != "2026-10" {
		t.Fatalf("usage = %+v, want ok for 2026-10", got)
	}
	if got.InBytes != 2e9 || got.OutBytes != 2e9 {
		t.Errorf("in/out = %d/%d, want 2e9 each (both metrics answered alike)", got.InBytes, got.OutBytes)
	}
	if len(runner.Calls) != 2 {
		t.Fatalf("calls = %d, want 2 (inbound + outbound)", len(runner.Calls))
	}
	args := strings.Join(runner.Calls[0].Args, " ")
	for _, want := range []string{
		`VnicFromNetworkBytes[1d]{resourceId = "ocid1.vnic.oc1..v"}.sum()`,
		"--start-time 2026-10-01T00:00:00Z", "--end-time 2026-10-19T06:00:00Z",
		"--namespace oci_vnic", "--auth instance_principal",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
}

func TestCollectOCINetwork_PermissionDeniedAndUnknownTarget(t *testing.T) {
	denied := &fakeRunner{Default: Result{ExitCode: 1, Stderr: []byte(`ServiceError: {"code": "NotAuthorizedOrNotFound"}`)}}
	got := CollectOCINetwork(context.Background(), denied, OCINetworkConfig{CompartmentID: "c", VNICIDs: []string{"v"}, HomeDir: t.TempDir()}, fixedNow("2026-10-19T00:00:00Z"))
	if got.Status != models.CloudBillingPermissionDenied {
		t.Errorf("status = %q, want permission_denied", got.Status)
	}

	none := &fakeRunner{}
	got = CollectOCINetwork(context.Background(), none, OCINetworkConfig{HomeDir: t.TempDir()}, fixedNow("2026-10-19T00:00:00Z"))
	if got.Status != models.CloudBillingNotConfigured || len(none.Calls) != 0 {
		t.Errorf("usage = %+v calls = %d, want not_configured without CLI calls", got, len(none.Calls))
	}
}
