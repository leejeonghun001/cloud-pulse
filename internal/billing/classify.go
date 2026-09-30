package billing

import (
	"strings"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// classification is the result of inspecting a CommandRunner Result:
// the models.CloudBillingStatus to record, plus a short secret-free
// StatusDetail string safe to persist/display. Callers must never pass
// the raw stderr bytes anywhere except into classify itself.
type classification struct {
	Status models.CloudBillingStatus
	Detail string
}

// authFailedPatterns/permissionDeniedPatterns are fixed, secret-free
// substrings matched against a CLI's stderr (case-sensitive — these are
// exact provider error-message fragments, not user input) to classify a
// nonzero exit without ever logging the stderr itself. Order matters:
// authFailedPatterns is checked first since some providers phrase
// "your credentials are missing/invalid" and "you lack permission for
// this specific action" differently and the more specific match should
// win when both could apply.
var authFailedPatterns = []string{
	// AWS CLI / botocore.
	"Unable to locate credentials",
	"UnrecognizedClientException",
	"InvalidClientTokenId",
	"ExpiredToken",
	"could not be found",    // "The config profile (foo) could not be found"
	"NoCredentialProviders", // legacy botocore
	// OCI CLI.
	"NotAuthenticated",
	"config file not found",
	"key file not found",
	"Invalid key_file path",
	"was not found in the config file",
}

var permissionDeniedPatterns = []string{
	// AWS CLI: ce:GetCostAndUsage/GetCostForecast denied by IAM policy.
	"AccessDeniedException",
	"is not authorized to perform",
	// OCI CLI: NotAuthorizedOrNotFound is OCI's deliberately-ambiguous
	// "either this doesn't exist or you can't see it" denial.
	"NotAuthorizedOrNotFound",
	"403",
}

// classify inspects res (a completed CommandRunner.Run result for an
// aws/oci billing CLI call) and returns the CloudBillingStatus/Detail to
// record. It never returns CloudBillingOK — callers set that themselves
// once they've also successfully parsed the JSON payload.
func classify(res Result) classification {
	switch {
	case res.NotFound:
		return classification{Status: models.CloudBillingNotInstalled, Detail: "CLI not found on PATH"}
	case res.TimedOut:
		return classification{Status: models.CloudBillingError, Detail: "CLI call timed out"}
	case res.ExitCode == 0:
		// Exit 0 but caller still needs to classify (e.g. JSON parse
		// failed) — treat as a generic error; callers only invoke
		// classify at all once they know something went wrong.
		return classification{Status: models.CloudBillingError, Detail: "unexpected output from CLI"}
	}

	stderr := string(res.Stderr)
	if containsAny(stderr, authFailedPatterns) {
		return classification{Status: models.CloudBillingAuthFailed, Detail: "CLI reported an authentication error"}
	}
	if containsAny(stderr, permissionDeniedPatterns) {
		return classification{Status: models.CloudBillingPermissionDenied, Detail: "CLI reported a permission error"}
	}
	if looksNotConfigured(stderr) {
		return classification{Status: models.CloudBillingNotConfigured, Detail: "CLI has no usable configuration"}
	}
	return classification{Status: models.CloudBillingError, Detail: "CLI exited with an error"}
}

// notConfiguredPatterns catches the "CLI is installed but has never been
// configured at all" case (no config file/profile of any kind), distinct
// from "configured with bad credentials" (authFailedPatterns, checked
// first by classify so a message that could arguably match both always
// resolves to auth_failed).
var notConfiguredPatterns = []string{
	"You must specify a region",
	"missing_tenancy",
	"tenancy OCID",
	"Missing config",
}

func looksNotConfigured(stderr string) bool {
	return containsAny(stderr, notConfiguredPatterns)
}

func containsAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
