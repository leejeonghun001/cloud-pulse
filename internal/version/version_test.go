package version

import "testing"

func TestString(t *testing.T) {
	t.Parallel()

	// Preserve and restore package vars since they are mutated by the
	// table-driven cases below.
	origVersion, origCommit, origDate := Version, Commit, Date
	t.Cleanup(func() {
		Version, Commit, Date = origVersion, origCommit, origDate
	})

	tests := []struct {
		name    string
		version string
		commit  string
		date    string
		want    string
	}{
		{
			name:    "defaults",
			version: "dev",
			commit:  "none",
			date:    "unknown",
			want:    "dev (none, unknown)",
		},
		{
			name:    "released build",
			version: "v1.2.3",
			commit:  "abc1234",
			date:    "2026-09-29T00:00:00Z",
			want:    "v1.2.3 (abc1234, 2026-09-29T00:00:00Z)",
		},
		{
			name:    "empty fields",
			version: "",
			commit:  "",
			date:    "",
			want:    " (, )",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			Version, Commit, Date = tt.version, tt.commit, tt.date

			got := String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
