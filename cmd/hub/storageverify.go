// storageverify.go implements `cloud-pulse-hub storage verify`
// (SPEC-v0.7 §3): a user-run, real-account check that loads a
// connected storage account from the hub's own SQLite database and
// exercises the exact same quota-fetch code path the background
// poller uses (internal/storageusage's registered Provider for that
// account's provider type), never accepting credentials on this
// command's own command line — the account must already be connected
// via the dashboard's OAuth flow first.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage"
)

// storageVerifyTimeout bounds the whole storage verify CLI run (DB
// open + one provider quota fetch), mirroring resetPasswordTimeout's
// role.
const storageVerifyTimeout = 30 * time.Second

// runStorageVerify is the entry point for `cloud-pulse-hub storage
// verify`, dispatched from main before any normal flag parsing or hub
// config loading.
func runStorageVerify(args []string) int {
	fs := flag.NewFlagSet("storage verify", flag.ContinueOnError)
	providerFlag := fs.String("provider", "", "googledrive|dropbox (required)")
	nameFlag := fs.String("name", "", "the connected account's Name, as shown in Settings -> Storage accounts (required if more than one account uses --provider)")
	dataDirFlag := fs.String("data-dir", "", "override CP_DATA_DIR (directory holding the hub's SQLite database)")
	jsonFlag := fs.Bool("json", false, "print the fetched quota as JSON instead of text")
	fs.Usage = func() { printStorageVerifyUsage(false) }
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *providerFlag == "" {
		fmt.Fprintln(os.Stderr, "cloud-pulse-hub storage verify: --provider is required")
		printStorageVerifyUsage(false)
		return 2
	}
	provider := models.StorageAccountProvider(*providerFlag)
	if provider != models.StorageAccountGoogleDrive && provider != models.StorageAccountDropbox {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub storage verify: unknown --provider %q (want googledrive or dropbox)\n", *providerFlag)
		return 2
	}

	dataDir := resolveDataDir(*dataDirFlag, os.LookupEnv, defaultSystemDataDirExists)
	dbPath := dataDir + "/cloud-pulse.db"

	result, err := verifyStorageAccount(dbPath, provider, *nameFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub storage verify: %v\n", err)
		return 1
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "cloud-pulse-hub storage verify: encode result: %v\n", err)
			return 1
		}
		return 0
	}

	printStorageVerifyResult(result)
	return 0
}

// storageVerifyResult is the JSON/text output shape of a successful
// verify run — secret-free by construction (no token/secret field
// exists on this type).
type storageVerifyResult struct {
	Provider     models.StorageAccountProvider `json:"provider"`
	AccountName  string                        `json:"account_name"`
	AccountEmail string                        `json:"account_email,omitempty"`
	UsedBytes    uint64                        `json:"used_bytes"`
	LimitBytes   uint64                        `json:"limit_bytes,omitempty"`
	Unlimited    bool                          `json:"unlimited"`
	UsedPercent  float64                       `json:"used_percent,omitempty"`
}

// verifyStorageAccount opens the hub's database, finds the connected
// account matching provider (and name, if there is more than one), and
// fetches its quota through the real registered internal/storageusage
// Provider for that provider type — the exact same code path the
// background poller uses.
func verifyStorageAccount(dbPath string, provider models.StorageAccountProvider, name string) (*storageVerifyResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), storageVerifyTimeout)
	defer cancel()

	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }() // best-effort close; the fetch above already completed or failed

	return verifyStorageAccountWithRegistry(ctx, db, provider, name, buildStorageRegistry(&http.Client{Timeout: storageHTTPClientTimeout}, storageusage.SSRFOptions{}))
}

func verifyStorageAccountWithRegistry(ctx context.Context, db *storage.DB, provider models.StorageAccountProvider, name string, registry *storageusage.Registry) (*storageVerifyResult, error) {
	accounts, err := db.ListStorageAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list storage accounts: %w", err)
	}

	acct, err := selectStorageAccount(accounts, provider, name)
	if err != nil {
		return nil, err
	}

	p, ok := registry.Get(string(provider))
	if !ok {
		return nil, fmt.Errorf("no provider registered for %q", provider)
	}

	q, err := p.FetchQuota(ctx, acct.Config, acct.Secret)
	if err != nil {
		status, detail := p.Classify(err)
		return nil, fmt.Errorf("fetch quota failed (%s): %s", status, detail)
	}

	return &storageVerifyResult{
		Provider:     provider,
		AccountName:  acct.Name,
		AccountEmail: q.AccountEmail,
		UsedBytes:    q.Quota.UsedBytes,
		LimitBytes:   q.Quota.LimitBytes,
		Unlimited:    q.Quota.Unlimited,
		UsedPercent:  q.Quota.UsedPercent(),
	}, nil
}

// selectStorageAccount finds the one account in accounts matching
// provider, disambiguated by name if more than one matches. Returns a
// descriptive error (never a raw "not found") for every failure case,
// listing the available account names when there's ambiguity or no
// match, to guide the operator without needing to query the database
// directly.
func selectStorageAccount(accounts []models.StorageAccount, provider models.StorageAccountProvider, name string) (models.StorageAccount, error) {
	var matches []models.StorageAccount
	for _, a := range accounts {
		if a.Provider != provider {
			continue
		}
		if name != "" && a.Name != name {
			continue
		}
		matches = append(matches, a)
	}

	switch len(matches) {
	case 0:
		if name != "" {
			return models.StorageAccount{}, fmt.Errorf("no %s account named %q found; run without --name to see available accounts", provider, name)
		}
		return models.StorageAccount{}, fmt.Errorf("no %s account configured on this hub yet; connect one from Settings -> Storage accounts first", provider)
	case 1:
		if len(matches[0].Secret) == 0 {
			return models.StorageAccount{}, fmt.Errorf("account %q has not completed the OAuth connection flow yet", matches[0].Name)
		}
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.Name)
		}
		return models.StorageAccount{}, fmt.Errorf("multiple %s accounts found (%v); pass --name to disambiguate", provider, names)
	}
}

// printStorageVerifyResult prints result as human-readable text to
// stdout.
func printStorageVerifyResult(result *storageVerifyResult) {
	fmt.Printf("provider:     %s\n", result.Provider)
	fmt.Printf("account:      %s\n", result.AccountName)
	if result.AccountEmail != "" {
		fmt.Printf("email:        %s\n", result.AccountEmail)
	}
	if result.Unlimited {
		fmt.Printf("usage:        %s used (unlimited quota)\n", formatBytes(result.UsedBytes))
	} else {
		fmt.Printf("usage:        %s / %s (%.1f%%)\n", formatBytes(result.UsedBytes), formatBytes(result.LimitBytes), result.UsedPercent)
	}
	fmt.Println("status:       ok")
}

// formatBytes renders a byte count using binary (GiB) units, matching
// the dashboard's own display convention for storage quotas.
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	return fmt.Sprintf("%.2f %s", float64(b)/float64(div), units[exp])
}

// printStorageVerifyUsage documents storage verify's flag surface.
// toStdout selects os.Stdout (a `--help` request) vs os.Stderr (usage
// shown alongside an error) — mirroring the prep stage's
// toStdout-bool-branch pattern (see notes/v07-prep.md's errcheck
// pitfall note: a *os.File-typed parameter would be flagged by
// errcheck even when every call site passes os.Stdout/os.Stderr).
func printStorageVerifyUsage(toStdout bool) {
	lines := []string{
		"usage: cloud-pulse-hub storage verify --provider googledrive|dropbox [flags]",
		"",
		"  --provider NAME    googledrive|dropbox (required)",
		"  --name NAME        the connected account's Name (required if more than one account uses --provider)",
		"  --data-dir DIR     override CP_DATA_DIR (directory holding the hub's SQLite database)",
		"  --json             print the fetched quota as JSON instead of text",
		"",
		"Verifies a connected account by running the same quota-fetch code path the",
		"hub's own background poller uses, against the account's already-stored OAuth",
		"token in the hub's database — no credentials are accepted on this command's",
		"own command line. Connect the account from the dashboard's Settings ->",
		"Storage accounts page first.",
	}
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
