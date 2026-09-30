package storageusage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage/googledrive"
)

// GoogleDriveProvider implements Provider for Google Drive (SPEC-v0.7
// §3). Config carries "client_id" (non-secret); Secret carries
// "client_secret" and "refresh_token".
type GoogleDriveProvider struct {
	client *googledrive.Client
}

// NewGoogleDriveProvider constructs a GoogleDriveProvider using
// httpClient for every outbound call, validated against opts'
// allowlist (production callers pass a zero SSRFOptions{} to use
// officialHosts).
func NewGoogleDriveProvider(httpClient HTTPDoer, opts SSRFOptions) *GoogleDriveProvider {
	ssrf := func(rawURL string) (*url.URL, error) { return validateURL(rawURL, opts) }
	return &GoogleDriveProvider{client: googledrive.New(httpClient, ssrf)}
}

func (p *GoogleDriveProvider) ID() models.StorageAccountProvider {
	return models.StorageAccountGoogleDrive
}

// FetchQuota exchanges the stored refresh token for a fresh access
// token, then calls Drive's about.get.
func (p *GoogleDriveProvider) FetchQuota(ctx context.Context, cfg, secret map[string]string) (Quota, error) {
	clientID := cfg["client_id"]
	clientSecret := secret["client_secret"]
	refreshToken := secret["refresh_token"]
	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return Quota{}, errNotConfigured
	}

	accessToken, err := p.client.RefreshAccessToken(ctx, clientID, clientSecret, refreshToken)
	if err != nil {
		return Quota{}, err
	}

	about, err := p.client.FetchAbout(ctx, accessToken)
	if err != nil {
		return Quota{}, err
	}

	return Quota{
		AccountEmail: about.AccountEmail,
		AccountName:  about.AccountDisplayName,
		Quota: models.StorageQuota{
			UsedBytes:  about.UsageBytes,
			LimitBytes: about.LimitBytes,
			Unlimited:  about.Unlimited,
			TrashBytes: about.UsageInTrashBytes,
		},
	}, nil
}

func (p *GoogleDriveProvider) Classify(err error) (models.StorageAccountStatus, string) {
	return classifyGoogleError(err)
}

func (p *GoogleDriveProvider) RevokeToken(ctx context.Context, _, secret map[string]string) error {
	refreshToken := secret["refresh_token"]
	if refreshToken == "" {
		return nil
	}
	return p.client.RevokeToken(ctx, refreshToken)
}

// errNotConfigured is a sentinel classified as StorageAccountNotConfigured.
var errNotConfigured = errors.New("storageusage: account not fully configured")

// classifyGoogleError maps an error from GoogleDriveProvider's calls
// into a models.StorageAccountStatus/detail, never logging secret
// content — only fixed strings and HTTP status codes reach the
// returned detail.
func classifyGoogleError(err error) (models.StorageAccountStatus, string) {
	if err == nil {
		return models.StorageAccountOK, ""
	}
	if errors.Is(err, errNotConfigured) {
		return models.StorageAccountNotConfigured, "account is missing client_id/client_secret/refresh_token"
	}

	var httpErr *googledrive.HTTPStatusError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden:
			return models.StorageAccountAuthFailed, fmt.Sprintf("google api returned http %d", httpErr.Status)
		case httpErr.Status == http.StatusTooManyRequests:
			return models.StorageAccountError, "google api rate limited (http 429)"
		default:
			return models.StorageAccountError, fmt.Sprintf("google api returned http %d", httpErr.Status)
		}
	}

	// A refresh-token exchange failure (e.g. "invalid_grant") surfaces
	// as a plain error from RefreshAccessToken, not an httpStatusError
	// — treat any such failure as an auth failure, the closest quiet-
	// skip status (a revoked/expired refresh token is exactly this
	// case).
	return models.StorageAccountAuthFailed, "token refresh failed"
}
