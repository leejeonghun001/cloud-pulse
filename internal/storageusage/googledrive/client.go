package googledrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// deviceCodeEndpoint/tokenEndpoint/revokeEndpoint/aboutEndpoint are
// Google's official OAuth2/Drive hosts (SPEC-v0.7 §3 research notes,
// notes/v07-storage.md): device flow per
// developers.google.com/identity/protocols/oauth2/limited-input-device.
const (
	deviceCodeEndpoint = "https://oauth2.googleapis.com/device/code"
	tokenEndpoint      = "https://oauth2.googleapis.com/token"
	revokeEndpoint     = "https://oauth2.googleapis.com/revoke"
	aboutEndpoint      = "https://www.googleapis.com/drive/v3/about"
)

// driveFileScope is the Drive scope requested (SPEC-v0.7 §3: minimal,
// non-sensitive, sufficient for about.get's storageQuota — see
// notes/v07-storage.md's research citations).
const driveFileScope = "https://www.googleapis.com/auth/drive.file"

// deviceCodeGrantType is RFC 8628's grant_type value for the device-flow
// token-polling request.
const deviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// DeviceCodeResponse is Google's step-2 response to a device/code
// request.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// PollOutcome is the classified result of one device-flow token-polling
// request (SPEC-v0.7 §3's authorization_pending/slow_down/access_denied/
// expired_token handling).
type PollOutcome string

// Poll outcomes.
const (
	PollGranted              PollOutcome = "granted"
	PollAuthorizationPending PollOutcome = "authorization_pending"
	PollSlowDown             PollOutcome = "slow_down"
	PollAccessDenied         PollOutcome = "access_denied"
	PollExpired              PollOutcome = "expired"
	PollError                PollOutcome = "error"
)

// PollResult is the outcome of one call to Poll.
type PollResult struct {
	Outcome      PollOutcome
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	// ErrorDetail is a short, secret-free classification string, set
	// when Outcome is PollError.
	ErrorDetail string
}

// Client is a Google OAuth2 device-flow + Drive API client. The zero
// value is not usable; construct with New.
type Client struct {
	httpClient httpDoer
	ssrf       ssrfChecker
}

type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ssrfChecker validates a URL against an allowlist before Client issues
// any request to it — injected so tests can point Client at an
// httptest server without a production SSRF-bypass env var (see
// internal/storageusage/ssrf.go, which supplies the real
// implementation via a small adapter in internal/storageusage/googledrive_provider.go).
type ssrfChecker func(rawURL string) (*url.URL, error)

// New constructs a Client. ssrf must not be nil in production (a
// no-op/allow-all func is only appropriate for tests that already
// trust their own httptest URLs).
func New(httpClient httpDoer, ssrf ssrfChecker) *Client {
	return &Client{httpClient: httpClient, ssrf: ssrf}
}

// RequestDeviceCode performs step 1 of the device flow: POST
// oauth2.googleapis.com/device/code with client_id and the drive.file
// scope.
func (c *Client) RequestDeviceCode(ctx context.Context, clientID string) (DeviceCodeResponse, error) {
	form := url.Values{
		"client_id": {clientID},
		"scope":     {driveFileScope},
	}
	var out DeviceCodeResponse
	_, err := c.post(ctx, deviceCodeEndpoint, form, &out)
	if err != nil {
		return DeviceCodeResponse{}, err
	}
	return out, nil
}

// Poll performs one device-flow token-polling request (step 4/6).
func (c *Client) Poll(ctx context.Context, clientID, clientSecret, deviceCode string) PollResult {
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"device_code":   {deviceCode},
		"grant_type":    {deviceCodeGrantType},
	}

	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	status, err := c.post(ctx, tokenEndpoint, form, &body)
	if err != nil {
		return PollResult{Outcome: PollError, ErrorDetail: err.Error()}
	}

	if status == http.StatusOK && body.AccessToken != "" {
		return PollResult{
			Outcome:      PollGranted,
			AccessToken:  body.AccessToken,
			RefreshToken: body.RefreshToken,
			ExpiresIn:    body.ExpiresIn,
		}
	}

	switch body.Error {
	case "authorization_pending":
		return PollResult{Outcome: PollAuthorizationPending}
	case "slow_down":
		return PollResult{Outcome: PollSlowDown}
	case "access_denied":
		return PollResult{Outcome: PollAccessDenied}
	case "invalid_grant", "expired_token":
		// Google's own doc lists invalid_grant as "code invalid, already
		// claimed, or cannot be parsed" — for the device flow this is
		// functionally the same terminal state as an expired device
		// code from the caller's point of view (start over).
		return PollResult{Outcome: PollExpired}
	default:
		detail := body.Error
		if detail == "" {
			detail = fmt.Sprintf("unexpected http status %d", status)
		}
		return PollResult{Outcome: PollError, ErrorDetail: detail}
	}
}

// RefreshAccessToken exchanges a stored refresh token for a fresh
// access token.
func (c *Client) RefreshAccessToken(ctx context.Context, clientID, clientSecret, refreshToken string) (accessToken string, err error) {
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	status, err := c.post(ctx, tokenEndpoint, form, &body)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK || body.AccessToken == "" {
		if body.Error != "" {
			return "", fmt.Errorf("googledrive: refresh token: %s", body.Error)
		}
		return "", fmt.Errorf("googledrive: refresh token: unexpected http status %d", status)
	}
	return body.AccessToken, nil
}

// RevokeToken revokes token (an access or refresh token) with Google.
func (c *Client) RevokeToken(ctx context.Context, token string) error {
	form := url.Values{"token": {token}}
	status, err := c.post(ctx, revokeEndpoint, form, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("googledrive: revoke token: unexpected http status %d", status)
	}
	return nil
}

// AboutQuota is the subset of Drive's about.get response this client
// parses.
type AboutQuota struct {
	LimitBytes         uint64 // 0 + Unlimited=true if the field was absent
	Unlimited          bool
	UsageBytes         uint64
	UsageInDriveBytes  uint64
	UsageInTrashBytes  uint64
	AccountEmail       string
	AccountDisplayName string
}

// FetchAbout calls Drive v3's about.get with accessToken, requesting
// storageQuota and user fields.
func (c *Client) FetchAbout(ctx context.Context, accessToken string) (AboutQuota, error) {
	u, err := c.ssrf(aboutEndpoint)
	if err != nil {
		return AboutQuota{}, err
	}
	q := u.Query()
	q.Set("fields", "storageQuota,user(emailAddress,displayName)")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return AboutQuota{}, fmt.Errorf("googledrive: build about request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return AboutQuota{}, fmt.Errorf("googledrive: about request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return AboutQuota{}, fmt.Errorf("googledrive: read about response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return AboutQuota{}, &HTTPStatusError{Status: resp.StatusCode, Body: string(data)}
	}

	var body struct {
		StorageQuota struct {
			Limit             string `json:"limit"`
			Usage             string `json:"usage"`
			UsageInDrive      string `json:"usageInDrive"`
			UsageInDriveTrash string `json:"usageInDriveTrash"`
		} `json:"storageQuota"`
		User struct {
			EmailAddress string `json:"emailAddress"`
			DisplayName  string `json:"displayName"`
		} `json:"user"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return AboutQuota{}, fmt.Errorf("googledrive: parse about response: %w", err)
	}

	out := AboutQuota{
		UsageBytes:         parseUint(body.StorageQuota.Usage),
		UsageInDriveBytes:  parseUint(body.StorageQuota.UsageInDrive),
		UsageInTrashBytes:  parseUint(body.StorageQuota.UsageInDriveTrash),
		AccountEmail:       body.User.EmailAddress,
		AccountDisplayName: body.User.DisplayName,
	}
	if body.StorageQuota.Limit == "" {
		out.Unlimited = true
	} else {
		out.LimitBytes = parseUint(body.StorageQuota.Limit)
	}
	return out, nil
}

// httpStatusError carries an HTTP response's status code and body for
// Classify to inspect, without ever needing to log the body itself.
type HTTPStatusError struct {
	Status int
	Body   string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("googledrive: http status %d", e.Status)
}

// post issues a form-encoded POST to rawURL (validated via c.ssrf) and
// decodes a JSON response into out (skipped if out is nil). Returns the
// HTTP status code.
func (c *Client) post(ctx context.Context, rawURL string, form url.Values, out any) (int, error) {
	u, err := c.ssrf(rawURL)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return 0, fmt.Errorf("googledrive: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("googledrive: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("googledrive: read response: %w", err)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("googledrive: parse response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func parseUint(s string) uint64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// DeviceFlowExpiry computes the wall-clock deadline for a device code,
// given resp.ExpiresIn seconds from now.
func DeviceFlowExpiry(now time.Time, resp DeviceCodeResponse) time.Time {
	return now.Add(time.Duration(resp.ExpiresIn) * time.Second)
}
