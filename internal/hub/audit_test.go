package hub

import "testing"

// TestAuditValuesEquivalent_IgnoresBookkeepingTimestamps is a deterministic
// regression test for a flaky no-op save: the store refreshes updated_at
// on every write, so a save landing on a different second than the seed
// must still count as a no-op.
func TestAuditValuesEquivalent_IgnoresBookkeepingTimestamps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		before, after string
		want          bool
	}{
		{"identical", `{"id":1,"name":"a"}`, `{"id":1,"name":"a"}`, true},
		{"only updated_at differs", `{"id":1,"name":"a","updated_at":100}`, `{"id":1,"name":"a","updated_at":101}`, true},
		{"only created_at differs", `{"id":1,"created_at":1}`, `{"id":1,"created_at":2}`, true},
		{"content differs", `{"id":1,"price":0.09,"updated_at":100}`, `{"id":1,"price":0.085,"updated_at":101}`, false},
		{"field added", `{"id":1}`, `{"id":1,"name":"a"}`, false},
		{"non-object values", `"a"`, `"b"`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := auditValuesEquivalent(tc.before, tc.after); got != tc.want {
				t.Fatalf("auditValuesEquivalent(%s, %s) = %v, want %v", tc.before, tc.after, got, tc.want)
			}
		})
	}
}
