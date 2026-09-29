package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
)

// resetPasswordTimeout bounds the whole reset-password CLI run (DB
// open, update, chown), so a stuck/locked database can't hang the
// process forever.
const resetPasswordTimeout = 30 * time.Second

// runResetPassword implements `cloud-pulse-hub reset-password
// [--data-dir DIR] [--password-stdin]`: by default resets the admin
// password to "changeme" and sets must_change=1; with --password-stdin,
// reads one line from stdin as the new password (policy-checked,
// must_change=0). Either way, every existing session is revoked. See
// SPEC-v0.4 §1.
func runResetPassword(args []string) int {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	dataDirFlag := fs.String("data-dir", "", "override CP_DATA_DIR / default data directory resolution")
	passwordStdin := fs.Bool("password-stdin", false, "read the new password from stdin (one line) instead of resetting to the default")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-hub reset-password [--data-dir DIR] [--password-stdin]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dataDir := resolveDataDir(*dataDirFlag, os.LookupEnv, defaultSystemDataDirExists)
	dbPath := filepath.Join(dataDir, "cloud-pulse.db")

	var newPassword string
	mustChange := true
	if *passwordStdin {
		line, err := readPasswordLine(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cloud-pulse-hub reset-password: read password from stdin: %v\n", err)
			return 1
		}
		newPassword = line
		mustChange = false
	}

	if err := resetPassword(dbPath, newPassword, mustChange); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub reset-password: %v\n", err)
		return 1
	}

	if err := chownToDataDirOwner(dbPath, dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub reset-password: warning: %v\n", err)
		// Non-fatal: the password reset itself already succeeded.
	}

	if *passwordStdin {
		fmt.Println("cloud-pulse-hub: password updated; all existing sessions revoked")
	} else {
		fmt.Println("cloud-pulse-hub: password reset to the default (\"changeme\"); sign in and change it. All existing sessions revoked.")
	}
	return 0
}

// resetPassword opens the SQLite database at dbPath (applying
// migrations as needed, working correctly even while the hub is running
// thanks to WAL + busy_timeout), stores a new password hash and
// must-change flag, and revokes every session. An empty newPassword
// means "reset to the default password".
//
// This is a small, directly unit-testable helper: tests call it against
// a temp-dir database without going through the CLI's flag
// parsing/stdin handling.
func resetPassword(dbPath, newPassword string, mustChange bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), resetPasswordTimeout)
	defer cancel()

	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }() // best-effort close; the operations above already completed or failed

	password := newPassword
	if password == "" {
		password = "changeme"
	} else if err := hub.ValidatePassword(password, ""); err != nil {
		return fmt.Errorf("new password: %w", err)
	}

	hash, err := hub.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := db.SetSetting(ctx, hub.SettingPasswordHash, hash); err != nil {
		return fmt.Errorf("store password hash: %w", err)
	}
	mustChangeValue := "0"
	if mustChange {
		mustChangeValue = "1"
	}
	if err := db.SetSetting(ctx, hub.SettingMustChangePassword, mustChangeValue); err != nil {
		return fmt.Errorf("store must-change flag: %w", err)
	}
	if _, err := db.DeleteSessionsExcept(ctx, ""); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return nil
}

// readPasswordLine reads exactly one line from r, trimming a trailing
// newline (and, if present, a preceding carriage return for CRLF
// input), and returns an error if the line is empty or r yields no
// data at all.
func readPasswordLine(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("no password provided on stdin")
	}
	line := strings.TrimSuffix(scanner.Text(), "\r")
	if line == "" {
		return "", fmt.Errorf("empty password provided on stdin")
	}
	return line, nil
}

// systemDataDir is the data directory used by the standard installer
// (see scripts/install-hub.sh), consulted by resolveDataDir as the
// third-priority fallback when it exists.
const systemDataDir = "/var/lib/cloud-pulse"

// defaultSystemDataDirExists reports whether systemDataDir exists and
// is a directory, using the real filesystem. resolveDataDir takes this
// check as a parameter (rather than calling os.Stat directly) so tests
// — including reset-network's, which shares this helper — can inject a
// fake without depending on this specific path's real state on the
// machine running the test (on the hub's own host, that path is the
// real system install's actual data directory).
func defaultSystemDataDirExists() bool {
	info, err := os.Stat(systemDataDir)
	return err == nil && info.IsDir()
}

// resolveDataDir applies the DIR precedence shared by reset-password
// and reset-network: an explicit flag value, else CP_DATA_DIR, else
// systemDataDir if systemDataDirExists() reports it exists, else
// "./data".
func resolveDataDir(flagValue string, lookupEnv func(string) (string, bool), systemDataDirExists func() bool) string {
	if flagValue != "" {
		return flagValue
	}
	if v, ok := lookupEnv("CP_DATA_DIR"); ok && v != "" {
		return v
	}
	if systemDataDirExists() {
		return systemDataDir
	}
	return "./data"
}

// chownToDataDirOwner chdirs the database file plus its SQLite
// WAL/SHM sidecar files to dataDir's owning uid/gid, but only when
// running as root on a platform with numeric uid/gid ownership (POSIX);
// it is a no-op otherwise (including on Windows, where os.Stat's
// Sys() does not expose Uid/Gid).
func chownToDataDirOwner(dbPath, dataDir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if os.Geteuid() != 0 {
		return nil
	}
	uid, gid, ok := statOwner(dataDir)
	if !ok {
		return nil
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := dbPath + suffix
		if _, err := os.Stat(path); err != nil {
			continue // -wal/-shm may not exist depending on checkpoint state
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", path, err)
		}
	}
	return nil
}
