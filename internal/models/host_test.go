package models

import "testing"

func TestValidHostID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
		want bool
	}{
		{"simple", "host-1", true},
		{"empty", "", false},
		{"dot_underscore_dash", "web.01_east-1", true},
		{"unicode", "hôst-1", false},
		{"space", "host 1", false},
		{"exactly_128", string(make([]byte, 128, 128)), false}, // NUL bytes, invalid runes
		{"128_valid_chars", repeatChar('a', 128), true},
		{"129_valid_chars", repeatChar('a', 129), false},
		{"single_char", "a", true},
		{"dots_only", "...", true}, // ValidHostID only checks charset, not trimming
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidHostID(tc.id); got != tc.want {
				t.Errorf("ValidHostID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

func repeatChar(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

func TestSanitizeHostID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"already_valid", "host-1.example", "host-1.example"},
		{"spaces", "my host name", "my-host-name"},
		{"unicode", "hôst-日本", "h-st"}, // matches the strings.Trim semantics below
		{"empty", "", "host"},
		{"only_invalid", "!!!", "host"},
		{"leading_trailing_dash", "-host-", "host"},
		{"leading_trailing_dot", ".host.", "host"},
		{"collapse_repeats", "host!!!name", "host-name"},
		{"200_chars", repeatChar('a', 200), repeatChar('a', 128)},
		{"dots_literal", "...", "host"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeHostID(tc.in)
			if got != tc.want {
				t.Errorf("SanitizeHostID(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if !ValidHostID(got) {
				t.Errorf("SanitizeHostID(%q) = %q is not a ValidHostID", tc.in, got)
			}
		})
	}
}

func TestSanitizeHostID_TruncatedResultStillValid(t *testing.T) {
	t.Parallel()
	in := repeatChar('a', 300)
	got := SanitizeHostID(in)
	if len(got) != 128 {
		t.Fatalf("len = %d, want 128", len(got))
	}
	if !ValidHostID(got) {
		t.Fatalf("SanitizeHostID(%q) = %q is not valid", in, got)
	}
}

func TestParseProvider(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      string
		want    Provider
		wantErr bool
	}{
		{"aws_lower", "aws", ProviderAWS, false},
		{"aws_upper", "AWS", ProviderAWS, false},
		{"oci_mixed", "Oci", ProviderOCI, false},
		{"other", "other", ProviderOther, false},
		{"spaces", "  aws  ", ProviderAWS, false},
		{"invalid", "gcp", "", true},
		{"empty", "", "", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseProvider(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseProvider(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("ParseProvider(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDefaultEgressLimit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		p    Provider
		want uint64
	}{
		{"aws", ProviderAWS, 100 * GiB},
		{"oci", ProviderOCI, 10 * TiB},
		{"other", ProviderOther, 0},
		{"unknown", Provider("gcp"), 0},
		{"empty", Provider(""), 0},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DefaultEgressLimit(tc.p); got != tc.want {
				t.Errorf("DefaultEgressLimit(%q) = %d, want %d", tc.p, got, tc.want)
			}
		})
	}
}
