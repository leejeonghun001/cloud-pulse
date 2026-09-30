// notifyverify.go implements `cloud-pulse-hub notify verify` (SPEC-v0.7
// §2): sends a real test notification (chart image included) through the
// exact same internal/notify senders and internal/alerting/chart renderer
// the hub's own alert-delivery worker uses, then attempts a read-back
// (and, with --cleanup, a delete) via each sender's optional
// notify.MessageReader/MessageDeleter interface. Credentials are read
// only from CP_VERIFY_* environment variables or a --credentials-file —
// never from a command-line argument value, matching
// scripts/verify-notify.py's existing v0.6.0 rules.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Environment variables credentials may be read from — the Go CLI's
// names mirror scripts/verify-notify.py's exactly (SPEC-v0.7 §2).
const (
	envDiscordWebhookURL     = "CP_VERIFY_DISCORD_WEBHOOK_URL"
	envTelegramBotToken      = "CP_VERIFY_TELEGRAM_BOT_TOKEN"
	envTelegramChatID        = "CP_VERIFY_TELEGRAM_CHAT_ID"
	envWhatsAppAccessToken   = "CP_VERIFY_WHATSAPP_ACCESS_TOKEN"
	envWhatsAppPhoneNumberID = "CP_VERIFY_WHATSAPP_PHONE_NUMBER_ID"
	envWhatsAppTo            = "CP_VERIFY_WHATSAPP_TO"
	envWhatsAppTemplateName  = "CP_VERIFY_WHATSAPP_TEMPLATE_NAME"
	envWhatsAppTemplateLang  = "CP_VERIFY_WHATSAPP_TEMPLATE_LANG"
	envWhatsAppAppSecret     = "CP_VERIFY_WHATSAPP_APP_SECRET"
)

// credentialEnvKeys lists every recognized KEY in a --credentials-file or
// the environment, in a fixed order (used for both file parsing and the
// masked "configured" summary).
var credentialEnvKeys = []string{
	envDiscordWebhookURL,
	envTelegramBotToken,
	envTelegramChatID,
	envWhatsAppAccessToken,
	envWhatsAppPhoneNumberID,
	envWhatsAppTo,
	envWhatsAppTemplateName,
	envWhatsAppTemplateLang,
	envWhatsAppAppSecret,
}

// notifyVerifyDefaultTimeout is the per-request timeout used unless
// --timeout overrides it.
const notifyVerifyDefaultTimeout = 20 * time.Second

// notifyVerifyOptions holds runNotifyVerify's parsed flags.
type notifyVerifyOptions struct {
	credentialsFile     string
	platform            string
	cleanup             bool
	timeout             time.Duration
	jsonOutput          bool
	statusWebhookListen string
	statusWebhookWait   time.Duration
	senderFactory       notifySenderFactory
}

// runNotifyVerify is the entry point for `cloud-pulse-hub notify
// verify`, dispatched from main before any normal flag parsing or
// hub-server config loading (the same pattern as runUpdate/
// runResetPassword). Returns a process exit code.
func runNotifyVerify(args []string) int {
	opts, err := parseNotifyVerifyArgs(args)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			printNotifyVerifyUsage(true)
			return 0
		}
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub notify verify: %v\n", err)
		printNotifyVerifyUsage(false)
		return 1
	}

	values, err := loadNotifyVerifyCredentials(opts.credentialsFile, os.LookupEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub notify verify: %v\n", err)
		return 1
	}
	report := runNotifyVerifyReport(context.Background(), values, opts)

	if opts.jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "cloud-pulse-hub notify verify: encode json: %v\n", err)
			return 1
		}
	} else {
		printNotifyVerifyText(report)
	}

	if !report.OK {
		return 1
	}
	return 0
}

// errHelpRequested is a sentinel returned by parseNotifyVerifyArgs when
// -h/--help was given, distinguishing "print usage and exit 0" from an
// actual parse error ("print usage and exit 1").
var errHelpRequested = errors.New("help requested")

// parseNotifyVerifyArgs parses args by hand (not the stdlib flag
// package) so it can apply the same argv-secret-rejection rule
// scripts/verify-notify.py's parser enforces: no flag value here is ever
// a credential (every credential-shaped flag scripts/verify-notify.py
// has was deliberately never added to this CLI's flag surface either),
// but we still defensively reject any accepted flag's value that merely
// looks like a secret, so a user who tries to pass one by mistake gets a
// clear rejection instead of it silently reaching argv/ps output.
func parseNotifyVerifyArgs(args []string) (notifyVerifyOptions, error) {
	opts := notifyVerifyOptions{
		platform:          "all",
		timeout:           notifyVerifyDefaultTimeout,
		statusWebhookWait: 60 * time.Second,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			return opts, errHelpRequested
		case a == "--credentials-file":
			v, err := nextArgValue(args, &i, "--credentials-file")
			if err != nil {
				return opts, err
			}
			opts.credentialsFile = v
		case a == "--platform":
			v, err := nextArgValue(args, &i, "--platform")
			if err != nil {
				return opts, err
			}
			switch v {
			case "discord", "telegram", "whatsapp", "all":
				opts.platform = v
			default:
				return opts, fmt.Errorf("invalid --platform %q (want discord|telegram|whatsapp|all)", v)
			}
		case a == "--cleanup":
			opts.cleanup = true
		case a == "--timeout":
			v, err := nextArgValue(args, &i, "--timeout")
			if err != nil {
				return opts, err
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return opts, fmt.Errorf("invalid --timeout %q: %w", v, err)
			}
			opts.timeout = d
		case a == "--json":
			opts.jsonOutput = true
		case a == "--status-webhook-listen":
			v, err := nextArgValue(args, &i, "--status-webhook-listen")
			if err != nil {
				return opts, err
			}
			opts.statusWebhookListen = v
		case a == "--status-webhook-wait":
			v, err := nextArgValue(args, &i, "--status-webhook-wait")
			if err != nil {
				return opts, err
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return opts, fmt.Errorf("invalid --status-webhook-wait %q: %w", v, err)
			}
			opts.statusWebhookWait = d
		default:
			return opts, fmt.Errorf("unrecognized argument %q (see --help); credentials must be provided via environment variables or --credentials-file, never as an argument", redactIfSecretLooking(a))
		}
	}
	return opts, nil
}

// nextArgValue consumes and returns the value following a flag at
// args[*i], advancing *i, or an error if no value follows.
func nextArgValue(args []string, i *int, flagName string) (string, error) {
	if *i+1 >= len(args) {
		return "", fmt.Errorf("%s requires a value", flagName)
	}
	*i++
	return args[*i], nil
}

// redactIfSecretLooking never echoes a value that looks like it could be
// a credential (a bot-token shape, a URL, or a long opaque token) back
// into an error message — mirroring scripts/verify-notify.py's
// _looks_like_token/UnknownArgumentError behavior so this Go CLI never
// becomes the weaker link in the same rule.
func redactIfSecretLooking(v string) string {
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return "(redacted: looks like a URL)"
	}
	if len(v) >= 40 && isOpaqueTokenShape(v) {
		return "(redacted: looks like a token)"
	}
	if i := strings.IndexByte(v, ':'); i >= 6 && i < len(v)-1 && isAllDigits(v[:i]) {
		return "(redacted: looks like a bot token)"
	}
	return v
}

func isOpaqueTokenShape(s string) bool {
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// printNotifyVerifyUsage documents the flag surface (SPEC-v0.7 §2).
// toStdout selects os.Stdout (a `--help` request) vs os.Stderr (usage
// shown alongside an error).
func printNotifyVerifyUsage(toStdout bool) {
	lines := []string{
		"usage: cloud-pulse-hub notify verify --credentials-file FILE [flags]",
		"",
		"  --credentials-file FILE     KEY=VALUE file (mode 0600 or stricter); see docs/notifications.md",
		"  --platform NAME             discord|telegram|whatsapp|all (default all)",
		"  --cleanup                   delete the sent test message/media after verifying it",
		"  --timeout DURATION          per-request timeout (default 20s)",
		"  --json                      print a models.NotifyVerifyReport as JSON instead of text",
		"  --status-webhook-listen ADDR  (WhatsApp only) run a local HTTP listener on ADDR",
		"                                (e.g. 127.0.0.1:8443) and wait for a signed delivery-status",
		"                                webhook before reporting WhatsApp as verified instead of accepted;",
		"                                only useful once a real Meta app webhook is pointed at it",
		"  --status-webhook-wait DURATION  how long to wait for that delivery (default 60s)",
		"",
		"Credentials are read only from --credentials-file or CP_VERIFY_* environment",
		"variables — never from a command-line argument value. Recognized environment",
		"variables: " + strings.Join(credentialEnvKeys, ", "),
	}
	printLines(lines, toStdout)
}

func printLines(lines []string, toStdout bool) {
	if toStdout {
		for _, l := range lines {
			fmt.Println(l)
		}
		return
	}
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, l)
	}
}

// loadNotifyVerifyCredentials resolves credential values, giving
// --credentials-file priority over environment variables — identical
// precedence to scripts/verify-notify.py's load_credentials. Only known
// keys (credentialEnvKeys) are considered; unrelated file/env entries
// are ignored, so a shared credentials file with extra keys still works.
func loadNotifyVerifyCredentials(credentialsFile string, lookupEnv func(string) (string, bool)) (map[string]string, error) {
	resolved := make(map[string]string)
	if credentialsFile != "" {
		fileValues, err := parseNotifyVerifyCredentialsFile(credentialsFile)
		if err != nil {
			return nil, err
		}
		for _, k := range credentialEnvKeys {
			if v, ok := fileValues[k]; ok {
				resolved[k] = v
			}
		}
	}
	for _, k := range credentialEnvKeys {
		if _, ok := resolved[k]; ok {
			continue
		}
		if v, ok := lookupEnv(k); ok {
			resolved[k] = v
		}
	}
	return resolved, nil
}

// parseNotifyVerifyCredentialsFile reads and permission-checks path,
// then parses it as KEY=VALUE lines (no shell interpretation, matching
// scripts/verify-notify.py's _parse_credentials_file exactly).
func parseNotifyVerifyCredentialsFile(path string) (map[string]string, error) {
	if err := checkCredentialsFilePermissions(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-supplied CLI flag, permission-checked above
	if err != nil {
		return nil, fmt.Errorf("read credentials file: %w", err)
	}
	result := make(map[string]string)
	for lineNum, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		idx := strings.IndexByte(trimmed, '=')
		if idx < 0 {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNum+1)
		}
		key := strings.TrimSpace(trimmed[:idx])
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty key", path, lineNum+1)
		}
		result[key] = trimmed[idx+1:]
	}
	return result, nil
}

// checkCredentialsFilePermissions rejects a credentials file that is not
// safely private:
//   - Unix: must be owned by the current user and grant no permission to
//     group/other (mode 0600 or stricter) — identical to
//     scripts/verify-notify.py's _check_credentials_file_permissions.
//   - Windows: no POSIX permission bits exist to check, so this instead
//     requires the file to live inside the current user's profile
//     directory (%USERPROFILE%) and prints a warning that Windows ACLs
//     are not otherwise verified (SPEC-v0.7 §2's own documented
//     platform-limitation carve-out).
func checkCredentialsFilePermissions(path string) error {
	if runtime.GOOS == "windows" {
		return checkCredentialsFilePermissionsWindows(path)
	}
	return checkCredentialsFilePermissionsUnix(path)
}

// checkCredentialsFilePermissionsWindows implements the Windows half of
// checkCredentialsFilePermissions. Defined here (not a separate
// windows-only build-tagged file) since it contains no Windows-specific
// API calls — only a path-prefix check against os.UserHomeDir(), which
// resolves to %USERPROFILE% on Windows and is safe to compile-check on
// every OS even though it only ever runs when runtime.GOOS == "windows".
func checkCredentialsFilePermissionsWindows(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("%s: could not resolve user profile directory to check credentials-file location: %w", path, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%s: resolve absolute path: %w", path, err)
	}
	rel, err := filepath.Rel(home, absPath)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return fmt.Errorf("%s: must be located inside your user profile directory (%s) on Windows, where NTFS ACLs default to owner-only access", path, home)
	}
	fmt.Fprintf(os.Stderr, "warning: cloud-pulse-hub notify verify: on Windows, only the credentials file's location (inside your user profile) is checked — its NTFS ACLs are not independently verified; ensure it is not shared with other accounts\n")
	return nil
}
