package version

import (
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want SemVer
		ok   bool
	}{
		{"basic release", "v1.2.3", SemVer{1, 2, 3, ""}, true},
		{"zero version", "v0.0.0", SemVer{0, 0, 0, ""}, true},
		{"single-digit components", "v1.0.0", SemVer{1, 0, 0, ""}, true},
		{"multi-digit components", "v12.34.56", SemVer{12, 34, 56, ""}, true},
		{"pre-release simple", "v1.2.3-rc.1", SemVer{1, 2, 3, "rc.1"}, true},
		{"pre-release alpha", "v1.0.0-alpha", SemVer{1, 0, 0, "alpha"}, true},
		{"pre-release alpha dot beta", "v1.0.0-alpha.beta", SemVer{1, 0, 0, "alpha.beta"}, true},
		{"pre-release numeric identifier", "v1.0.0-alpha.1", SemVer{1, 0, 0, "alpha.1"}, true},
		{"pre-release multiple numeric", "v1.0.0-0.3.7", SemVer{1, 0, 0, "0.3.7"}, true},
		{"pre-release x-y-z hyphen identifier", "v1.0.0-x-y-z.--", SemVer{1, 0, 0, "x-y-z.--"}, true},
		{"our own release tag", "v0.2.0", SemVer{0, 2, 0, ""}, true},

		// dev / non-release placeholders.
		{"literal dev", "dev", SemVer{}, false},
		{"empty string", "", SemVer{}, false},

		// git-describe dev builds must be rejected.
		{"git describe dev", "v0.2.0-3-gabc1234", SemVer{}, false},
		{"git describe dev short hash", "v0.2.0-1-ga1b2c3d", SemVer{}, false},
		{"dirty suffix only", "v0.2.0-dirty", SemVer{}, false},
		{"git describe dev dirty", "v0.2.0-3-gabc1234-dirty", SemVer{}, false},
		{"git describe dev big count", "v1.10.0-123-gdeadbeef", SemVer{}, false},
		{"dirty suffix with pre-release-looking text", "v1.2.3-rc.1-dirty", SemVer{}, false},

		// malformed.
		{"missing v prefix", "1.2.3", SemVer{}, false},
		{"uppercase V prefix", "V1.2.3", SemVer{}, false},
		{"missing patch", "v1.2", SemVer{}, false},
		{"too many components", "v1.2.3.4", SemVer{}, false},
		{"non-numeric major", "va.2.3", SemVer{}, false},
		{"non-numeric minor", "v1.b.3", SemVer{}, false},
		{"non-numeric patch", "v1.2.c", SemVer{}, false},
		{"leading zero major", "v01.2.3", SemVer{}, false},
		{"leading zero minor", "v1.02.3", SemVer{}, false},
		{"leading zero patch", "v1.2.03", SemVer{}, false},
		{"trailing dash no pre-release", "v1.2.3-", SemVer{}, false},
		{"build metadata plus", "v1.2.3+build.5", SemVer{}, false},
		{"pre-release with plus", "v1.2.3-rc.1+build.5", SemVer{}, false},
		{"empty pre-release identifier", "v1.2.3-rc..1", SemVer{}, false},
		{"pre-release invalid char underscore", "v1.2.3-rc_1", SemVer{}, false},
		{"pre-release invalid char space", "v1.2.3-rc 1", SemVer{}, false},
		{"whitespace only", "   ", SemVer{}, false},
		{"leading whitespace", " v1.2.3", SemVer{}, false},
		{"trailing whitespace", "v1.2.3 ", SemVer{}, false},
		{"just v", "v", SemVer{}, false},
		{"negative major", "v-1.2.3", SemVer{}, false},

		// huge numbers must not panic on 32-bit; treated as invalid.
		{"huge major", "v99999999999.0.0", SemVer{}, false},
		{"huge minor", "v1.99999999999.0", SemVer{}, false},
		{"huge patch", "v1.0.99999999999", SemVer{}, false},
		{"just over threshold", "v1000000001.0.0", SemVer{}, false},
		{"exactly at threshold", "v1000000000.0.0", SemVer{1_000_000_000, 0, 0, ""}, true},
		{"int32 max plus one", "v2147483648.0.0", SemVer{}, false},
		{"way beyond int64", "v99999999999999999999999999.0.0", SemVer{}, false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := Parse(tt.in)
			if ok != tt.ok {
				t.Fatalf("Parse(%q) ok = %v, want %v (got %+v)", tt.in, ok, tt.ok, got)
			}
			if ok && got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParse_NeverPanics(t *testing.T) {
	t.Parallel()

	// A defensive sweep of adversarial inputs that must never panic,
	// regardless of platform int width.
	inputs := []string{
		"",
		"v",
		"v.",
		"v..",
		"v1..3",
		"v1.2.",
		strRepeat("v", 1000),
		"v" + strRepeat("9", 1000) + ".0.0",
		"v0.0.0-" + strRepeat("a.", 500),
		"v9223372036854775808.0.0", // > math.MaxInt64
		"v-0.-0.-0",
		"v+.+.+",
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Parse(%q) panicked: %v", in, r)
				}
			}()
			Parse(in)
		}()
	}
}

func strRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func TestCompare(t *testing.T) {
	t.Parallel()

	must := func(s string) SemVer {
		v, ok := Parse(s)
		if !ok {
			t.Fatalf("test setup: Parse(%q) failed", s)
		}
		return v
	}

	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"equal", "v1.2.3", "v1.2.3", 0},
		{"major greater", "v2.0.0", "v1.9.9", 1},
		{"major less", "v1.9.9", "v2.0.0", -1},
		{"minor greater", "v1.3.0", "v1.2.9", 1},
		{"minor less", "v1.2.9", "v1.3.0", -1},
		{"patch greater", "v1.2.4", "v1.2.3", 1},
		{"patch less", "v1.2.3", "v1.2.4", -1},

		// semver 2.0 spec's own canonical precedence example (§11):
		// 1.0.0-alpha < 1.0.0-alpha.1 < 1.0.0-alpha.beta < 1.0.0-beta <
		// 1.0.0-beta.2 < 1.0.0-beta.11 < 1.0.0-rc.1 < 1.0.0
		{"alpha < alpha.1", "v1.0.0-alpha", "v1.0.0-alpha.1", -1},
		{"alpha.1 < alpha.beta", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", -1},
		{"alpha.beta < beta", "v1.0.0-alpha.beta", "v1.0.0-beta", -1},
		{"beta < beta.2", "v1.0.0-beta", "v1.0.0-beta.2", -1},
		{"beta.2 < beta.11", "v1.0.0-beta.2", "v1.0.0-beta.11", -1},
		{"beta.11 < rc.1", "v1.0.0-beta.11", "v1.0.0-rc.1", -1},
		{"rc.1 < release", "v1.0.0-rc.1", "v1.0.0", -1},

		// reverse direction of the same chain.
		{"release > rc.1", "v1.0.0", "v1.0.0-rc.1", 1},
		{"alpha.1 > alpha", "v1.0.0-alpha.1", "v1.0.0-alpha", 1},

		// pre-release vs release at equal core version.
		{"pre-release less than release", "v1.0.0-alpha", "v1.0.0", -1},
		{"release greater than pre-release", "v1.0.0", "v1.0.0-alpha", 1},

		// numeric identifiers compare numerically, not lexically
		// ("11" > "2" numerically despite "11" < "2" lexically).
		{"numeric identifier 2 vs 11", "v1.0.0-beta.2", "v1.0.0-beta.11", -1},

		// shorter identifier list with equal leading identifiers has
		// lower precedence.
		{"shorter pre-release list is lower", "v1.0.0-alpha", "v1.0.0-alpha.1", -1},

		{"equal pre-release", "v1.0.0-rc.1", "v1.0.0-rc.1", 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, b := must(tt.a), must(tt.b)
			got := Compare(a, b)
			if sign(got) != sign(tt.want) {
				t.Errorf("Compare(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
			}

			// Compare must be antisymmetric.
			gotRev := Compare(b, a)
			if sign(gotRev) != -sign(tt.want) {
				t.Errorf("Compare(%q, %q) = %d, want sign %d (antisymmetric check)", tt.b, tt.a, gotRev, -tt.want)
			}
		})
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

func TestIsNewer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		candidate string
		current   string
		want      bool
	}{
		{"newer patch", "v0.2.1", "v0.2.0", true},
		{"newer minor", "v0.3.0", "v0.2.0", true},
		{"newer major", "v1.0.0", "v0.2.0", true},
		{"equal", "v0.2.0", "v0.2.0", false},
		{"older", "v0.1.0", "v0.2.0", false},
		{"candidate unparsable dev", "dev", "v0.2.0", false},
		{"current unparsable dev", "v0.3.0", "dev", false},
		{"both unparsable", "dev", "dev", false},
		{"candidate git-describe dev string", "v0.3.0-3-gabc1234", "v0.2.0", false},
		{"current git-describe dev string", "v0.3.0", "v0.2.0-3-gabc1234", false},
		{"candidate dirty", "v0.3.0-dirty", "v0.2.0", false},
		{"candidate malformed", "not-a-version", "v0.2.0", false},
		{"current malformed", "v0.3.0", "not-a-version", false},
		{"release newer than candidate pre-release of next version", "v0.3.0-rc.1", "v0.2.0", true},
		{"pre-release of same version is not newer than release", "v0.2.0-rc.1", "v0.2.0", false},
		{"huge candidate number treated unparsable", "v99999999999.0.0", "v0.2.0", false},
		{"empty candidate", "", "v0.2.0", false},
		{"empty current", "v0.2.0", "", false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := IsNewer(tt.candidate, tt.current)
			if got != tt.want {
				t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.candidate, tt.current, got, tt.want)
			}
		})
	}
}

func TestValidTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"basic", "v1.2.3", true},
		{"zero", "v0.0.0", true},
		{"pre-release", "v1.2.3-rc.1", true},
		{"our release tag", "v0.2.0", true},
		{"multi-digit", "v12.34.56", true},
		{"pre-release with hyphen and dots", "v1.2.3-alpha.1-beta", true},

		// ValidTag is deliberately looser than Parse: it does not
		// reject "dev", "dirty", or git-describe suffixes, since those
		// are still syntax-safe for the URL-building use case it guards.
		{"dirty suffix is syntactically valid", "v0.2.0-dirty", true},
		{"git describe dev is syntactically valid", "v0.2.0-3-gabc1234", true},

		{"missing v", "1.2.3", false},
		{"uppercase V", "V1.2.3", false},
		{"missing patch", "v1.2", false},
		{"too many components", "v1.2.3.4", false},
		{"non-numeric", "va.b.c", false},
		{"empty", "", false},
		{"just v", "v", false},
		{"path traversal attempt", "v1.2.3/../../etc/passwd", false},
		{"embedded url", "v1.2.3-http://evil.example", false},
		{"query string injection", "v1.2.3?evil=1", false},
		{"space", "v1.2.3 ", false},
		{"newline", "v1.2.3\n", false},
		{"null byte", "v1.2.3\x00", false},

		// length boundary: regex-valid but too long overall.
		{"exactly 64 chars", "v1.2.3-" + strRepeat("a", 57), true},              // 7 + 57 = 64
		{"65 chars, one over the limit", "v1.2.3-" + strRepeat("a", 58), false}, // 7 + 58 = 65
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ValidTag(tt.in); got != tt.want {
				t.Errorf("ValidTag(%q) = %v, want %v (len=%d)", tt.in, got, tt.want, len(tt.in))
			}
		})
	}
}

func TestSelfUpdateSince(t *testing.T) {
	t.Parallel()

	if SelfUpdateSince != "v0.3.0" {
		t.Fatalf("SelfUpdateSince = %q, want %q", SelfUpdateSince, "v0.3.0")
	}
	if !ValidTag(SelfUpdateSince) {
		t.Errorf("SelfUpdateSince %q must be a valid tag", SelfUpdateSince)
	}
	if _, ok := Parse(SelfUpdateSince); !ok {
		t.Errorf("SelfUpdateSince %q must parse", SelfUpdateSince)
	}

	// Sanity-check the constant's intended use: a v0.3.0 agent/hub is
	// self-update-capable, a v0.2.0 one is not.
	if !IsNewer("v0.3.0", "v0.2.0") {
		t.Errorf("IsNewer(%q, %q) = false, want true", "v0.3.0", "v0.2.0")
	}
}
