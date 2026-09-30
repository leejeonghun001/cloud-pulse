package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// cloudMetaTimeout bounds each individual detection step (DMI read,
// IMDSv2 round-trip, OCI metadata round-trip) — SPEC-v0.6 §1: "각 2초
// 타임아웃".
const cloudMetaTimeout = 2 * time.Second

// awsBoardAssetTagPath is the Nitro-platform DMI file exposing an
// instance's own "i-…" ID directly, no HTTP round-trip needed. Empty or
// absent on non-Nitro/non-AWS hosts.
const awsBoardAssetTagPath = "/sys/class/dmi/id/board_asset_tag"

// awsIMDSBaseURL/ociMetadataBaseURL are overridden in tests via
// cloudMetaOverrides to point at an httptest server instead of the real
// link-local metadata address.
const (
	awsIMDSBaseURL     = "http://169.254.169.254"
	ociMetadataBaseURL = "http://169.254.169.254"
)

// cloudMetaOverrides lets tests substitute the DMI file reader and the
// AWS/OCI metadata base URLs without touching cloudInstanceID's
// signature (which hostinfo.go depends on staying stable — see
// notes/v06-prep.md). Zero value uses the real os.ReadFile and the real
// link-local addresses.
type cloudMetaOverrides struct {
	readFile   func(string) ([]byte, error)
	awsBaseURL string
	ociBaseURL string
	client     *http.Client
}

// cloudMetaTestHook is nil in production; tests set it via
// withCloudMetaOverrides to inject a fake DMI reader/HTTP base URLs.
var cloudMetaTestHook *cloudMetaOverrides

// withCloudMetaOverrides temporarily installs overrides for the
// duration of fn, restoring the previous hook afterward. Not
// goroutine-safe against concurrent tests in the same package, matching
// this codebase's other package-level test-hook conventions (see
// internal/agent's existing tests using similar package vars).
func withCloudMetaOverrides(o cloudMetaOverrides, fn func()) {
	prev := cloudMetaTestHook
	cloudMetaTestHook = &o
	defer func() { cloudMetaTestHook = prev }()
	fn()
}

func currentCloudMetaOverrides() cloudMetaOverrides {
	if cloudMetaTestHook != nil {
		return *cloudMetaTestHook
	}
	return cloudMetaOverrides{}
}

// cloudInstanceID detects the current host's cloud resource identifier
// (SPEC-v0.6 §1's host<->cloud-resource matching), trying in order (each
// step bounded by cloudMetaTimeout, falling through to the next on any
// failure):
//  1. DMI board_asset_tag (AWS Nitro "i-…", Linux only, no network call)
//  2. AWS IMDSv2 (PUT /latest/api/token, then GET /latest/meta-data/
//     instance-id with the token header)
//  3. OCI instance metadata v2 (GET /opc/v2/instance/ with
//     "Authorization: Bearer Oracle", reading the "id" field)
//
// Detection is always best-effort: any failure at any step falls
// through silently (no error is ever returned) since cloud metadata
// detection must never block or fail agent startup/reporting.
func cloudInstanceID(ctx context.Context, provider models.Provider) string {
	ov := currentCloudMetaOverrides()

	if id := dmiInstanceID(ov); id != "" {
		return id
	}

	client := ov.client
	if client == nil {
		client = http.DefaultClient
	}

	if id := imdsInstanceID(ctx, client, imdsBaseURL(ov)); id != "" {
		return id
	}
	if id := ociInstanceID(ctx, client, ociBaseURL(ov)); id != "" {
		return id
	}
	_ = provider // detection tries every step regardless of the configured provider hint
	return ""
}

func imdsBaseURL(ov cloudMetaOverrides) string {
	if ov.awsBaseURL != "" {
		return ov.awsBaseURL
	}
	return awsIMDSBaseURL
}

func ociBaseURL(ov cloudMetaOverrides) string {
	if ov.ociBaseURL != "" {
		return ov.ociBaseURL
	}
	return ociMetadataBaseURL
}

// dmiInstanceID reads awsBoardAssetTagPath directly (Linux only, no
// network call, effectively instant — still conceptually bounded by
// cloudMetaTimeout via the caller's overall detection budget, but a
// local file read never actually blocks long enough to need its own
// context).
func dmiInstanceID(ov cloudMetaOverrides) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	readFile := ov.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	b, err := readFile(awsBoardAssetTagPath)
	if err != nil {
		return ""
	}
	tag := strings.TrimSpace(string(b))
	if strings.HasPrefix(tag, "i-") {
		return tag
	}
	return ""
}

// imdsInstanceID performs the AWS IMDSv2 token + instance-id round trip
// against baseURL, returning "" on any error/non-200 response.
func imdsInstanceID(ctx context.Context, client *http.Client, baseURL string) string {
	callCtx, cancel := context.WithTimeout(ctx, cloudMetaTimeout)
	defer cancel()

	tokenReq, err := http.NewRequestWithContext(callCtx, http.MethodPut, baseURL+"/latest/api/token", nil)
	if err != nil {
		return ""
	}
	tokenReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		return ""
	}
	token, err := readSmallBody(tokenResp)
	if err != nil || tokenResp.StatusCode != http.StatusOK || token == "" {
		return ""
	}

	idReq, err := http.NewRequestWithContext(callCtx, http.MethodGet, baseURL+"/latest/meta-data/instance-id", nil)
	if err != nil {
		return ""
	}
	idReq.Header.Set("X-aws-ec2-metadata-token", token)
	idResp, err := client.Do(idReq)
	if err != nil {
		return ""
	}
	id, err := readSmallBody(idResp)
	if err != nil || idResp.StatusCode != http.StatusOK {
		return ""
	}
	return strings.TrimSpace(id)
}

// ociInstanceID performs the OCI instance metadata v2 round trip
// against baseURL, returning "" on any error/non-200 response or a
// response missing the "id" field.
func ociInstanceID(ctx context.Context, client *http.Client, baseURL string) string {
	callCtx, cancel := context.WithTimeout(ctx, cloudMetaTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, baseURL+"/opc/v2/instance/", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer Oracle")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var payload struct {
		ID string `json:"id"`
	}
	if err := decodeJSONLimited(resp.Body, &payload); err != nil {
		return ""
	}
	return payload.ID
}

// maxCloudMetaBodyBytes bounds how much of a metadata HTTP response body
// is read, defending against a misbehaving/malicious responder on the
// link-local address.
const maxCloudMetaBodyBytes = 64 << 10 // 64 KiB

// readSmallBody reads resp.Body up to maxCloudMetaBodyBytes, always
// closing the body.
func readSmallBody(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxCloudMetaBodyBytes))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeJSONLimited decodes JSON from r, capped at maxCloudMetaBodyBytes,
// into v.
func decodeJSONLimited(r io.Reader, v any) error {
	return json.NewDecoder(io.LimitReader(r, maxCloudMetaBodyBytes)).Decode(v)
}
