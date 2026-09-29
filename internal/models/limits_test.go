package models

import "testing"

func uint64Ptr(v uint64) *uint64 { return &v }

func TestEffectiveLimits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		agentLimit uint64
		limits     HostLimits
		wantTx     uint64
		wantRx     uint64
		wantTxSrc  string
		wantRxSrc  string
	}{
		{
			name:       "no_overrides_uses_agent_and_unlimited_rx",
			agentLimit: 100 * GiB,
			limits:     HostLimits{HostID: "h1"},
			wantTx:     100 * GiB,
			wantRx:     0,
			wantTxSrc:  "agent",
			wantRxSrc:  "none",
		},
		{
			name:       "hub_egress_override_nonzero",
			agentLimit: 100 * GiB,
			limits:     HostLimits{HostID: "h1", EgressLimitBytes: uint64Ptr(50 * GiB)},
			wantTx:     50 * GiB,
			wantRx:     0,
			wantTxSrc:  "hub",
			wantRxSrc:  "none",
		},
		{
			name:       "hub_egress_override_explicit_zero_unlimited",
			agentLimit: 100 * GiB,
			limits:     HostLimits{HostID: "h1", EgressLimitBytes: uint64Ptr(0)},
			wantTx:     0,
			wantRx:     0,
			wantTxSrc:  "hub",
			wantRxSrc:  "none",
		},
		{
			name:       "hub_ingress_override_nonzero",
			agentLimit: 100 * GiB,
			limits:     HostLimits{HostID: "h1", IngressLimitBytes: uint64Ptr(200 * GiB)},
			wantTx:     100 * GiB,
			wantRx:     200 * GiB,
			wantTxSrc:  "agent",
			wantRxSrc:  "hub",
		},
		{
			name:       "hub_ingress_override_explicit_zero_still_hub_source",
			agentLimit: 100 * GiB,
			limits:     HostLimits{HostID: "h1", IngressLimitBytes: uint64Ptr(0)},
			wantTx:     100 * GiB,
			wantRx:     0,
			wantTxSrc:  "agent",
			wantRxSrc:  "hub",
		},
		{
			name:       "both_overrides",
			agentLimit: 100 * GiB,
			limits: HostLimits{
				HostID:            "h1",
				EgressLimitBytes:  uint64Ptr(10 * GiB),
				IngressLimitBytes: uint64Ptr(20 * GiB),
			},
			wantTx:    10 * GiB,
			wantRx:    20 * GiB,
			wantTxSrc: "hub",
			wantRxSrc: "hub",
		},
		{
			name:       "agent_limit_zero_unlimited_no_override",
			agentLimit: 0,
			limits:     HostLimits{HostID: "h1"},
			wantTx:     0,
			wantRx:     0,
			wantTxSrc:  "agent",
			wantRxSrc:  "none",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tx, rx, txSrc, rxSrc := EffectiveLimits(tc.agentLimit, tc.limits)
			if tx != tc.wantTx {
				t.Errorf("tx = %d, want %d", tx, tc.wantTx)
			}
			if rx != tc.wantRx {
				t.Errorf("rx = %d, want %d", rx, tc.wantRx)
			}
			if txSrc != tc.wantTxSrc {
				t.Errorf("txSource = %q, want %q", txSrc, tc.wantTxSrc)
			}
			if rxSrc != tc.wantRxSrc {
				t.Errorf("rxSource = %q, want %q", rxSrc, tc.wantRxSrc)
			}
		})
	}
}

// TestEffectiveLimits_32BitSafe exercises values at and above the 32-bit
// boundary to ensure EffectiveLimits performs no platform-width-dependent
// arithmetic (it doesn't do arithmetic at all, but the values must survive
// the pointer round-trip unchanged on a 32-bit GOARCH build).
func TestEffectiveLimits_32BitSafe(t *testing.T) {
	t.Parallel()

	const large = uint64(1<<32) + 12345
	limits := HostLimits{
		HostID:            "h1",
		EgressLimitBytes:  uint64Ptr(large),
		IngressLimitBytes: uint64Ptr(large + 1),
	}
	tx, rx, txSrc, rxSrc := EffectiveLimits(large*2, limits)
	if tx != large {
		t.Errorf("tx = %d, want %d", tx, large)
	}
	if rx != large+1 {
		t.Errorf("rx = %d, want %d", rx, large+1)
	}
	if txSrc != "hub" || rxSrc != "hub" {
		t.Errorf("sources = %q/%q, want hub/hub", txSrc, rxSrc)
	}
}
