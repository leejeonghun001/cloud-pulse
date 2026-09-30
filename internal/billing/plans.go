// Package billing implements SPEC-v0.6 §3's month-to-date/projected
// network egress cost estimation: pricing-plan validation, tiered cost
// calculation with same-plan pooling, and USD/KRW display conversion.
// Every function in this package is pure (no I/O, no clock reads beyond
// an explicit "now" parameter) so it can be table-driven tested without
// a database or HTTP server.
package billing

import (
	"fmt"
	"sort"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// OCI region groups used by the builtin default plans (migration
// 0005_v06.sql) and DefaultPlanProvider's OCI branch. These are NOT
// models.Provider values (that enum only has "oci", with no region
// split) — see notes/v06-prep.md's flag (c): the builtin plans store
// region-specific provider strings ("oci-na-eu"/"oci-apac") precisely so
// a host's default plan can depend on its OCI region.
const (
	// ProviderOCINAEU is the builtin plan provider string for OCI's
	// "Always Free" North America/Europe egress pricing.
	ProviderOCINAEU = "oci-na-eu"
	// ProviderOCIAPAC is the builtin plan provider string for OCI's
	// "Always Free" APAC/Japan/South America egress pricing.
	ProviderOCIAPAC = "oci-apac"
	// ProviderOther is the builtin plan provider string for the
	// zero-cost fallback plan.
	ProviderOther = "other"
)

// ociAPACRegionPrefixes lists OCI region-identifier prefixes billed
// under the APAC/Japan/South America Always Free egress rate rather
// than the North America/Europe rate. Sourced from Oracle's public
// region catalog (region identifiers, not display names); see
// notes/v06-network-cost.md for the citation. Matched by prefix so
// e.g. "ap-tokyo-1" and "ap-osaka-1" both match "ap-", and "sa-saopaulo-1"
// matches "sa-".
var ociAPACRegionPrefixes = []string{"ap-", "sa-", "me-", "il-", "af-"}

// DefaultPlanProvider resolves the builtin-plan "provider" string (see
// PricingPlan.Provider's doc comment — a free-form label, not
// models.Provider) to assign a host with no explicit HostPricing row,
// per SPEC-v0.6 §3: "aws→AWS, oci→OCI NA/EU, other→무료" plus this
// package's own OCI region-awareness (flagged as an open ambiguity by
// the prep stage). ociRegion is the OCI region identifier the host was
// detected in (e.g. from CP_OCI_TENANCY_ID's configured region, or ""
// if unknown/not applicable); an unknown or empty region defaults to the
// NA/EU rate, matching the builtin plan a plain "oci" host would have
// gotten before this package's region split existed.
func DefaultPlanProvider(p models.Provider, ociRegion string) string {
	switch p {
	case models.ProviderAWS:
		return string(models.ProviderAWS)
	case models.ProviderOCI:
		if ociRegionIsAPAC(ociRegion) {
			return ProviderOCIAPAC
		}
		return ProviderOCINAEU
	default:
		return ProviderOther
	}
}

// ociRegionIsAPAC reports whether region (an OCI region identifier, e.g.
// "ap-tokyo-1") falls under the APAC/Japan/South America Always Free
// rate per ociAPACRegionPrefixes. An empty or unrecognized region
// returns false (NA/EU default).
func ociRegionIsAPAC(region string) bool {
	for _, prefix := range ociAPACRegionPrefixes {
		if len(region) >= len(prefix) && region[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// ValidatePlan checks p's fields for SPEC-v0.6 §3's validation rules,
// returning a field-name -> message map (empty when p is valid):
//   - name must not be empty
//   - egress_free_gb must be non-negative
//   - ingress_price_per_gb must be non-negative
//   - egress_tiers must have at least one entry
//   - every tier's price_per_gb must be non-negative
//   - tiers must be strictly ascending by up_to_gb, EXCEPT the last
//     tier, which must have up_to_gb == 0 (unbounded); no earlier tier
//     may have up_to_gb == 0
//
// ValidatePlan does not check p.Builtin/p.ID — callers reject an
// attempt to create/update/delete a builtin plan before calling this
// (see IsBuiltinImmutable).
func ValidatePlan(p models.PricingPlan) map[string]string {
	details := make(map[string]string)

	if p.Name == "" {
		details["name"] = "must not be empty"
	}
	if len(p.Name) > 200 {
		details["name"] = "must be at most 200 characters"
	}
	if p.EgressFreeGB < 0 {
		details["egress_free_gb"] = "must be non-negative"
	}
	if p.IngressPricePerGB < 0 {
		details["ingress_price_per_gb"] = "must be non-negative"
	}

	if len(p.EgressTiers) == 0 {
		details["egress_tiers"] = "must have at least one tier"
		return details
	}

	prevUpTo := 0.0
	for i, t := range p.EgressTiers {
		if t.PricePerGB < 0 {
			details["egress_tiers"] = fmt.Sprintf("tier %d: price_per_gb must be non-negative", i)
			return details
		}
		last := i == len(p.EgressTiers)-1
		if t.UpToGB == 0 {
			if !last {
				details["egress_tiers"] = fmt.Sprintf("tier %d: only the last tier may be unbounded (up_to_gb=0)", i)
				return details
			}
			continue
		}
		if last {
			details["egress_tiers"] = "the last tier must be unbounded (up_to_gb=0)"
			return details
		}
		if t.UpToGB <= prevUpTo {
			details["egress_tiers"] = fmt.Sprintf("tier %d: up_to_gb must be strictly ascending", i)
			return details
		}
		prevUpTo = t.UpToGB
	}

	return details
}

// IsBuiltinImmutable reports whether p (an existing stored plan) must
// reject update/delete requests: true whenever p.Builtin is set, per
// SPEC-v0.6 §3 "내장본은 수정·삭제 불가" ("builtin plans cannot be
// modified or deleted").
func IsBuiltinImmutable(p models.PricingPlan) bool {
	return p.Builtin
}

// SortedTiers returns a copy of tiers sorted ascending by UpToGB, with
// any unbounded (UpToGB == 0) tier moved last. Used defensively by
// EstimateCost so a plan loaded from storage in an unexpected order
// still calculates correctly; ValidatePlan is what actually enforces
// correct ordering at write time.
func SortedTiers(tiers []models.PricingTier) []models.PricingTier {
	out := make([]models.PricingTier, len(tiers))
	copy(out, tiers)
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].UpToGB, out[j].UpToGB
		if ai == 0 {
			return false // unbounded never sorts before anything
		}
		if aj == 0 {
			return true
		}
		return ai < aj
	})
	return out
}
