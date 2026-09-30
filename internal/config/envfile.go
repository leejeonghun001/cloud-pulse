package config

import (
	"fmt"
	"strings"
)

// ParseEnvFile parses the KEY=VALUE lines in content into a map,
// following the same rules as the hub/agent installers' shell
// `load_env`/`.env`-style files (SPEC-v0.7 §1: macOS launchd and
// Windows services pass configuration via an explicit `--env-file`
// flag, since launchd has no `EnvironmentFile=` equivalent and neither
// does a Windows service).
//
// Rules, applied per line after splitting content on '\n' (a trailing
// '\r' from a CRLF line ending is stripped first, so files edited on
// Windows parse identically to Unix-style files):
//   - A line that is empty, or entirely whitespace, is skipped.
//   - A line whose first non-whitespace character is '#' is a comment
//     and is skipped in full (no leading-whitespace requirement — "#"
//     and "  # foo" are both comments).
//   - Every other line must contain a literal '=' splitting it into
//     KEY and VALUE at the first occurrence; KEY is trimmed of
//     surrounding whitespace, VALUE is used verbatim (not trimmed) so
//     a value with meaningful trailing/leading spaces round-trips —
//     matching a plain `KEY=VALUE` systemd/launchd env file, not a
//     shell script.
//   - KEY must be non-empty and match [A-Za-z_][A-Za-z0-9_]* (a valid
//     environment variable name); anything else is a parse error
//     naming the offending line number.
//   - There is deliberately no shell expansion (`$VAR`, “ `cmd` “,
//     `~`) and no quote interpretation: a VALUE of `"foo"` is stored
//     literally as the four-character string `"foo"`, including the
//     quote characters — callers that want quoted-value semantics must
//     strip them themselves. This matches systemd's EnvironmentFile
//     behavior (see systemd.exec(5)), not a shell's.
//   - A control character (any byte < 0x20 other than the line's own
//     trailing '\r'/'\n', or 0x7F) anywhere in the line is a parse
//     error naming the offending line number — this rejects a file
//     that isn't plain text (e.g. one that's actually binary, or
//     carries an embedded NUL from a truncated write).
//   - A duplicate KEY across multiple lines keeps the last occurrence
//     (matching shell/systemd override-on-redefine semantics), not an
//     error.
func ParseEnvFile(content string) (map[string]string, error) {
	out := make(map[string]string)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lineNum := i + 1
		line = strings.TrimSuffix(line, "\r")

		if err := checkNoControlChars(line, lineNum); err != nil {
			return nil, err
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("config: env file line %d: missing '=' (expected KEY=VALUE)", lineNum)
		}
		key := strings.TrimSpace(line[:eq])
		value := line[eq+1:]

		if !validEnvKey(key) {
			return nil, fmt.Errorf("config: env file line %d: invalid variable name %q", lineNum, key)
		}
		out[key] = value
	}
	return out, nil
}

// checkNoControlChars returns an error naming lineNum if line contains
// any ASCII control character (< 0x20 or == 0x7F). line has already had
// a trailing '\r' stripped by the caller, so a CRLF-terminated file's
// own line endings never trigger this check.
func checkNoControlChars(line string, lineNum int) error {
	for _, r := range line {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("config: env file line %d: contains a control character", lineNum)
		}
	}
	return nil
}

// validEnvKey reports whether key matches [A-Za-z_][A-Za-z0-9_]*, the
// same restriction POSIX places on environment variable names (and the
// hub/agent's own CP_* variables already satisfy).
func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
			// always valid, any position
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// LookupFuncFromEnvFile parses content via ParseEnvFile and returns a
// LookupFunc backed by the resulting map, suitable for passing directly
// to LoadAgent/LoadHub — this is the single implementation both the
// macOS launchd agent (`cloud-pulse-agent --env-file PATH`) and the
// Windows service agent use to read their configuration file, since
// neither platform's service manager supports systemd's
// `EnvironmentFile=` directive (SPEC-v0.7 §1).
func LookupFuncFromEnvFile(content string) (LookupFunc, error) {
	values, err := ParseEnvFile(content)
	if err != nil {
		return nil, err
	}
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}, nil
}
