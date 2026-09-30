package version

import (
	"regexp"
	"strconv"
	"strings"
)

// SelfUpdateSince is the first released version whose binaries include the
// `update` subcommand (see D-U1 in SPEC-v0.3.md). Releases before this tag
// have no self-update capability and must be upgraded via the install
// scripts instead.
const SelfUpdateSince = "v0.3.0"

// UnitManagedSince is the first released version whose binaries include
// the `systemd-unit` subcommand (see SPEC-v0.3.1 section B) and whose
// `update` subcommand's default PostUpdate hook is therefore safe to
// invoke: a release at or after this tag is guaranteed to have
// `systemd-unit apply` available, so updating *to* a tag >= this value
// may run it as part of `update`. Updating to a tag older than this
// (an explicit downgrade via `--version`) never attempts it, since the
// binary being installed wouldn't have the subcommand.
const UnitManagedSince = "v0.3.1"

// LaunchdManagedSince is the first released version whose binaries
// include the `plist` subcommand (SPEC-v0.7 §1) and whose `update`
// subcommand's default darwin PostUpdate hook is therefore safe to
// invoke: a release at or after this tag is guaranteed to have `plist
// apply` available. Mirrors UnitManagedSince's role for the Linux
// systemd-unit subcommand.
const LaunchdManagedSince = "v0.7.0"

// RemoteUpdatePlatformsSince is the first released version whose
// agents can report RemoteUpdateCapability.Platform and whose hub can
// therefore apply the per-OS remote-update version gate (SPEC-v0.7
// §6: "Linux >= v0.6.0, macOS/Windows >= v0.7.0") instead of the
// v0.6.0-only Linux gate.
const RemoteUpdatePlatformsSince = "v0.7.0"

// maxVersionNumber caps the numeric value accepted for a major/minor/patch
// component. It is well below the int32 range (~2.147e9) so that parsing a
// tag with an absurdly large numeric component can never overflow int on a
// 32-bit platform; such tags are simply treated as invalid rather than
// risking undefined/wrapped arithmetic.
const maxVersionNumber = 1_000_000_000

// devSuffixPattern matches git-describe "dev build" suffixes that must be
// rejected by Parse even though they otherwise look like a valid tag:
// "-<N>-g<hex>" (optionally followed by "-dirty"), e.g. "v0.2.0-3-gabc1234"
// or "v0.2.0-3-gabc1234-dirty".
var devSuffixPattern = regexp.MustCompile(`-\d+-g[0-9a-f]+(-dirty)?$`)

// validTagPattern is the syntax ValidTag enforces: a leading "v", three
// dot-separated numeric components, and an optional "-" prefixed
// pre-release consisting of alphanumerics, ".", and "-".
var validTagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// maxTagLen is the maximum length ValidTag accepts for a whole tag string.
const maxTagLen = 64

// SemVer is a parsed semantic version (see https://semver.org). Build
// metadata (a "+..." suffix) is not represented; cloud-pulse release tags
// never carry one, and Parse rejects tags that have one.
type SemVer struct {
	Major, Minor, Patch int
	Pre                 string // pre-release identifiers, e.g. "rc.1"; "" if none.
}

// Parse parses a version string of the form "vMAJOR.MINOR.PATCH" or
// "vMAJOR.MINOR.PATCH-PRERELEASE" (a leading "v" is required). It reports
// false for anything that isn't a clean release tag, including:
//
//   - the literal string "dev" (the default development build placeholder)
//   - git-describe "dev build" strings such as "v0.2.0-3-gabc1234",
//     "v0.2.0-dirty", or "v0.2.0-3-gabc1234-dirty"
//   - malformed strings (missing "v" prefix, non-numeric components, extra
//     dot-separated components, build-metadata "+" suffixes, etc.)
//   - strings where any numeric component would be implausibly huge
//     (> 1e9), to guard against overflow on 32-bit platforms
func Parse(s string) (SemVer, bool) {
	if s == "" || s == "dev" {
		return SemVer{}, false
	}
	if devSuffixPattern.MatchString(s) || strings.HasSuffix(s, "-dirty") {
		return SemVer{}, false
	}
	if !strings.HasPrefix(s, "v") {
		return SemVer{}, false
	}
	rest := s[1:]

	core := rest
	pre := ""
	if idx := strings.IndexByte(rest, '-'); idx >= 0 {
		core = rest[:idx]
		pre = rest[idx+1:]
		if pre == "" {
			// Trailing "-" with nothing after it.
			return SemVer{}, false
		}
	}
	if strings.Contains(core, "+") {
		return SemVer{}, false
	}

	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return SemVer{}, false
	}

	nums := make([]int, 3)
	for i, p := range parts {
		n, ok := parseVersionNumber(p)
		if !ok {
			return SemVer{}, false
		}
		nums[i] = n
	}

	if pre != "" && !validPreRelease(pre) {
		return SemVer{}, false
	}

	return SemVer{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre}, true
}

// parseVersionNumber parses a single numeric version component. It rejects
// empty strings, non-digit characters, leading zeros on multi-digit values
// (per semver 2.0's numeric identifier rule), and values above
// maxVersionNumber.
func parseVersionNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	if n > maxVersionNumber {
		return 0, false
	}
	return n, true
}

// validPreRelease reports whether a pre-release string is a sequence of
// dot-separated identifiers, each consisting only of ASCII alphanumerics
// and hyphens, none empty, per semver 2.0 §9. It does not itself reject the
// git-describe dev-build shapes; Parse checks those separately before ever
// reaching this function.
func validPreRelease(pre string) bool {
	for _, ident := range strings.Split(pre, ".") {
		if ident == "" {
			return false
		}
		for _, c := range ident {
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z':
			case c >= 'A' && c <= 'Z':
			case c == '-':
			default:
				return false
			}
		}
	}
	return true
}

// isNumericIdentifier reports whether a pre-release identifier consists
// only of digits (and is therefore compared numerically per semver 2.0,
// rather than lexically).
func isNumericIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Compare returns -1, 0, or +1 as a is less than, equal to, or greater than
// b, using semver 2.0 precedence rules: Major, Minor, Patch are compared
// numerically in that order; a version with a pre-release has lower
// precedence than the same Major.Minor.Patch without one; when both have
// a pre-release, identifiers are compared left to right — numeric
// identifiers are compared numerically and are always lower precedence
// than alphanumeric identifiers, alphanumeric identifiers are compared
// lexically in ASCII order, and a shorter identifier list that is
// otherwise equal has lower precedence.
func Compare(a, b SemVer) int {
	if c := compareInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := compareInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := compareInt(a.Patch, b.Patch); c != 0 {
		return c
	}
	return comparePreRelease(a.Pre, b.Pre)
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePreRelease implements semver 2.0 §11's pre-release precedence
// comparison between two pre-release strings (either may be "" to denote
// no pre-release, i.e. a plain release).
func comparePreRelease(a, b string) int {
	if a == "" && b == "" {
		return 0
	}
	if a == "" {
		return 1 // release > pre-release
	}
	if b == "" {
		return -1 // pre-release < release
	}

	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if c := compareIdentifier(aParts[i], bParts[i]); c != 0 {
			return c
		}
	}
	return compareInt(len(aParts), len(bParts))
}

// compareIdentifier compares a single pair of dot-separated pre-release
// identifiers per semver 2.0 §11.4.
func compareIdentifier(a, b string) int {
	aNum := isNumericIdentifier(a)
	bNum := isNumericIdentifier(b)

	switch {
	case aNum && bNum:
		// Numeric identifiers compare numerically. These come from
		// validated pre-release strings, so they always fit in an int
		// (bounded by realistic tag lengths); ignore the error.
		an, _ := strconv.Atoi(a)
		bn, _ := strconv.Atoi(b)
		return compareInt(an, bn)
	case aNum && !bNum:
		return -1 // numeric identifiers always have lower precedence
	case !aNum && bNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// AtLeastIncludingPrerelease reports whether candidate is at least gate,
// treating every valid prerelease of gate's numeric version as satisfying
// the gate. A capability introduced in vX.Y.Z is present in CI builds such
// as vX.Y.Z-ci.1 and release candidates such as vX.Y.Z-rc.1, even though
// ordinary SemVer precedence places those tags below vX.Y.Z. Invalid tags
// conservatively return false.
func AtLeastIncludingPrerelease(candidate, gate string) bool {
	c, ok := Parse(candidate)
	if !ok {
		return false
	}
	g, ok := Parse(gate)
	if !ok {
		return false
	}
	if g.Pre == "" {
		// SemVer's numeric prerelease identifier 0 is lower than every
		// valid non-empty prerelease identifier, and lower than the final
		// release, so vX.Y.Z-0 is the inclusive capability floor.
		g.Pre = "0"
	}
	return Compare(c, g) >= 0
}

// IsNewer reports whether candidate is a strictly newer version than
// current. Both strings must parse via Parse; if either does not parse,
// IsNewer returns false (an unparsable version is never considered "newer"
// — callers treat that as "no update information available", not as an
// update).
func IsNewer(candidate, current string) bool {
	c, ok := Parse(candidate)
	if !ok {
		return false
	}
	cur, ok := Parse(current)
	if !ok {
		return false
	}
	return Compare(c, cur) > 0
}

// ValidTag reports whether s is a syntactically valid release tag: a
// leading "v", three dot-separated numeric components, an optional
// "-"-prefixed pre-release made of alphanumerics/"."/"-", and a total
// length of at most 64 characters. It is intentionally stricter about
// syntax-only validation than Parse's semantic checks (e.g. it does not
// reject "-dirty"/git-describe suffixes, since those are still
// syntactically valid tag-shaped strings) — ValidTag exists to guard
// against URL/path injection when a tag string from an untrusted source
// (e.g. a GitHub redirect Location header) is used to build a download
// URL, not to decide whether a tag represents a clean release.
func ValidTag(s string) bool {
	if len(s) > maxTagLen {
		return false
	}
	return validTagPattern.MatchString(s)
}
