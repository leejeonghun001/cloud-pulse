package billing

import (
	"context"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestCollectOCI_OK(t *testing.T) {
	now := fixedNow("2026-09-11T00:00:00Z") // 11 of 30 days
	runner := &fakeRunner{
		Rules: map[string]Result{
			"oci usage-api usage-summary request-summarized-usages": {
				ExitCode: 0,
				Stdout: []byte(`{"data":[
					{"computedAmount":5.5,"resourceId":"ocid1.instance.oc1..aaa","currency":"USD"},
					{"computedAmount":4.5,"resourceId":"ocid1.instance.oc1..bbb","currency":"USD"}
				]}`),
			},
		},
	}

	snap := CollectOCI(context.Background(), runner, OCIConfig{TenancyID: "ocid1.tenancy.oc1..xyz", HomeDir: t.TempDir()}, now)

	if snap.Status != models.CloudBillingOK {
		t.Fatalf("Status = %q, want ok", snap.Status)
	}
	if snap.MTDCost != 10.0 {
		t.Errorf("MTDCost = %v, want 10.0", snap.MTDCost)
	}
	if snap.ForecastMethod != "linear" {
		t.Errorf("ForecastMethod = %q, want linear (OCI has no forecast API)", snap.ForecastMethod)
	}
	// 10 / 11 days * 30 days ≈ 27.2727...
	if got, want := snap.ForecastCost, 10.0/11*30; abs(got-want) > 1e-9 {
		t.Errorf("ForecastCost = %v, want %v", got, want)
	}
	if snap.AccountLevel {
		t.Error("AccountLevel = true, want false (per-resource data present)")
	}
	if snap.PerResource["ocid1.instance.oc1..aaa"] != 5.5 {
		t.Errorf("PerResource[aaa] = %v, want 5.5", snap.PerResource["ocid1.instance.oc1..aaa"])
	}
}

func TestCollectOCI_NoTenancyIsNotConfigured(t *testing.T) {
	runner := &fakeRunner{}
	snap := CollectOCI(context.Background(), runner, OCIConfig{HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))
	if snap.Status != models.CloudBillingNotConfigured {
		t.Errorf("Status = %q, want not_configured", snap.Status)
	}
	if len(runner.Calls) != 0 {
		t.Errorf("Calls = %v, want no CLI call when tenancy is unset", runner.Calls)
	}
}

func TestCollectOCI_NotInstalled(t *testing.T) {
	runner := &fakeRunner{Default: Result{NotFound: true}}
	snap := CollectOCI(context.Background(), runner, OCIConfig{TenancyID: "ocid1.tenancy.oc1..xyz", HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))
	if snap.Status != models.CloudBillingNotInstalled {
		t.Errorf("Status = %q, want not_installed", snap.Status)
	}
}

func TestCollectOCI_PermissionDenied(t *testing.T) {
	runner := &fakeRunner{
		Rules: map[string]Result{
			"oci usage-api usage-summary request-summarized-usages": {
				ExitCode: 1,
				Stderr:   []byte("ServiceError: {'status': 404, 'code': 'NotAuthorizedOrNotFound', 'message': 'Authorization failed or requested resource not found.'}"),
			},
		},
	}
	snap := CollectOCI(context.Background(), runner, OCIConfig{TenancyID: "ocid1.tenancy.oc1..xyz", HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))
	if snap.Status != models.CloudBillingPermissionDenied {
		t.Errorf("Status = %q, want permission_denied", snap.Status)
	}
}

func TestCollectOCI_Timeout(t *testing.T) {
	runner := &fakeRunner{
		Rules: map[string]Result{
			"oci usage-api usage-summary request-summarized-usages": {TimedOut: true, ExitCode: -1},
		},
	}
	snap := CollectOCI(context.Background(), runner, OCIConfig{TenancyID: "ocid1.tenancy.oc1..xyz", HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))
	if snap.Status != models.CloudBillingError {
		t.Errorf("Status = %q, want error (timeout classification)", snap.Status)
	}
}

func TestCollectOCI_CallCountBounded(t *testing.T) {
	runner := &fakeRunner{
		Rules: map[string]Result{
			"oci usage-api usage-summary request-summarized-usages": {
				ExitCode: 0,
				Stdout:   []byte(`{"data":[]}`),
			},
		},
	}
	CollectOCI(context.Background(), runner, OCIConfig{TenancyID: "ocid1.tenancy.oc1..xyz", HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))
	if len(runner.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want exactly 1 (OCI has no forecast call)", len(runner.Calls))
	}
}

func TestBuildOCIEnv(t *testing.T) {
	env := BuildOCIEnv("/tmp/home", "/usr/bin", "/cfg", "myprofile")
	want := []string{"HOME=/tmp/home", "PATH=/usr/bin", "OCI_CLI_CONFIG_FILE=/cfg", "OCI_CLI_PROFILE=myprofile"}
	if len(env) != len(want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}

func TestBuildOCIEnv_OmitsUnsetOptionalFields(t *testing.T) {
	env := BuildOCIEnv("/tmp/home", "/usr/bin", "", "")
	if len(env) != 2 {
		t.Fatalf("env = %v, want only HOME+PATH", env)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestCollectOCI_InstancePrincipalAuthAndCLIPath(t *testing.T) {
	runner := &fakeRunner{Default: Result{ExitCode: 0, Stdout: []byte(`{"data":[]}`)}}
	snap := CollectOCI(context.Background(), runner, OCIConfig{
		TenancyID: "ocid1.tenancy.oc1..xyz",
		HomeDir:   t.TempDir(),
		Auth:      "instance_principal",
		CLI:       "/opt/oci-cli/bin/oci",
	}, fixedNow("2026-09-15T00:00:00Z"))
	if snap.Status != models.CloudBillingOK {
		t.Fatalf("Status = %q, want ok", snap.Status)
	}
	if len(runner.Calls) != 1 {
		t.Fatalf("Calls = %d, want 1", len(runner.Calls))
	}
	call := runner.Calls[0]
	if call.Name != "/opt/oci-cli/bin/oci" {
		t.Errorf("Name = %q, want the configured CLI path", call.Name)
	}
	n := len(call.Args)
	if n < 2 || call.Args[n-2] != "--auth" || call.Args[n-1] != "instance_principal" {
		t.Errorf("Args = %v, want trailing --auth instance_principal", call.Args)
	}
}

func TestCollectOCI_DayBoundsAreUTCMidnightOnNonUTCHost(t *testing.T) {
	kst := time.FixedZone("KST", 9*60*60)
	// 2026-10-01 05:00 KST is still 2026-09-30 in UTC.
	now := time.Date(2026, 10, 1, 5, 0, 0, 0, kst)
	runner := &fakeRunner{Default: Result{ExitCode: 0, Stdout: []byte(`{"data":[]}`)}}
	CollectOCI(context.Background(), runner, OCIConfig{TenancyID: "ocid1.tenancy.oc1..xyz", HomeDir: t.TempDir()}, now)

	args := runner.Calls[0].Args
	flag := func(name string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == name {
				return args[i+1]
			}
		}
		return ""
	}
	if got := flag("--time-usage-started"); got != "2026-09-01T00:00:00Z" {
		t.Errorf("--time-usage-started = %q, want 2026-09-01T00:00:00Z", got)
	}
	if got := flag("--time-usage-ended"); got != "2026-10-01T00:00:00Z" {
		t.Errorf("--time-usage-ended = %q, want 2026-10-01T00:00:00Z", got)
	}
}

// TestCollectOCI_CLIKebabCaseOutput uses the shape the real `oci` CLI
// prints: a "data" object with "items" and kebab-case keys, including
// rows without a resource ID.
func TestCollectOCI_CLIKebabCaseOutput(t *testing.T) {
	runner := &fakeRunner{Default: Result{ExitCode: 0, Stdout: []byte(`{"data":{"group-by":["resourceId"],"items":[
		{"computed-amount":2.5,"resource-id":"ocid1.instance.oc1..a","currency":"SGD"},
		{"computed-amount":1.25,"resource-id":"ocid1.instance.oc1..a","currency":"SGD"},
		{"computed-amount":0.75,"resource-id":null,"currency":"SGD"},
		{"computed-amount":null,"resource-id":"ocid1.bootvolume.oc1..b","currency":"SGD"}
	]}}`)}}
	snap := CollectOCI(context.Background(), runner, OCIConfig{TenancyID: "ocid1.tenancy.oc1..xyz", HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))

	if snap.Status != models.CloudBillingOK {
		t.Fatalf("Status = %q, want ok", snap.Status)
	}
	if snap.MTDCost != 4.5 || snap.Currency != "SGD" {
		t.Errorf("MTDCost/Currency = %v %q, want 4.5 SGD", snap.MTDCost, snap.Currency)
	}
	if snap.PerResource["ocid1.instance.oc1..a"] != 3.75 {
		t.Errorf("PerResource[a] = %v, want 3.75", snap.PerResource["ocid1.instance.oc1..a"])
	}
}
