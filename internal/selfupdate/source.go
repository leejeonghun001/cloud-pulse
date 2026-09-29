// Package selfupdate implements the `update` subcommand shared by
// cloud-pulse-hub and cloud-pulse-agent: checking GitHub Releases for a
// newer version, downloading and checksum-verifying the matching asset,
// and atomically replacing the running binary. It imports only the
// standard library and internal/version.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// defaultLatestURL is GitHub's "latest release" redirect endpoint, which
// answers with a 302 to .../releases/tag/<tag> and is not subject to the
// GitHub API's rate limit.
const defaultLatestURL = "https://github.com/leejeonghun001/cloud-pulse/releases/latest"

// defaultAssetBaseURLTemplate is formatted with the resolved tag to build
// the base URL that release assets and checksums.txt are downloaded
// from.
const defaultAssetBaseURLTemplate = "https://github.com/leejeonghun001/cloud-pulse/releases/download/%s"

// defaultHTTPTimeout bounds every HTTP request Source makes when no
// client is supplied.
const defaultHTTPTimeout = 30 * time.Second

// maxAssetBytes is the maximum size accepted for either checksums.txt or
// the binary asset itself, guarding against a malicious or misconfigured
// server sending an unbounded response body.
const maxAssetBytes = 200 << 20 // 200 MiB

// fetchTimeout bounds the whole Fetch operation (both files).
const fetchTimeout = 5 * time.Minute

// Sentinel errors returned by this package. Callers should use
// errors.Is to detect them rather than matching error text.
var (
	// ErrChecksum indicates a downloaded asset's sha256 did not match
	// the corresponding entry in checksums.txt, or no matching entry
	// was present at all.
	ErrChecksum = errors.New("selfupdate: checksum verification failed")
	// ErrPermission indicates the target binary's directory is not
	// writable by the current process.
	ErrPermission = errors.New("selfupdate: permission denied; run with sudo")
	// ErrUnsupported indicates the current OS/architecture (or binary
	// name) has no corresponding release asset.
	ErrUnsupported = errors.New("selfupdate: unsupported platform")
)

// Source describes where to fetch release information and assets from.
// The zero value is not directly usable for LatestURL/AssetBaseURL
// resolution inside Latest/Fetch; use SourceFromEnv or set fields
// explicitly.
type Source struct {
	// LatestURL is the URL that answers with a redirect to the latest
	// release's tag page. Defaults to defaultLatestURL when empty.
	LatestURL string
	// AssetBaseURL, when non-empty, is used as "<AssetBaseURL>/<asset>"
	// for both checksums.txt and the binary asset, ignoring tag
	// substitution (mirrors the install scripts' CP_RELEASE_BASE_URL
	// semantics). When empty, defaultAssetBaseURLTemplate is formatted
	// with the resolved tag instead.
	AssetBaseURL string
	// Client is the HTTP client used for all requests. A nil Client
	// causes a client with a defaultHTTPTimeout timeout to be used;
	// that client must not follow redirects when used by Latest (see
	// noRedirectClient).
	Client *http.Client
}

// SourceFromEnv builds a Source from environment variables using lookup
// (typically os.LookupEnv, injected so tests never touch real env vars):
//
//   - CP_UPDATE_LATEST_URL overrides LatestURL.
//   - CP_RELEASE_BASE_URL overrides AssetBaseURL.
//
// Both are optional; omitting either keeps this package's defaults.
func SourceFromEnv(lookup func(string) (string, bool)) Source {
	var s Source
	if v, ok := lookup("CP_UPDATE_LATEST_URL"); ok && v != "" {
		s.LatestURL = v
	}
	if v, ok := lookup("CP_RELEASE_BASE_URL"); ok && v != "" {
		s.AssetBaseURL = v
	}
	return s
}

// latestURL returns the effective latest-release URL.
func (s Source) latestURL() string {
	if s.LatestURL != "" {
		return s.LatestURL
	}
	return defaultLatestURL
}

// client returns the effective HTTP client, applying the default timeout
// when none was supplied. The returned client's redirect policy is
// caller-controlled: httpClientNoRedirect wraps it for Latest's use.
func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: defaultHTTPTimeout}
}

// Latest resolves the current latest release tag. It issues a GET to
// LatestURL with redirect-following disabled: a 3xx response's Location
// header is parsed for a "/tag/<tag>" path segment; a 200 response whose
// final request URL already contains "/tag/" is also accepted (in case
// an intermediary followed the redirect itself). The resolved tag must
// pass version.ValidTag, which rejects anything that isn't a
// syntactically clean, bounded-length release tag — this is the specific
// defense against a compromised or malicious redirect target being used
// to build a path/URL later in Fetch.
func (s Source) Latest(ctx context.Context) (string, error) {
	base := s.client()
	noRedirect := &http.Client{
		Timeout:   base.Timeout,
		Transport: base.Transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.latestURL(), nil)
	if err != nil {
		return "", fmt.Errorf("selfupdate: build latest request: %w", err)
	}

	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", fmt.Errorf("selfupdate: fetch latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var tag string
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		tag, err = tagFromPath(loc)
		if err != nil {
			return "", fmt.Errorf("selfupdate: parse redirect location: %w", err)
		}
	} else if resp.StatusCode == http.StatusOK {
		tag, err = tagFromPath(resp.Request.URL.String())
		if err != nil {
			return "", fmt.Errorf("selfupdate: no release tag in response URL: %w", err)
		}
	} else {
		return "", fmt.Errorf("selfupdate: fetch latest release: unexpected status %d", resp.StatusCode)
	}

	if !version.ValidTag(tag) {
		return "", fmt.Errorf("selfupdate: latest release tag %q failed validation", tag)
	}
	return tag, nil
}

// tagFromPath extracts the path segment following the last "/tag/" in
// rawURL. It returns an error if "/tag/" is not present or nothing
// follows it.
func tagFromPath(rawURL string) (string, error) {
	const marker = "/tag/"
	idx := strings.LastIndex(rawURL, marker)
	if idx < 0 {
		return "", fmt.Errorf("no %q segment in %q", marker, rawURL)
	}
	tag := rawURL[idx+len(marker):]
	// Trim anything after a query string or fragment, and any trailing
	// slash, in case the URL has extra segments after the tag.
	if i := strings.IndexAny(tag, "?#/"); i >= 0 {
		tag = tag[:i]
	}
	if tag == "" {
		return "", fmt.Errorf("empty tag segment in %q", rawURL)
	}
	return tag, nil
}

// releaseTargets is the exact set of (GOOS, GOARCH) pairs cloud-pulse
// publishes release assets for.
var releaseTargets = map[string]map[string]bool{
	"linux":   {"amd64": true, "arm64": true, "arm": true},
	"darwin":  {"amd64": true, "arm64": true},
	"windows": {"amd64": true, "arm64": true},
	"freebsd": {"amd64": true},
}

// AssetName returns the release asset filename for binary on the given
// GOOS/GOARCH, matching the naming scheme used by scripts/build-release.sh
// and the install scripts: "cloud-pulse-{hub,agent}-{os}-{arch}[.exe]",
// with GOARCH "arm" (armv7) rendered as "armv7" in the filename. binary
// must be exactly "cloud-pulse-hub" or "cloud-pulse-agent"; goos/goarch
// must be one of the 8 published release targets, else ErrUnsupported.
func AssetName(binary, goos, goarch string) (string, error) {
	if binary != "cloud-pulse-hub" && binary != "cloud-pulse-agent" {
		return "", fmt.Errorf("selfupdate: asset name: unknown binary %q: %w", binary, ErrUnsupported)
	}
	archs, ok := releaseTargets[goos]
	if !ok || !archs[goarch] {
		return "", fmt.Errorf("selfupdate: asset name: unsupported platform %s/%s: %w", goos, goarch, ErrUnsupported)
	}

	archName := goarch
	if goarch == "arm" {
		archName = "armv7"
	}

	name := fmt.Sprintf("%s-%s-%s", binary, goos, archName)
	if goos == "windows" {
		name += ".exe"
	}
	return name, nil
}

// assetBaseURL returns the base URL that asset/checksums.txt downloads
// are joined onto for the given tag.
func (s Source) assetBaseURL(tag string) string {
	if s.AssetBaseURL != "" {
		return strings.TrimSuffix(s.AssetBaseURL, "/")
	}
	return fmt.Sprintf(defaultAssetBaseURLTemplate, tag)
}

// Fetch downloads checksums.txt and the named asset for tag into dir,
// verifies that checksums.txt contains an exact line
// "<64 lowercase hex>  <asset>" (two spaces, sha256sum's default format)
// matching the asset's actual sha256, and returns the path to the
// downloaded (and verified) asset file. Both downloads are capped at
// maxAssetBytes and the whole operation is bounded by fetchTimeout
// regardless of ctx's own deadline. Any temp file created is removed on
// every error path; only a successful return leaves the asset file
// behind (named "<asset>.download" inside dir, so callers can inspect the
// returned path directly without re-deriving a name).
func (s Source) Fetch(ctx context.Context, tag, asset, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	base := s.assetBaseURL(tag)

	checksumsPath := filepath.Join(dir, "checksums.txt.download")
	if err := s.downloadFile(ctx, base+"/checksums.txt", checksumsPath); err != nil {
		return "", fmt.Errorf("selfupdate: fetch checksums.txt: %w", err)
	}
	defer func() { _ = os.Remove(checksumsPath) }()

	wantSum, err := findChecksum(checksumsPath, asset)
	if err != nil {
		return "", err
	}

	assetPath := filepath.Join(dir, asset+".download")
	if err := s.downloadFile(ctx, base+"/"+asset, assetPath); err != nil {
		return "", fmt.Errorf("selfupdate: fetch %s: %w", asset, err)
	}

	gotSum, err := sha256File(assetPath)
	if err != nil {
		_ = os.Remove(assetPath)
		return "", fmt.Errorf("selfupdate: hash %s: %w", asset, err)
	}
	if gotSum != wantSum {
		_ = os.Remove(assetPath)
		return "", fmt.Errorf("%w: %s: expected %s, got %s", ErrChecksum, asset, wantSum, gotSum)
	}

	return assetPath, nil
}

// downloadFile GETs url and writes up to maxAssetBytes+1 bytes of the
// response body to destPath (mode 0644), erroring if the body exceeds
// maxAssetBytes. The destination file is removed if any error occurs
// after it was created.
func (s Source) downloadFile(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d for %s", resp.StatusCode, url)
	}

	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", destPath, err)
	}

	limited := io.LimitReader(resp.Body, maxAssetBytes+1)
	n, copyErr := io.Copy(f, limited)
	closeErr := f.Close()

	if copyErr != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("write body: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("close %s: %w", destPath, closeErr)
	}
	if n > maxAssetBytes {
		_ = os.Remove(destPath)
		return fmt.Errorf("response for %s exceeded %d bytes", url, maxAssetBytes)
	}
	return nil
}

// findChecksum scans checksumsPath for a line of the exact form
// "<64 lowercase hex chars>  <asset>" (sha256sum default two-space
// format; a single space is also accepted for robustness) and returns
// the hex digest. It returns ErrChecksum if no matching line is found.
func findChecksum(checksumsPath, asset string) (string, error) {
	f, err := os.Open(checksumsPath) //nolint:gosec // path is caller-controlled, built from filepath.Join(dir, const)
	if err != nil {
		return "", fmt.Errorf("selfupdate: open checksums.txt: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sum, name := fields[0], fields[1]
		name = strings.TrimPrefix(name, "*") // sha256sum binary-mode marker, if present
		if name != asset {
			continue
		}
		if len(sum) != 64 || !isLowerHex(sum) {
			continue
		}
		return sum, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("selfupdate: read checksums.txt: %w", err)
	}
	return "", fmt.Errorf("%w: no checksums.txt entry for %s", ErrChecksum, asset)
}

// isLowerHex reports whether s consists only of lowercase hex digits.
func isLowerHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// sha256File returns the lowercase hex sha256 digest of the file at
// path.
func sha256File(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path is caller-controlled, built from filepath.Join(dir, const)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// currentGOOSGOARCH returns runtime.GOOS/runtime.GOARCH, extracted so
// tests can exercise AssetName-selection logic paths without relying on
// the actual build target (Run itself always uses the real runtime
// values; only Options-level plumbing is tested this way).
func currentGOOSGOARCH() (string, string) {
	return runtime.GOOS, runtime.GOARCH
}
