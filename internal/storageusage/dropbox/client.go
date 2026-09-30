// Package dropbox implements SPEC-v0.7 §3's Dropbox integration: the
// no-redirect OAuth2 PKCE code flow (authorize URL shown to the user,
// who pastes back the resulting code — see notes/v07-storage.md's
// research citations for developers.dropbox.com/oauth-guide), token
// exchange/refresh/revoke, and the users/get_space_usage +
// users/get_current_account API calls used to report quota.
package dropbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// authorizeURL/tokenEndpoint/revokeEndpoint/spaceUsageEndpoint/
// currentAccountEndpoint are Dropbox's official OAuth/API hosts.
const (
	authorizeURL           = "https://www.dropbox.com/oauth2/authorize"
	tokenEndpoint          = "https://api.dropboxapi.com/oauth2/token"
	revokeEndpoint         = "https://api.dropboxapi.com/2/auth/token/revoke"
	spaceUsageEndpoint     = "https://api.dropboxapi.com/2/users/get_space_usage"
	currentAccountEndpoint = "https://api.dropboxapi.com/2/users/get_current_account"
)

// pkceVerifierBytes determines the random code_verifier's length before
// base64url encoding — 64 raw bytes encodes to 86 base64url characters
// (no padding), within RFC 7636 / Dropbox's documented 43-128 character
// range.
const pkceVerifierBytes = 64

// httpDoer is the minimal HTTP client surface Client needs.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ssrfChecker validates a URL against an allowlist before Client issues
// any request to it.
type ssrfChecker func(rawURL string) (*url.URL, error)

// Client is a Dropbox OAuth2 PKCE + API client. The zero value is not
// usable; construct with New.
type Client struct {
	httpClient httpDoer
	ssrf       ssrfChecker
}

// New constructs a Client.
func New(httpClient httpDoer, ssrf ssrfChecker) *Client {
	return &Client{httpClient: httpClient, ssrf: ssrf}
}

// PKCEState holds one in-progress PKCE authorization's verifier, kept
// server-side (in-memory, per SPEC-v0.7 §3's transient-flow-state
// decision — see notes/v07-storage.md) between AuthorizeURL and
// ExchangeCode.
type PKCEState struct {
	CodeVerifier string
}

// NewPKCEState generates a fresh random code_verifier.
func NewPKCEState() (PKCEState, error) {
	buf := make([]byte, pkceVerifierBytes)
	if _, err := rand.Read(buf); err != nil {
		return PKCEState{}, fmt.Errorf("dropbox: generate pkce verifier: %w", err)
	}
	return PKCEState{CodeVerifier: base64.RawURLEncoding.EncodeToString(buf)}, nil
}

// codeChallenge computes the S256 code_challenge for verifier per RFC
// 7636: base64url(sha256(verifier)), no padding.
func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL builds the no-redirect PKCE authorize URL to show the
// user (SPEC-v0.7 §3: token_access_type=offline for a refresh token,
// S256 challenge, no redirect_uri so Dropbox displays the code on-page
// for the user to copy/paste back).
func AuthorizeURL(appKey string, state PKCEState) string {
	q := url.Values{
		"client_id":             {appKey},
		"response_type":         {"code"},
		"token_access_type":     {"offline"},
		"code_challenge":        {codeChallenge(state.CodeVerifier)},
		"code_challenge_method": {"S256"},
	}
	return authorizeURL + "?" + q.Encode()
}

// ExchangeResult is the outcome of a token exchange/refresh call.
type ExchangeResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// ExchangeCode exchanges the user-pasted authorization code for tokens,
// using the PKCE code_verifier instead of a client secret (Dropbox's
// PKCE flow needs no app secret at all).
func (c *Client) ExchangeCode(ctx context.Context, appKey, code string, state PKCEState) (ExchangeResult, error) {
	form := url.Values{
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"client_id":     {appKey},
		"code_verifier": {state.CodeVerifier},
	}
	return c.doTokenRequest(ctx, form)
}

// RefreshAccessToken exchanges a stored refresh token for a fresh access
// token.
func (c *Client) RefreshAccessToken(ctx context.Context, appKey, refreshToken string) (ExchangeResult, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {appKey},
	}
	return c.doTokenRequest(ctx, form)
}

func (c *Client) doTokenRequest(ctx context.Context, form url.Values) (ExchangeResult, error) {
	u, err := c.ssrf(tokenEndpoint)
	if err != nil {
		return ExchangeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("dropbox: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("dropbox: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("dropbox: read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errBody struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(data, &errBody)
		return ExchangeResult{}, &HTTPStatusError{Status: resp.StatusCode, ErrorTag: errBody.Error}
	}

	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return ExchangeResult{}, fmt.Errorf("dropbox: parse token response: %w", err)
	}
	return ExchangeResult{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken, ExpiresIn: body.ExpiresIn}, nil
}

// RevokeToken revokes the calling access token (auth/token/revoke).
func (c *Client) RevokeToken(ctx context.Context, accessToken string) error {
	u, err := c.ssrf(revokeEndpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dropbox: build revoke request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dropbox: revoke request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return &HTTPStatusError{Status: resp.StatusCode}
	}
	return nil
}

// SpaceUsage is the parsed users/get_space_usage response.
type SpaceUsage struct {
	UsedBytes uint64
	// AllocatedBytes is the individual or team allocation total. 0 is
	// not a documented case for Dropbox (every plan has a numeric
	// allocation), so Unlimited is never set for Dropbox.
	AllocatedBytes uint64
	AllocationTag  string // "individual" or "team"
}

// FetchSpaceUsage calls users/get_space_usage with accessToken.
func (c *Client) FetchSpaceUsage(ctx context.Context, accessToken string) (SpaceUsage, error) {
	var body struct {
		Used       uint64 `json:"used"`
		Allocation struct {
			Tag       string `json:".tag"`
			Allocated uint64 `json:"allocated"`
			Used      uint64 `json:"used"`
		} `json:"allocation"`
	}
	if err := c.callAPI(ctx, spaceUsageEndpoint, accessToken, &body); err != nil {
		return SpaceUsage{}, err
	}
	return SpaceUsage{
		UsedBytes:      body.Used,
		AllocatedBytes: body.Allocation.Allocated,
		AllocationTag:  body.Allocation.Tag,
	}, nil
}

// AccountInfo is the parsed users/get_current_account response subset
// this package needs.
type AccountInfo struct {
	Email       string
	DisplayName string
}

// FetchCurrentAccount calls users/get_current_account with accessToken.
func (c *Client) FetchCurrentAccount(ctx context.Context, accessToken string) (AccountInfo, error) {
	var body struct {
		Email string `json:"email"`
		Name  struct {
			DisplayName string `json:"display_name"`
		} `json:"name"`
	}
	if err := c.callAPI(ctx, currentAccountEndpoint, accessToken, &body); err != nil {
		return AccountInfo{}, err
	}
	return AccountInfo{Email: body.Email, DisplayName: body.Name.DisplayName}, nil
}

// callAPI issues an authenticated POST with an empty JSON body (as
// Dropbox's API v2 endpoints that take no arguments require a
// "null"/empty JSON body and a matching Content-Type) and decodes the
// JSON response into out.
func (c *Client) callAPI(ctx context.Context, rawURL, accessToken string, out any) error {
	u, err := c.ssrf(rawURL)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader("null"))
	if err != nil {
		return fmt.Errorf("dropbox: build api request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dropbox: api request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("dropbox: read api response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		retryAfter := ""
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter = resp.Header.Get("Retry-After")
		}
		return &HTTPStatusError{Status: resp.StatusCode, RetryAfter: retryAfter}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("dropbox: parse api response: %w", err)
	}
	return nil
}

// HTTPStatusError carries an HTTP response's status/error classification
// for Classify to inspect, without ever needing to log the body itself.
type HTTPStatusError struct {
	Status     int
	ErrorTag   string // OAuth error tag ("invalid_grant" etc.), token endpoint only
	RetryAfter string // Retry-After header value, 429 only
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("dropbox: http status %d", e.Status)
}
