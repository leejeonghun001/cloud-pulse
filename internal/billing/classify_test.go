package billing

import (
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		res  Result
		want models.CloudBillingStatus
	}{
		{
			name: "not_installed",
			res:  Result{NotFound: true},
			want: models.CloudBillingNotInstalled,
		},
		{
			name: "timeout",
			res:  Result{TimedOut: true, ExitCode: -1},
			want: models.CloudBillingError,
		},
		{
			name: "auth_failed_aws",
			res: Result{
				ExitCode: 254,
				Stderr:   []byte("Unable to locate credentials. You can configure credentials by running \"aws configure\"."),
			},
			want: models.CloudBillingAuthFailed,
		},
		{
			name: "auth_failed_oci",
			res: Result{
				ExitCode: 1,
				Stderr:   []byte("ServiceError: {'code': 'NotAuthenticated', 'message': 'The required information to complete authentication was not provided'}"),
			},
			want: models.CloudBillingAuthFailed,
		},
		{
			name: "permission_denied_aws",
			res: Result{
				ExitCode: 254,
				Stderr:   []byte("An error occurred (AccessDeniedException) when calling the GetCostAndUsage operation: User: arn:aws:iam::123456789012:user/bob is not authorized to perform: ce:GetCostAndUsage"),
			},
			want: models.CloudBillingPermissionDenied,
		},
		{
			name: "permission_denied_oci",
			res: Result{
				ExitCode: 1,
				Stderr:   []byte("ServiceError: {'status': 404, 'code': 'NotAuthorizedOrNotFound'}"),
			},
			want: models.CloudBillingPermissionDenied,
		},
		{
			name: "not_configured",
			res: Result{
				ExitCode: 1,
				Stderr:   []byte("You must specify a region. You can also configure your region by running \"aws configure\"."),
			},
			want: models.CloudBillingNotConfigured,
		},
		{
			name: "generic_error",
			res: Result{
				ExitCode: 1,
				Stderr:   []byte("some completely unrecognized failure text"),
			},
			want: models.CloudBillingError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(tt.res)
			if got.Status != tt.want {
				t.Errorf("classify(%+v).Status = %q, want %q", tt.res, got.Status, tt.want)
			}
			if got.Detail == "" {
				t.Error("classify returned an empty Detail")
			}
		})
	}
}

// TestClassify_NeverLeaksStderr verifies that Detail (the only field
// safe to log/persist) never contains the raw stderr text — only a
// fixed, secret-free description.
func TestClassify_NeverLeaksStderr(t *testing.T) {
	secretStderr := "AccessDeniedException: arn:aws:iam::999999999999:user/super-secret-username is not authorized"
	got := classify(Result{ExitCode: 254, Stderr: []byte(secretStderr)})
	if got.Detail == secretStderr {
		t.Fatal("classify's Detail echoed the raw stderr verbatim")
	}
	for _, forbidden := range []string{"999999999999", "super-secret-username"} {
		if contains := (func() bool {
			for i := 0; i+len(forbidden) <= len(got.Detail); i++ {
				if got.Detail[i:i+len(forbidden)] == forbidden {
					return true
				}
			}
			return false
		})(); contains {
			t.Errorf("classify's Detail leaked secret substring %q: %q", forbidden, got.Detail)
		}
	}
}
