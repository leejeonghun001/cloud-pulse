package hub

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// passwordHashIterations is the PBKDF2 iteration count used for every
// newly stored password hash (measured ~186ms on arm64, ~1.05s on
// armv7; see SPEC-v0.4 §1).
const passwordHashIterations = 600_000

// passwordSaltBytes is the length of the random salt stored in each new
// password hash.
const passwordSaltBytes = 16

// passwordKeyBytes is the length of the derived key stored in each new
// password hash.
const passwordKeyBytes = 32

// passwordHashPrefix identifies the pbkdf2-sha256 hash format stored in
// the "auth_password_hash" setting: "pbkdf2-sha256$<iter>$<salt
// b64raw>$<hash b64raw>".
const passwordHashPrefix = "pbkdf2-sha256"

// defaultPassword is the bootstrap admin password set by
// EnsureDefaultCredentials on a fresh hub.
const defaultPassword = "changeme"

// minPasswordLen and maxPasswordLen bound accepted password lengths (in
// bytes, per SPEC-v0.4 §1).
const (
	minPasswordLen = 8
	maxPasswordLen = 1024
)

// HashPassword derives a pbkdf2-sha256 hash of password with a fresh
// random salt, returning the encoded form stored in the
// "auth_password_hash" setting. It is exported for cmd/hub's
// reset-password subcommand; internal callers within this package use
// the lowercase hashPassword directly.
func HashPassword(password string) (string, error) {
	return hashPassword(password)
}

// VerifyPassword reports whether password matches encoded (the stored
// "auth_password_hash" value). It is exported for cmd/hub's
// reset-password subcommand and its tests; internal callers within
// this package use the lowercase verifyPassword directly.
func VerifyPassword(password, encoded string) bool {
	return verifyPassword(password, encoded)
}

// ValidatePassword enforces SPEC-v0.4 §1's password policy (see
// validatePassword). It is exported for cmd/hub's reset-password
// subcommand.
func ValidatePassword(candidate, current string) error {
	return validatePassword(candidate, current)
}

// hashPassword derives a pbkdf2-sha256 hash of password with a fresh
// random salt, returning the encoded form stored in the
// "auth_password_hash" setting.
func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("hub: generate password salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordHashIterations, passwordKeyBytes)
	if err != nil {
		return "", fmt.Errorf("hub: derive password key: %w", err)
	}
	encoded := fmt.Sprintf("%s$%d$%s$%s",
		passwordHashPrefix,
		passwordHashIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
	return encoded, nil
}

// verifyPassword reports whether password matches encoded (the stored
// "auth_password_hash" value), in constant time with respect to the
// comparison itself. It always runs the full PBKDF2 computation, even
// when encoded is malformed, so that callers who run this once per
// login attempt (including for a nonexistent/wrong username) don't leak
// timing information through a fast-path early return. A malformed
// encoded hash reports false (with a synthetic PBKDF2 computation still
// performed) rather than an error, since the caller's response to the
// client is identical either way ("invalid credentials").
func verifyPassword(password, encoded string) bool {
	iterations, salt, key, ok := parsePasswordHash(encoded)
	if !ok {
		// Malformed/missing stored hash: still perform an equivalent
		// PBKDF2 computation against fixed parameters so this call
		// takes roughly the same time as the valid-hash path, then
		// report false.
		_, _ = pbkdf2.Key(sha256.New, password, make([]byte, passwordSaltBytes), passwordHashIterations, passwordKeyBytes)
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(key))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, key) == 1
}

// parsePasswordHash decodes an encoded "pbkdf2-sha256$<iter>$<salt
// b64raw>$<hash b64raw>" string.
func parsePasswordHash(encoded string) (iterations int, salt, key []byte, ok bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordHashPrefix {
		return 0, nil, nil, false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return 0, nil, nil, false
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return 0, nil, nil, false
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return 0, nil, nil, false
	}
	return iterations, salt, key, true
}

// passwordPolicyError is returned by validatePassword when a candidate
// password fails the policy; its message is suitable to return directly
// to the client alongside code "weak_password".
type passwordPolicyError struct {
	msg string
}

func (e *passwordPolicyError) Error() string { return e.msg }

// validatePassword enforces SPEC-v0.4 §1's password policy: 8..1024
// bytes, not all whitespace, not "changeme", and (when current is
// non-empty) different from the current password.
func validatePassword(candidate, current string) error {
	if len(candidate) < minPasswordLen {
		return &passwordPolicyError{msg: fmt.Sprintf("password must be at least %d characters", minPasswordLen)}
	}
	if len(candidate) > maxPasswordLen {
		return &passwordPolicyError{msg: fmt.Sprintf("password must be at most %d characters", maxPasswordLen)}
	}
	if isAllWhitespace(candidate) {
		return &passwordPolicyError{msg: "password must not be all whitespace"}
	}
	if candidate == defaultPassword {
		return &passwordPolicyError{msg: `password must not be "changeme"`}
	}
	if current != "" && candidate == current {
		return &passwordPolicyError{msg: "new password must be different from the current password"}
	}
	return nil
}

// isAllWhitespace reports whether s consists entirely of whitespace
// (including the empty string).
func isAllWhitespace(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
