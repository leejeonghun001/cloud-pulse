// Package storageusage implements SPEC-v0.7 §3's storage-usage account
// collection: polling Google Drive and Dropbox for a connected
// account's quota via each provider's own OAuth-authenticated REST API,
// classifying every quiet-skip condition the same way
// internal/billing's AWS/OCI collectors do, and driving the OAuth
// device-flow (Google) / PKCE code-flow (Dropbox) state machines used
// to obtain that authentication in the first place.
package storageusage

import (
	"context"
	"net/http"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// Provider is one storage-usage backend (Google Drive, Dropbox). A
// Provider is stateless with respect to any single account — every
// method takes the account's Config/Secret explicitly, so a single
// Provider instance serves every connected account of that provider
// type.
type Provider interface {
	// ID returns the models.StorageAccountProvider value this Provider
	// implements.
	ID() models.StorageAccountProvider

	// FetchQuota retrieves the current quota/usage and account
	// identity for the account described by cfg/secret. It returns a
	// classified error (see Classify) rather than raw transport errors
	// where possible; callers still run every returned error through
	// Classify to get a models.StorageAccountStatus.
	FetchQuota(ctx context.Context, cfg, secret map[string]string) (Quota, error)

	// Classify inspects err (as returned by FetchQuota or an OAuth
	// step) and returns the models.StorageAccountStatus/detail to
	// record. Never returns models.StorageAccountOK — callers set that
	// themselves on a nil error.
	Classify(err error) (models.StorageAccountStatus, string)

	// RevokeToken best-effort revokes secret's stored token with the
	// provider, called when an account is deleted. cfg is passed
	// alongside secret since some providers (Dropbox) need a non-secret
	// config field (app_key) to obtain a fresh access token before
	// revoking it. Errors are logged, never surfaced to the API caller
	// (the account row is still deleted either way).
	RevokeToken(ctx context.Context, cfg, secret map[string]string) error
}

// Quota is a provider-neutral result of FetchQuota, mapped into
// models.StorageAccountSnapshot by the caller (internal/hub's storage
// runtime), which also fills in the freshness fields.
type Quota struct {
	AccountEmail string
	AccountName  string
	Quota        models.StorageQuota
}

// HTTPDoer is the minimal HTTP client surface Provider implementations
// need — satisfied by *http.Client, injected so tests can swap in a
// client pointed at an httptest server (and so a provider never
// constructs its own default-timeout client internally, matching this
// project's existing collector-injection convention in
// internal/billing/internal/cloud).
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Clock returns the current time; used for OAuth flow-state expiry
// checks (device-code/PKCE-state TTLs), injected for deterministic
// tests.
type Clock func() time.Time
