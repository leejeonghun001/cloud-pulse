package hub

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	t.Parallel()
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") {
		t.Errorf("hash = %q, want pbkdf2-sha256$ prefix", hash)
	}
	if !verifyPassword("correct horse battery staple", hash) {
		t.Error("verifyPassword: correct password rejected")
	}
	if verifyPassword("wrong password", hash) {
		t.Error("verifyPassword: wrong password accepted")
	}
}

func TestHashPassword_UniqueSaltPerCall(t *testing.T) {
	t.Parallel()
	h1, err := hashPassword("same-password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	h2, err := hashPassword("same-password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if h1 == h2 {
		t.Error("two hashes of the same password with fresh salts must differ")
	}
	if !verifyPassword("same-password", h1) || !verifyPassword("same-password", h2) {
		t.Error("both hashes must still verify the original password")
	}
}

func TestVerifyPassword_MalformedHashNeverPanics(t *testing.T) {
	t.Parallel()
	cases := []string{"", "not-a-hash", "pbkdf2-sha256$abc$x$y", "pbkdf2-sha256$1$$"}
	for _, malformed := range cases {
		malformed := malformed
		t.Run(malformed, func(t *testing.T) {
			t.Parallel()
			if verifyPassword("anything", malformed) {
				t.Error("verifyPassword must return false for a malformed hash")
			}
		})
	}
}

func TestParsePasswordHash_RoundTrip(t *testing.T) {
	t.Parallel()
	hash, err := hashPassword("hello world")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	iter, salt, key, ok := parsePasswordHash(hash)
	if !ok {
		t.Fatalf("parsePasswordHash: ok=false for %q", hash)
	}
	if iter != passwordHashIterations {
		t.Errorf("iterations = %d, want %d", iter, passwordHashIterations)
	}
	if len(salt) != passwordSaltBytes {
		t.Errorf("salt length = %d, want %d", len(salt), passwordSaltBytes)
	}
	if len(key) != passwordKeyBytes {
		t.Errorf("key length = %d, want %d", len(key), passwordKeyBytes)
	}
}

func TestValidatePassword_Policy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		candidate string
		current   string
		wantErr   bool
	}{
		{"too_short", "short12", "", true},
		{"min_length_ok", "12345678", "", false},
		{"too_long", strings.Repeat("a", 1025), "", true},
		{"max_length_ok", strings.Repeat("a", 1024), "", false},
		{"all_whitespace", "        ", "", true},
		{"whitespace_and_letters_ok", "  abcdef  ", "", false},
		{"is_changeme", "changeme", "", true},
		{"same_as_current", "same-password-1", "same-password-1", true},
		{"different_from_current", "new-password-1", "old-password-1", false},
		{"valid", "a-perfectly-fine-password", "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validatePassword(tc.candidate, tc.current)
			if (err != nil) != tc.wantErr {
				t.Errorf("validatePassword(%q, %q) error = %v, wantErr %v", tc.candidate, tc.current, err, tc.wantErr)
			}
		})
	}
}

func TestHashPassword_ExportedWrapper(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("some-password-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !verifyPassword("some-password-123", hash) {
		t.Error("HashPassword produced a hash that doesn't verify")
	}
	if err := ValidatePassword("some-password-123", ""); err != nil {
		t.Errorf("ValidatePassword: %v", err)
	}
}
