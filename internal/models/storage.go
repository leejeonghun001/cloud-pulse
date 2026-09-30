package models

// StorageAccountProvider identifies a supported storage-usage provider
// (SPEC-v0.7 §3), distinct from StorageProvider (S3/R2 object storage
// buckets) — this is per-account personal/team cloud drive usage, not
// bucket statistics.
type StorageAccountProvider string

// Supported storage-usage account providers.
const (
	StorageAccountGoogleDrive StorageAccountProvider = "googledrive"
	StorageAccountDropbox     StorageAccountProvider = "dropbox"
)

// StorageAccountStatus is the quiet-skip status model shared with cloud
// billing (SPEC-v0.6 §1's table, reused verbatim by SPEC-v0.7 §3's
// internal/poller framework).
type StorageAccountStatus string

// Supported storage account statuses.
const (
	StorageAccountNotConfigured StorageAccountStatus = "not_configured"
	StorageAccountAuthFailed    StorageAccountStatus = "auth_failed"
	StorageAccountPermissionErr StorageAccountStatus = "permission_denied"
	StorageAccountError         StorageAccountStatus = "error"
	StorageAccountOK            StorageAccountStatus = "ok"
	StorageAccountPendingOAuth  StorageAccountStatus = "pending_oauth"
)

// StorageInterval is the polling interval for storage-usage accounts
// (SPEC-v0.7 §3), sharing BillingInterval's 15m|1h|6h|24h vocabulary
// plus an extra 15m option.
type StorageInterval string

// Supported storage polling intervals.
const (
	StorageInterval15m     StorageInterval = "15m"
	StorageInterval1h      StorageInterval = "1h"
	StorageInterval6h      StorageInterval = "6h"
	StorageInterval24h     StorageInterval = "24h"
	StorageIntervalDefault                 = StorageInterval1h
)

// ValidStorageInterval reports whether v is one of the supported
// storage polling intervals.
func ValidStorageInterval(v StorageInterval) bool {
	switch v {
	case StorageInterval15m, StorageInterval1h, StorageInterval6h, StorageInterval24h:
		return true
	default:
		return false
	}
}

// StorageAccount is one connected storage-usage account (SPEC-v0.7 §3,
// table storage_accounts). Secret is kept separate from Config so API
// responses can omit it entirely rather than redacting a mixed map (see
// Redacted).
type StorageAccount struct {
	ID       int64                  `json:"id"`
	Provider StorageAccountProvider `json:"provider"`
	// Name is a user-facing label (e.g. an account email or a
	// user-chosen nickname), never itself treated as a secret.
	Name string `json:"name"`
	// Config holds non-secret per-provider fields (e.g. Google's
	// client_id, Dropbox's app key) — never redacted.
	Config map[string]string `json:"config"`
	// Secret holds per-provider secret fields (e.g. a Google OAuth
	// client_secret, a stored refresh token) — always omitted from API
	// responses via Redacted, never logged.
	Secret map[string]string `json:"secret,omitempty"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// Redacted returns a copy of a with Secret entirely removed (not merely
// masked field-by-field, since every Secret field is sensitive and none
// are ever meaningfully echoed back for a preserve-on-omit update flow
// the way NotifyChannel's config is — OAuth completion is the only
// write path for these fields).
func (a StorageAccount) Redacted() StorageAccount {
	out := a
	out.Secret = nil
	return out
}

// StorageQuota describes one account's storage usage/limit as reported
// by its provider (SPEC-v0.7 §3's "조회 내용").
type StorageQuota struct {
	// UsedBytes is total space used.
	UsedBytes uint64 `json:"used_bytes"`
	// LimitBytes is the account's total quota; 0 means unlimited
	// (Unlimited is also set true in that case so a zero-value struct
	// used before any snapshot exists isn't mistaken for "unlimited").
	LimitBytes uint64 `json:"limit_bytes"`
	Unlimited  bool   `json:"unlimited"`
	// TrashBytes is space used by trashed/deleted-but-not-purged items,
	// 0 if the provider doesn't report it separately (e.g. Dropbox).
	TrashBytes uint64 `json:"trash_bytes,omitempty"`
	// AllocationType is "individual" or "team" (Dropbox-specific;
	// empty for Google Drive).
	AllocationType string `json:"allocation_type,omitempty"`
}

// UsedPercent returns UsedBytes/LimitBytes as a percentage in [0,100+],
// or 0 when Unlimited or LimitBytes is 0.
func (q StorageQuota) UsedPercent() float64 {
	if q.Unlimited || q.LimitBytes == 0 {
		return 0
	}
	return float64(q.UsedBytes) / float64(q.LimitBytes) * 100
}

// StorageAccountSnapshot is the most recently collected usage snapshot
// for one storage account (SPEC-v0.7 §3, table storage_snapshots) plus
// the same freshness/status metadata used by cloud billing snapshots.
type StorageAccountSnapshot struct {
	AccountID int64                `json:"account_id"`
	Status    StorageAccountStatus `json:"status"`
	// StatusDetail is a short, secret-free classification detail (e.g.
	// an HTTP status code range name), empty when Status is "ok".
	StatusDetail string `json:"status_detail,omitempty"`
	// AccountEmail/AccountName identify the connected account as
	// reported by the provider itself (e.g. Google's
	// user.emailAddress, Dropbox's account name).
	AccountEmail string       `json:"account_email,omitempty"`
	AccountName  string       `json:"account_name,omitempty"`
	Quota        StorageQuota `json:"quota"`

	CollectedAt   int64 `json:"collected_at"`
	LastSuccessAt int64 `json:"last_success_at"`
	LastAttemptAt int64 `json:"last_attempt_at"`
	// Stale is computed at read time (twice the configured polling
	// interval since LastSuccessAt), mirroring CloudCostStale.
	Stale bool `json:"stale"`
}

// StorageAccountStale reports whether snap is stale: LastSuccessAt is
// nonzero and more than 2*interval old as of now (unix seconds). A
// LastSuccessAt of 0 (never succeeded) is reported by the caller via a
// distinct "no successful data yet" UI state, not via this helper —
// mirrors CloudCostStale's contract.
func StorageAccountStale(lastSuccessAt, now int64, interval StorageInterval) bool {
	if lastSuccessAt == 0 {
		return false
	}
	secs := storageIntervalSeconds(interval)
	if secs == 0 {
		return false
	}
	return now-lastSuccessAt > 2*secs
}

func storageIntervalSeconds(interval StorageInterval) int64 {
	switch interval {
	case StorageInterval15m:
		return 15 * 60
	case StorageInterval1h:
		return 60 * 60
	case StorageInterval6h:
		return 6 * 60 * 60
	case StorageInterval24h:
		return 24 * 60 * 60
	default:
		return 0
	}
}

// StorageAccountView combines a redacted StorageAccount with its latest
// snapshot, for GET /api/v1/storage/accounts.
type StorageAccountView struct {
	Account  StorageAccount          `json:"account"`
	Snapshot *StorageAccountSnapshot `json:"snapshot,omitempty"`
}

// StorageAccountCreate is the request body for
// POST /api/v1/storage/accounts: registers a new account shell (before
// OAuth completes). Config carries provider-specific non-secret fields
// (Google: client_id; Dropbox: app_key); Secret carries
// provider-specific secret fields (Google: client_secret; Dropbox: none
// needed, PKCE has no client secret).
type StorageAccountCreate struct {
	Provider StorageAccountProvider `json:"provider"`
	Name     string                 `json:"name"`
	Config   map[string]string      `json:"config"`
	Secret   map[string]string      `json:"secret,omitempty"`
}

// StorageOAuthStartResponse is the response body for
// POST /api/v1/storage/accounts/{id}/oauth/start. Fields are a superset
// covering both supported flows:
//   - Google device flow: VerificationURL + UserCode + ExpiresInSec +
//     PollIntervalSec (poll .../oauth/complete on this cadence).
//   - Dropbox PKCE flow: AuthorizeURL (open in a browser; the user
//     pastes back the resulting code, which is submitted to
//     .../oauth/complete's Code field).
type StorageOAuthStartResponse struct {
	// Flow is "device" (Google) or "pkce" (Dropbox).
	Flow string `json:"flow"`

	// Device flow fields.
	VerificationURL string `json:"verification_url,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	ExpiresInSec    int    `json:"expires_in_sec,omitempty"`
	PollIntervalSec int    `json:"poll_interval_sec,omitempty"`

	// PKCE flow fields.
	AuthorizeURL string `json:"authorize_url,omitempty"`
}

// StorageOAuthCompleteRequest is the request body for
// POST /api/v1/storage/accounts/{id}/oauth/complete. Code is used only
// by the PKCE flow (Dropbox); the device flow (Google) needs no body
// field since the hub itself polls the token endpoint using state saved
// from the start call.
type StorageOAuthCompleteRequest struct {
	Code string `json:"code,omitempty"`
}

// StorageOAuthCompleteResponse is the response body for
// POST /api/v1/storage/accounts/{id}/oauth/complete.
type StorageOAuthCompleteResponse struct {
	// Status is "connected" on success, or one of
	// "authorization_pending"|"slow_down" for a device-flow caller that
	// should keep polling at (a possibly backed-off) PollIntervalSec,
	// or "denied"|"expired" for a terminal device-flow failure.
	Status string `json:"status"`
	// PollIntervalSec is only set when Status is
	// "authorization_pending" or "slow_down", possibly increased from
	// the original oauth/start value (RFC 8628 slow_down semantics).
	PollIntervalSec int             `json:"poll_interval_sec,omitempty"`
	Account         *StorageAccount `json:"account,omitempty"`
}
