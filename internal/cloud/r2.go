package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// defaultR2Endpoint is the Cloudflare GraphQL Analytics API endpoint.
// https://developers.cloudflare.com/analytics/graphql-api/
const defaultR2Endpoint = "https://api.cloudflare.com/client/v4/graphql"

// r2CallTimeout bounds the single GraphQL POST issued per Collect
// call.
const r2CallTimeout = 30 * time.Second

// r2StorageLookback bounds the r2StorageAdaptiveGroups query window;
// storage datapoints are daily, so 2 days ensures at least one
// datapoint even across a reporting delay.
const r2StorageLookback = 2 * 24 * time.Hour

// r2ClassAActions and r2ClassBActions classify Cloudflare R2 GraphQL
// actionType values into Class A / Class B operations per the R2
// pricing page. DeleteObject/DeleteBucket/AbortMultipartUpload are
// free and intentionally omitted (never billed, never counted).
// https://developers.cloudflare.com/r2/pricing/#class-a-operations
// https://developers.cloudflare.com/r2/pricing/#class-b-operations
var r2ClassAActions = map[string]bool{
	"ListBuckets":                     true,
	"PutBucket":                       true,
	"ListObjects":                     true,
	"PutObject":                       true,
	"CopyObject":                      true,
	"CompleteMultipartUpload":         true,
	"CreateMultipartUpload":           true,
	"LifecycleStorageTierTransition":  true,
	"ListMultipartUploads":            true,
	"UploadPart":                      true,
	"UploadPartCopy":                  true,
	"ListParts":                       true,
	"PutBucketEncryption":             true,
	"PutBucketCors":                   true,
	"PutBucketLifecycleConfiguration": true,
}

// r2ClassBActions lists Cloudflare R2 Class B (read) actionType
// values.
var r2ClassBActions = map[string]bool{
	"HeadBucket":                      true,
	"HeadObject":                      true,
	"GetObject":                       true,
	"UsageSummary":                    true,
	"GetBucketEncryption":             true,
	"GetBucketLocation":               true,
	"GetBucketCors":                   true,
	"GetBucketLifecycleConfiguration": true,
}

// r2FreeActions lists actionType values that are never billed
// (DeleteObject, DeleteBucket, AbortMultipartUpload).
var r2FreeActions = map[string]bool{
	"DeleteObject":         true,
	"DeleteBucket":         true,
	"AbortMultipartUpload": true,
}

// r2ActionClass classifies actionType into "a", "b", or "" (free /
// unknown).
func r2ActionClass(actionType string) string {
	switch {
	case r2ClassAActions[actionType]:
		return "a"
	case r2ClassBActions[actionType]:
		return "b"
	case r2FreeActions[actionType]:
		return ""
	default:
		return ""
	}
}

// R2Collector implements the hub BucketCollector interface for
// Cloudflare R2, using the GraphQL Analytics API
// (r2OperationsAdaptiveGroups, r2StorageAdaptiveGroups).
// https://developers.cloudflare.com/r2/platform/metrics-analytics/
type R2Collector struct {
	// AccountID is the Cloudflare account ID metrics are queried
	// under.
	AccountID string
	// APIToken is the Cloudflare API token (Bearer auth) used to
	// query the GraphQL Analytics API.
	APIToken string
	// Buckets restricts collection to the named buckets. If empty,
	// all buckets seen in the operations/storage query results are
	// collected.
	Buckets []string
	// Endpoint overrides the GraphQL endpoint; defaults to
	// https://api.cloudflare.com/client/v4/graphql.
	Endpoint string
	// Client is the HTTP client used for the GraphQL request. If
	// nil, http.DefaultClient is used.
	Client *http.Client
	// Now returns the current time. If nil, time.Now is used.
	Now func() time.Time
	// Window is the trailing collection window used for
	// RequestsWindow/WindowSeconds. Defaults to 15 minutes.
	Window time.Duration
}

// Name returns the collector name, "r2".
func (c *R2Collector) Name() string { return "r2" }

// Collect queries the Cloudflare GraphQL Analytics API once for
// operations (month-to-date and trailing window) and storage, and
// assembles BucketStats for every discovered or configured bucket.
func (c *R2Collector) Collect(ctx context.Context) ([]models.BucketStats, error) {
	now := c.now()
	window := c.window()
	monthStart := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	windowStart := now.Add(-window)
	storageStart := now.Add(-r2StorageLookback)

	resp, err := c.query(ctx, r2Variables{
		AccountTag:   c.AccountID,
		MTDStart:     monthStart.UTC().Format(time.RFC3339),
		WindowStart:  windowStart.UTC().Format(time.RFC3339),
		StorageStart: storageStart.UTC().Format(time.RFC3339),
		End:          now.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, fmt.Errorf("cloud: r2: query: %w", err)
	}

	bucketSet := map[string]bool{}
	for _, name := range c.Buckets {
		bucketSet[name] = true
	}
	filter := len(bucketSet) > 0

	type acc struct {
		classA, classB    uint64
		requestsWindow    uint64
		size, objects     uint64
		haveStorage       bool
		haveOperationsMTD bool
	}
	buckets := map[string]*acc{}
	get := func(name string) *acc {
		a, ok := buckets[name]
		if !ok {
			a = &acc{}
			buckets[name] = a
		}
		return a
	}

	viewer := resp.Data.Viewer
	if len(viewer.Accounts) == 0 {
		return nil, fmt.Errorf("cloud: r2: query: no account data returned for account %q", c.AccountID)
	}
	account := viewer.Accounts[0]

	for _, g := range account.OperationsMTD {
		name := g.Dimensions.BucketName
		if filter && !bucketSet[name] {
			continue
		}
		a := get(name)
		a.haveOperationsMTD = true
		switch r2ActionClass(g.Dimensions.ActionType) {
		case "a":
			a.classA += uint64(g.Sum.Requests)
		case "b":
			a.classB += uint64(g.Sum.Requests)
		}
	}

	for _, g := range account.OperationsWindow {
		name := g.Dimensions.BucketName
		if filter && !bucketSet[name] {
			continue
		}
		get(name).requestsWindow += uint64(g.Sum.Requests)
	}

	// Storage rows are ordered most-recent-first (orderBy datetime_DESC);
	// keep only the first (latest) row seen per bucket.
	for _, g := range account.Storage {
		name := g.Dimensions.BucketName
		if filter && !bucketSet[name] {
			continue
		}
		a := get(name)
		if a.haveStorage {
			continue
		}
		a.haveStorage = true
		a.size = uint64(g.Max.PayloadSize) + uint64(g.Max.MetadataSize)
		a.objects = uint64(g.Max.ObjectCount)
	}

	names := c.Buckets
	if !filter {
		names = make([]string, 0, len(buckets))
		for name := range buckets {
			names = append(names, name)
		}
	}

	out := make([]models.BucketStats, 0, len(names))
	for _, name := range names {
		a := buckets[name]
		if a == nil {
			a = &acc{}
		}
		out = append(out, models.BucketStats{
			Provider:                models.StorageR2,
			Bucket:                  name,
			CollectedAt:             now.Unix(),
			SizeBytes:               a.size,
			ObjectCount:             a.objects,
			ClassAOpsMTD:            a.classA,
			ClassBOpsMTD:            a.classB,
			RequestsWindow:          a.requestsWindow,
			WindowSeconds:           int64(window.Seconds()),
			RequestMetricsAvailable: true,
		})
	}
	return out, nil
}

// now returns c.Now(), or time.Now() if unset.
func (c *R2Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// window returns c.Window, or defaultS3Window (15m) if unset.
func (c *R2Collector) window() time.Duration {
	if c.Window > 0 {
		return c.Window
	}
	return defaultS3Window
}

// endpoint returns c.Endpoint, or defaultR2Endpoint if unset.
func (c *R2Collector) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return defaultR2Endpoint
}

// httpClient returns c.Client, or http.DefaultClient if unset.
func (c *R2Collector) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

// r2Variables are the GraphQL query variables for the combined
// operations+storage query.
type r2Variables struct {
	AccountTag   string
	MTDStart     string
	WindowStart  string
	StorageStart string
	End          string
}

// r2Query is a single GraphQL request combining the operations
// (month-to-date and trailing window) and storage datasets in one
// POST, per
// https://developers.cloudflare.com/r2/platform/metrics-analytics/#query-via-the-graphql-api
const r2Query = `
query R2Metrics($accountTag: string!, $mtdStart: Time!, $windowStart: Time!, $storageStart: Time!, $end: Time!) {
  viewer {
    accounts(filter: { accountTag: $accountTag }) {
      operationsMTD: r2OperationsAdaptiveGroups(
        limit: 10000
        filter: { datetime_geq: $mtdStart, datetime_leq: $end }
      ) {
        sum { requests }
        dimensions { actionType bucketName }
      }
      operationsWindow: r2OperationsAdaptiveGroups(
        limit: 10000
        filter: { datetime_geq: $windowStart, datetime_leq: $end }
      ) {
        sum { requests }
        dimensions { actionType bucketName }
      }
      storage: r2StorageAdaptiveGroups(
        limit: 10000
        filter: { datetime_geq: $storageStart, datetime_leq: $end }
        orderBy: [datetime_DESC]
      ) {
        max { objectCount payloadSize metadataSize }
        dimensions { bucketName datetime }
      }
    }
  }
}`

// r2GraphQLRequest is the JSON body sent to the GraphQL endpoint.
type r2GraphQLRequest struct {
	Query     string      `json:"query"`
	Variables r2Variables `json:"variables"`
}

// MarshalJSON implements a custom encoding so r2Variables' field
// names map to the GraphQL variable names (accountTag, mtdStart,
// etc.) without exposing those lowerCamel names on the exported Go
// struct fields.
func (v r2Variables) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		AccountTag   string `json:"accountTag"`
		MTDStart     string `json:"mtdStart"`
		WindowStart  string `json:"windowStart"`
		StorageStart string `json:"storageStart"`
		End          string `json:"end"`
	}{v.AccountTag, v.MTDStart, v.WindowStart, v.StorageStart, v.End})
}

// r2GraphQLResponse is the JSON envelope returned by the GraphQL
// endpoint, including the `errors` array Cloudflare uses to report
// query-level failures (which can accompany an HTTP 200).
type r2GraphQLResponse struct {
	Data   r2GraphQLData    `json:"data"`
	Errors []r2GraphQLError `json:"errors,omitempty"`
}

// r2GraphQLError is one entry of the GraphQL `errors` array.
type r2GraphQLError struct {
	Message string `json:"message"`
}

// r2GraphQLData is the `data` payload of a successful GraphQL
// response.
type r2GraphQLData struct {
	Viewer r2Viewer `json:"viewer"`
}

// r2Viewer mirrors the `viewer { accounts { ... } }` GraphQL shape.
type r2Viewer struct {
	Accounts []r2Account `json:"accounts"`
}

// r2Account holds the three aliased dataset queries for one account.
type r2Account struct {
	OperationsMTD    []r2OperationsGroup `json:"operationsMTD"`
	OperationsWindow []r2OperationsGroup `json:"operationsWindow"`
	Storage          []r2StorageGroup    `json:"storage"`
}

// r2OperationsGroup is one row of r2OperationsAdaptiveGroups.
type r2OperationsGroup struct {
	Sum        r2OperationsSum        `json:"sum"`
	Dimensions r2OperationsDimensions `json:"dimensions"`
}

// r2OperationsSum holds the summed `requests` field.
type r2OperationsSum struct {
	Requests float64 `json:"requests"`
}

// r2OperationsDimensions holds the grouping dimensions for an
// operations row.
type r2OperationsDimensions struct {
	ActionType string `json:"actionType"`
	BucketName string `json:"bucketName"`
}

// r2StorageGroup is one row of r2StorageAdaptiveGroups.
type r2StorageGroup struct {
	Max        r2StorageMax        `json:"max"`
	Dimensions r2StorageDimensions `json:"dimensions"`
}

// r2StorageMax holds the max-aggregated storage fields.
type r2StorageMax struct {
	ObjectCount  float64 `json:"objectCount"`
	PayloadSize  float64 `json:"payloadSize"`
	MetadataSize float64 `json:"metadataSize"`
}

// r2StorageDimensions holds the grouping dimensions for a storage row.
type r2StorageDimensions struct {
	BucketName string `json:"bucketName"`
	Datetime   string `json:"datetime"`
}

// query issues the single combined GraphQL POST and decodes the
// response, translating HTTP errors, non-2xx statuses, and the
// GraphQL `errors` array into a returned error. The API token is
// never included in the returned error text.
func (c *R2Collector) query(ctx context.Context, vars r2Variables) (*r2GraphQLResponse, error) {
	reqBody := r2GraphQLRequest{Query: r2Query, Variables: vars}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("cloud: r2: encode request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, r2CallTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("cloud: r2: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIToken)

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("cloud: r2: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // best-effort close; body fully read below

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloud: r2: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cloud: r2: http status %d: %s", resp.StatusCode, truncate(string(body), 512))
	}

	var out r2GraphQLResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("cloud: r2: decode response: %w", err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("cloud: r2: graphql error: %s", out.Errors[0].Message)
	}
	return &out, nil
}
