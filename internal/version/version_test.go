package version

import "testing"

func TestString(t *testing.T) {
	t.Parallel()
	if got, want := String(), Format(Version, Commit, Date); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestFormat(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, version, commit, date, want string }{
		{"defaults", "dev", "none", "unknown", "dev (none, unknown)"},
		{"released build", "v1.2.3", "abc1234", "2026-09-29T00:00:00Z", "v1.2.3 (abc1234, 2026-09-29T00:00:00Z)"},
		{"empty fields", "", "", "", " (, )"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Format(tt.version, tt.commit, tt.date); got != tt.want {
				t.Errorf("Format() = %q, want %q", got, tt.want)
			}
		})
	}
}
