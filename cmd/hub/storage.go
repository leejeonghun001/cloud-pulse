package main

import (
	"net/http"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/config"
	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage/dropbox"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage/googledrive"
)

// storageHTTPClientTimeout bounds every outbound HTTP call the storage-
// usage providers make (OAuth token exchange/refresh/revoke, quota
// fetch) — mirrors httpClientTimeout's role for cloud bucket
// collectors.
const storageHTTPClientTimeout = 30 * time.Second

// buildStorageRegistry constructs the Google Drive and Dropbox providers for
// one hub runtime. Its registry is never shared with another hub, CLI run, or
// test server.
func buildStorageRegistry(client *http.Client, opts storageusage.SSRFOptions) *storageusage.Registry {
	return storageusage.NewRegistry(
		storageusage.NewGoogleDriveProvider(client, opts),
		storageusage.NewDropboxProvider(client, opts),
	)
}

// buildStorageRuntime constructs the hub.StorageRuntime passed as
// hub.Options.Storage. Unlike billing (which can be disabled via
// CP_BILLING=off), storage-usage account collection has no on/off
// switch — the runtime is always constructed since accounts are opt-in
// per-account from the dashboard (an empty account list simply means
// the background poller has nothing to do), matching SPEC-v0.7 §3's
// "always available once the hub is on v0.7.0" design (see
// README's [Remote agent updates] precedent for a similar always-on-
// but-nothing-to-do-until-configured feature).
func buildStorageRuntime(cfg config.Hub) *hub.StorageRuntime {
	client := &http.Client{Timeout: storageHTTPClientTimeout}

	return &hub.StorageRuntime{
		EnvInterval:          cfg.StorageInterval,
		HTTPClient:           client,
		Registry:             buildStorageRegistry(client, storageSSRFOptions(cfg)),
		AllowCustomEndpoints: cfg.StorageAllowCustomEndpoints,
		FakeBaseURL:          cfg.StorageFakeBaseURL,
	}
}

// storageSSRFOptions builds the storageusage.SSRFOptions for this hub's
// instance-owned provider registry from cfg: the zero value (production)
// unless CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS=1, mirroring
// internal/hub's googleSSRFChecker/dropboxSSRFChecker's own
// AllowCustomEndpoints/FakeBaseURL logic so both the OAuth handlers'
// inline clients and the background poller's registered Provider agree
// on where requests go.
func storageSSRFOptions(cfg config.Hub) storageusage.SSRFOptions {
	if !cfg.StorageAllowCustomEndpoints {
		return storageusage.SSRFOptions{}
	}
	return storageusage.SSRFOptions{AllowInsecure: true, RedirectBase: cfg.StorageFakeBaseURL}
}

// Compile-time assertions mirroring cmd/hub/main.go's pattern.
var (
	_ storageusage.Provider = (*storageusage.GoogleDriveProvider)(nil)
	_ storageusage.Provider = (*storageusage.DropboxProvider)(nil)
	_ storageusage.HTTPDoer = (*http.Client)(nil)
)

// Reference the provider sub-packages so this file documents the
// dependency directly (both are already imported transitively via
// storageusage, but an explicit reference here keeps `go vet`'s import
// graph obvious to a reader scanning cmd/hub for what talks to the
// network).
var (
	_ = googledrive.New
	_ = dropbox.New
)
