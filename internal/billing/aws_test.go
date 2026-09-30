package billing

import (
	"context"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func fixedNow(rfc3339 string) time.Time {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestCollectAWS_OK(t *testing.T) {
	now := fixedNow("2026-09-15T10:00:00Z")
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage": {
				ExitCode: 0,
				Stdout:   []byte(`{"ResultsByTime":[{"Total":{"UnblendedCost":{"Amount":"123.45","Unit":"USD"}}}]}`),
			},
			"aws ce get-cost-forecast": {
				ExitCode: 0,
				Stdout:   []byte(`{"Total":{"Amount":"250.00","Unit":"USD"},"ForecastResultsByTime":[]}`),
			},
		},
	}

	snap := CollectAWS(context.Background(), runner, AWSConfig{HomeDir: t.TempDir()}, now)

	if snap.Status != models.CloudBillingOK {
		t.Fatalf("Status = %q, want ok", snap.Status)
	}
	if snap.MTDCost != 123.45 {
		t.Errorf("MTDCost = %v, want 123.45", snap.MTDCost)
	}
	if snap.ForecastCost != 250.00 {
		t.Errorf("ForecastCost = %v, want 250.00", snap.ForecastCost)
	}
	if snap.ForecastMethod != "api" {
		t.Errorf("ForecastMethod = %q, want api", snap.ForecastMethod)
	}
	if snap.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", snap.Currency)
	}
	if !snap.AccountLevel {
		t.Error("AccountLevel = false, want true (resources disabled)")
	}
}

// TestCollectAWS_SkipsForecastOnLastDayOfMonth verifies SPEC-v0.6 §1's
// "월 마지막 날에는 호출하지 않는다" rule: no get-cost-forecast call is
// made, and the forecast falls back to a linear projection of MTD.
func TestCollectAWS_SkipsForecastOnLastDayOfMonth(t *testing.T) {
	now := fixedNow("2026-09-30T23:00:00Z") // September has 30 days
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage": {
				ExitCode: 0,
				Stdout:   []byte(`{"ResultsByTime":[{"Total":{"UnblendedCost":{"Amount":"300.00","Unit":"USD"}}}]}`),
			},
			"aws ce get-cost-forecast": {
				ExitCode: 0,
				Stdout:   []byte(`{"Total":{"Amount":"999.00","Unit":"USD"}}`),
			},
		},
	}

	snap := CollectAWS(context.Background(), runner, AWSConfig{HomeDir: t.TempDir()}, now)

	for _, c := range runner.Calls {
		if c.Name == "aws" && len(c.Args) > 1 && c.Args[1] == "get-cost-forecast" {
			t.Fatalf("get-cost-forecast was called on the last day of the month: %+v", c)
		}
	}
	if snap.ForecastMethod != "linear" {
		t.Errorf("ForecastMethod = %q, want linear", snap.ForecastMethod)
	}
	if snap.ForecastCost != snap.MTDCost {
		t.Errorf("ForecastCost = %v, want equal to MTDCost (%v) on the last day", snap.ForecastCost, snap.MTDCost)
	}
}

// TestCollectAWS_ForecastFailureFallsBackToLinear verifies that a
// forecast-call failure doesn't fail the whole snapshot: MTD still
// succeeds and a linear projection substitutes for the forecast.
func TestCollectAWS_ForecastFailureFallsBackToLinear(t *testing.T) {
	now := fixedNow("2026-09-11T00:00:00Z") // 11 days into a 30-day month
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage": {
				ExitCode: 0,
				Stdout:   []byte(`{"ResultsByTime":[{"Total":{"UnblendedCost":{"Amount":"110.00","Unit":"USD"}}}]}`),
			},
			"aws ce get-cost-forecast": {
				ExitCode: 1,
				Stderr:   []byte("DataUnavailableException: The requested data is unavailable."),
			},
		},
	}

	snap := CollectAWS(context.Background(), runner, AWSConfig{HomeDir: t.TempDir()}, now)

	if snap.Status != models.CloudBillingOK {
		t.Fatalf("Status = %q, want ok (MTD alone should still succeed)", snap.Status)
	}
	if snap.ForecastMethod != "linear" {
		t.Errorf("ForecastMethod = %q, want linear", snap.ForecastMethod)
	}
	// 110 / 11 days * 30 days = 300
	if got, want := snap.ForecastCost, 300.0; got != want {
		t.Errorf("ForecastCost = %v, want %v", got, want)
	}
}

func TestCollectAWS_NotInstalled(t *testing.T) {
	runner := &fakeRunner{Default: Result{NotFound: true}}
	snap := CollectAWS(context.Background(), runner, AWSConfig{HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))
	if snap.Status != models.CloudBillingNotInstalled {
		t.Errorf("Status = %q, want not_installed", snap.Status)
	}
}

func TestCollectAWS_AuthFailed(t *testing.T) {
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage": {
				ExitCode: 254,
				Stderr:   []byte("Unable to locate credentials"),
			},
		},
	}
	snap := CollectAWS(context.Background(), runner, AWSConfig{HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))
	if snap.Status != models.CloudBillingAuthFailed {
		t.Errorf("Status = %q, want auth_failed", snap.Status)
	}
}

func TestCollectAWS_PermissionDenied(t *testing.T) {
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage": {
				ExitCode: 254,
				Stderr:   []byte("An error occurred (AccessDeniedException) when calling the GetCostAndUsage operation"),
			},
		},
	}
	snap := CollectAWS(context.Background(), runner, AWSConfig{HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))
	if snap.Status != models.CloudBillingPermissionDenied {
		t.Errorf("Status = %q, want permission_denied", snap.Status)
	}
}

func TestCollectAWS_Timeout(t *testing.T) {
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage": {TimedOut: true, ExitCode: -1},
		},
	}
	snap := CollectAWS(context.Background(), runner, AWSConfig{HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))
	if snap.Status != models.CloudBillingError {
		t.Errorf("Status = %q, want error (timeout classification)", snap.Status)
	}
}

// TestCollectAWS_ResourcesEnabled verifies per-resource matching and
// that the total number of CLI invocations is bounded (MTD + forecast +
// resources = 3 calls, not unbounded/looped).
func TestCollectAWS_ResourcesEnabled(t *testing.T) {
	now := fixedNow("2026-09-15T00:00:00Z")
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage-with-resources": {
				ExitCode: 0,
				Stdout: []byte(`{"ResultsByTime":[{"Groups":[
					{"Keys":["i-aaa"],"Metrics":{"UnblendedCost":{"Amount":"10.00","Unit":"USD"}}},
					{"Keys":["i-bbb"],"Metrics":{"UnblendedCost":{"Amount":"20.00","Unit":"USD"}}}
				]}]}`),
			},
			"aws ce get-cost-and-usage": {
				ExitCode: 0,
				Stdout:   []byte(`{"ResultsByTime":[{"Total":{"UnblendedCost":{"Amount":"30.00","Unit":"USD"}}}]}`),
			},
			"aws ce get-cost-forecast": {
				ExitCode: 0,
				Stdout:   []byte(`{"Total":{"Amount":"60.00","Unit":"USD"}}`),
			},
		},
	}

	snap := CollectAWS(context.Background(), runner, AWSConfig{HomeDir: t.TempDir(), Resources: true}, now)

	if len(runner.Calls) != 3 {
		t.Fatalf("len(Calls) = %d, want exactly 3 (MTD + forecast + resources)", len(runner.Calls))
	}
	if snap.AccountLevel {
		t.Error("AccountLevel = true, want false (per-resource data present)")
	}
	if snap.PerResource["i-aaa"] != 10.00 || snap.PerResource["i-bbb"] != 20.00 {
		t.Errorf("PerResource = %+v, want i-aaa=10, i-bbb=20", snap.PerResource)
	}
}

// TestCollectAWS_CallCountBounded verifies that a run without resources
// enabled makes exactly 2 calls (MTD + forecast), never more.
func TestCollectAWS_CallCountBounded(t *testing.T) {
	runner := &fakeRunner{
		Rules: map[string]Result{
			"aws ce get-cost-and-usage": {
				ExitCode: 0,
				Stdout:   []byte(`{"ResultsByTime":[{"Total":{"UnblendedCost":{"Amount":"1.00","Unit":"USD"}}}]}`),
			},
			"aws ce get-cost-forecast": {
				ExitCode: 0,
				Stdout:   []byte(`{"Total":{"Amount":"2.00","Unit":"USD"}}`),
			},
		},
	}
	CollectAWS(context.Background(), runner, AWSConfig{HomeDir: t.TempDir()}, fixedNow("2026-09-15T00:00:00Z"))
	if len(runner.Calls) != 2 {
		t.Fatalf("len(Calls) = %d, want exactly 2", len(runner.Calls))
	}
}

func TestBuildAWSEnv(t *testing.T) {
	env := BuildAWSEnv("/tmp/home", "/usr/bin:/bin", "AKIA...", "secret", "token", "myprofile", "/cfg", "/creds")
	want := map[string]bool{
		"HOME=/tmp/home":                     true,
		"PATH=/usr/bin:/bin":                 true,
		"AWS_ACCESS_KEY_ID=AKIA...":          true,
		"AWS_SECRET_ACCESS_KEY=secret":       true,
		"AWS_SESSION_TOKEN=token":            true,
		"AWS_PROFILE=myprofile":              true,
		"AWS_CONFIG_FILE=/cfg":               true,
		"AWS_SHARED_CREDENTIALS_FILE=/creds": true,
	}
	if len(env) != len(want) {
		t.Fatalf("len(env) = %d, want %d; env=%v", len(env), len(want), env)
	}
	for _, e := range env {
		if !want[e] {
			t.Errorf("unexpected env entry %q", e)
		}
	}
}

func TestBuildAWSEnv_OmitsUnsetOptionalFields(t *testing.T) {
	env := BuildAWSEnv("/tmp/home", "/usr/bin", "", "", "", "", "", "")
	if len(env) != 2 {
		t.Fatalf("BuildAWSEnv with no optional fields = %v, want only HOME+PATH", env)
	}
}
