// notifyverifyprint.go implements the text (non-JSON) output format for
// `cloud-pulse-hub notify verify`.
package main

import (
	"fmt"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// printNotifyVerifyText prints report in a human-readable, secret-free
// form (every field of models.NotifyVerifyResult is guaranteed secret-
// free by construction — see its doc comment).
func printNotifyVerifyText(report models.NotifyVerifyReport) {
	for _, r := range report.Results {
		switch r.Status {
		case models.NotifyVerifySkipped:
			fmt.Printf("%s: skipped (%s)\n", r.Platform, r.Detail)
		case models.NotifyVerifyVerified:
			fmt.Printf("%s: VERIFIED (%s)\n", r.Platform, r.Detail)
		case models.NotifyVerifyAccepted:
			fmt.Printf("%s: accepted (%s)\n", r.Platform, r.Detail)
		case models.NotifyVerifyFailed:
			fmt.Printf("%s: FAILED - %s\n", r.Platform, r.Detail)
			if r.Diagnosis != nil {
				fmt.Printf("  code: %s\n", r.Diagnosis.Code)
				if r.Diagnosis.Hint != "" {
					fmt.Printf("  hint: %s\n", r.Diagnosis.Hint)
				}
			}
		default:
			fmt.Printf("%s: %s (%s)\n", r.Platform, r.Status, r.Detail)
		}
		if r.Cleaned != nil {
			if *r.Cleaned {
				fmt.Printf("  cleanup: deleted\n")
			} else {
				fmt.Printf("  cleanup: not completed\n")
			}
		}
	}
	if report.OK {
		fmt.Println("overall: OK")
	} else {
		fmt.Println("overall: FAILED")
	}
}
