package storageusage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage/dropbox"
)

// DropboxProvider implements Provider for Dropbox (SPEC-v0.7 §3).
// Config carries "app_key" (non-secret, PKCE needs no client secret);
// Secret carries "refresh_token".
type DropboxProvider struct {
	client *dropbox.Client
}

// NewDropboxProvider constructs a DropboxProvider using httpClient for
// every outbound call, validated against opts' allowlist (production
// callers pass a zero SSRFOptions{} to use officialHosts).
func NewDropboxProvider(httpClient HTTPDoer, opts SSRFOptions) *DropboxProvider {
	ssrf := func(rawURL string) (*url.URL, error) { return validateURL(rawURL, opts) }
	return &DropboxProvider{client: dropbox.New(httpClient, ssrf)}
}

func (p *DropboxProvider) ID() models.StorageAccountProvider { return models.StorageAccountDropbox }

// FetchQuota exchanges the stored refresh token for a fresh access
// token, then calls get_space_usage + get_current_account.
func (p *DropboxProvider) FetchQuota(ctx context.Context, cfg, secret map[string]string) (Quota, error) {
	appKey := cfg["app_key"]
	refreshToken := secret["refresh_token"]
	if appKey == "" || refreshToken == "" {
		return Quota{}, errNotConfigured
	}

	tok, err := p.client.RefreshAccessToken(ctx, appKey, refreshToken)
	if err != nil {
		return Quota{}, err
	}

	usage, err := p.client.FetchSpaceUsage(ctx, tok.AccessToken)
	if err != nil {
		return Quota{}, err
	}
	account, err := p.client.FetchCurrentAccount(ctx, tok.AccessToken)
	if err != nil {
		return Quota{}, err
	}

	return Quota{
		AccountEmail: account.Email,
		AccountName:  account.DisplayName,
		Quota: models.StorageQuota{
			UsedBytes:      usage.UsedBytes,
			LimitBytes:     usage.AllocatedBytes,
			AllocationType: usage.AllocationTag,
		},
	}, nil
}

func (p *DropboxProvider) Classify(err error) (models.StorageAccountStatus, string) {
	return classifyDropboxError(err)
}

func (p *DropboxProvider) RevokeToken(ctx context.Context, cfg, secret map[string]string) error {
	appKey := cfg["app_key"]
	refreshToken := secret["refresh_token"]
	if refreshToken == "" {
		return nil
	}
	if appKey == "" {
		return fmt.Errorf("dropbox: revoke: missing app_key")
	}
	// Revoking requires a live access token, not the refresh token
	// itself — obtain one first.
	tok, err := p.client.RefreshAccessToken(ctx, appKey, refreshToken)
	if err != nil {
		return err
	}
	return p.client.RevokeToken(ctx, tok.AccessToken)
}

// classifyDropboxError maps an error from DropboxProvider's calls into
// a models.StorageAccountStatus/detail.
func classifyDropboxError(err error) (models.StorageAccountStatus, string) {
	if err == nil {
		return models.StorageAccountOK, ""
	}
	if errors.Is(err, errNotConfigured) {
		return models.StorageAccountNotConfigured, "account is missing app_key/refresh_token"
	}

	var httpErr *dropbox.HTTPStatusError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.Status == http.StatusUnauthorized:
			return models.StorageAccountAuthFailed, "dropbox api returned http 401"
		case httpErr.Status == http.StatusForbidden:
			return models.StorageAccountPermissionErr, "dropbox api returned http 403"
		case httpErr.Status == http.StatusTooManyRequests:
			return models.StorageAccountError, "dropbox api rate limited (http 429)"
		case httpErr.ErrorTag == "invalid_grant":
			return models.StorageAccountAuthFailed, "dropbox refresh token invalid or revoked"
		default:
			return models.StorageAccountError, fmt.Sprintf("dropbox api returned http %d", httpErr.Status)
		}
	}

	return models.StorageAccountAuthFailed, "token refresh failed"
}
