package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
)

// tempDBPath returns a path to a fresh (not-yet-created) SQLite
// database file inside a t.TempDir(), so each test gets an isolated
// database on disk.
func tempDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "cloud-pulse.db")
}

func TestResetPassword_DefaultResetsToChangemeAndMustChange(t *testing.T) {
	t.Parallel()
	dbPath := tempDBPath(t)

	// Seed a non-default password + an active session, as if the hub
	// had already been used.
	ctx := context.Background()
	seedDB, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	seedHash, err := hub.HashPassword("some-other-password-1")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := seedDB.SetSetting(ctx, hub.SettingPasswordHash, seedHash); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := seedDB.SetSetting(ctx, hub.SettingMustChangePassword, "0"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := seedDB.CreateSession(ctx, models.Session{
		IDHash:    "abc123",
		CreatedAt: time.Now().Unix(),
		LastSeen:  time.Now().Unix(),
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := resetPassword(dbPath, "", true); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}

	verifyDB, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.Open (verify): %v", err)
	}
	defer func() { _ = verifyDB.Close() }()

	hash, ok, err := verifyDB.GetSetting(ctx, hub.SettingPasswordHash)
	if err != nil || !ok {
		t.Fatalf("GetSetting(password hash): ok=%v err=%v", ok, err)
	}
	if !hub.VerifyPassword("changeme", hash) {
		t.Error("password was not reset to \"changeme\"")
	}

	mustChange, ok, err := verifyDB.GetSetting(ctx, hub.SettingMustChangePassword)
	if err != nil || !ok || mustChange != "1" {
		t.Errorf("must_change = %q (ok=%v), want \"1\"", mustChange, ok)
	}

	sessions, err := verifyDB.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("sessions = %+v, want none (all revoked)", sessions)
	}
}

func TestResetPassword_StdinPassword_MustChangeFalse(t *testing.T) {
	t.Parallel()
	dbPath := tempDBPath(t)

	if err := resetPassword(dbPath, "a-brand-new-password-1", false); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}

	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	hash, ok, err := db.GetSetting(ctx, hub.SettingPasswordHash)
	if err != nil || !ok {
		t.Fatalf("GetSetting(password hash): ok=%v err=%v", ok, err)
	}
	if !hub.VerifyPassword("a-brand-new-password-1", hash) {
		t.Error("password was not set to the provided value")
	}

	mustChange, ok, err := db.GetSetting(ctx, hub.SettingMustChangePassword)
	if err != nil || !ok || mustChange != "0" {
		t.Errorf("must_change = %q (ok=%v), want \"0\"", mustChange, ok)
	}
}

func TestResetPassword_WeakStdinPasswordRejected(t *testing.T) {
	t.Parallel()
	dbPath := tempDBPath(t)

	err := resetPassword(dbPath, "short", false)
	if err == nil {
		t.Fatal("expected an error for a password that fails the policy")
	}
	if !strings.Contains(err.Error(), "new password") {
		t.Errorf("error = %v, want it to mention the new password", err)
	}
}

func TestResetPassword_WorksOnAlreadyMigratedDB(t *testing.T) {
	t.Parallel()
	dbPath := tempDBPath(t)

	// Opening once applies migrations 0001..0003 and closes cleanly,
	// simulating an existing (already-run) hub database.
	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := resetPassword(dbPath, "", true); err != nil {
		t.Fatalf("resetPassword on an already-migrated DB: %v", err)
	}
}

func TestReadPasswordLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"simple", "hello-world\n", "hello-world", false},
		{"crlf", "hello-world\r\n", "hello-world", false},
		{"no_trailing_newline", "hello-world", "hello-world", false},
		{"empty_line", "\n", "", true},
		{"no_input_at_all", "", "", true},
		{"only_first_line_used", "first-line\nsecond-line\n", "first-line", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := readPasswordLine(strings.NewReader(tc.input))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("got = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveDataDir_Precedence(t *testing.T) {
	t.Parallel()
	lookupEnv := func(v string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			if key == "CP_DATA_DIR" && v != "" {
				return v, true
			}
			return "", false
		}
	}
	systemDirAbsent := func() bool { return false }
	systemDirPresent := func() bool { return true }

	if got := resolveDataDir("/explicit/flag/dir", lookupEnv("/env/dir"), systemDirPresent); got != "/explicit/flag/dir" {
		t.Errorf("flag precedence: got %q, want /explicit/flag/dir", got)
	}
	if got := resolveDataDir("", lookupEnv("/env/dir"), systemDirPresent); got != "/env/dir" {
		t.Errorf("env precedence: got %q, want /env/dir", got)
	}
	if got := resolveDataDir("", lookupEnv(""), systemDirAbsent); got != "./data" {
		t.Errorf("fallback (system dir absent): got %q, want ./data", got)
	}
	if got := resolveDataDir("", lookupEnv(""), systemDirPresent); got != systemDataDir {
		t.Errorf("fallback (system dir present): got %q, want %q", got, systemDataDir)
	}
}

func TestChownToDataDirOwner_NoOpWhenNotRoot(t *testing.T) {
	t.Parallel()
	dbPath := tempDBPath(t)
	if err := resetPassword(dbPath, "", true); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}
	// This test process is essentially never running as euid 0 in CI;
	// chownToDataDirOwner must be a safe no-op rather than erroring.
	if err := chownToDataDirOwner(dbPath, filepath.Dir(dbPath)); err != nil {
		t.Errorf("chownToDataDirOwner: %v, want nil (no-op when not root)", err)
	}
}
