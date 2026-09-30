package billing

import (
	"context"
	"testing"

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
