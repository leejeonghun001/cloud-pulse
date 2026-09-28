package cloud

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// callTimeout bounds every individual CloudWatch HTTP call.
const callTimeout = 30 * time.Second

// maxQueriesPerCall is the maximum number of MetricDataQuery entries
// GetMetricData accepts in a single call.
const maxQueriesPerCall = 500

// Dimension is a single CloudWatch metric dimension name/value pair.
type Dimension struct {
	Name  string
	Value string
}

// Metric identifies a CloudWatch metric by namespace, name, and
// dimensions.
type Metric struct {
	Namespace  string
	MetricName string
	Dimensions []Dimension
}

// MetricQuery is one entry of a GetMetricData request: a metric
// statistic to retrieve, identified by Id (used to correlate results).
type MetricQuery struct {
	ID     string
	Metric Metric
	Period int
	Stat   string
}

// Datapoint is a single timestamped value from a MetricDataResult.
type Datapoint struct {
	Timestamp time.Time
	Value     float64
}

// APIError is returned when CloudWatch responds with an XML
// ErrorResponse or a non-2xx HTTP status.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("cloud: cloudwatch: %s (%s, status %d)", e.Message, e.Code, e.StatusCode)
}

// CloudWatch is a minimal client for the CloudWatch GetMetricData
// Query API (POST form, XML response), signed with hand-written
// SigV4.
type CloudWatch struct {
	// Region is the AWS region CloudWatch metrics are read from.
	Region string
	// Creds are the credentials used to sign requests.
	Creds Credentials
	// Client is the HTTP client used for requests. If nil,
	// http.DefaultClient is used.
	Client *http.Client
	// Endpoint overrides the default
	// https://monitoring.<region>.amazonaws.com/ endpoint; used in
	// tests.
	Endpoint string
	// Now returns the current time, used as the SigV4 signing
	// timestamp. If nil, time.Now is used.
	Now func() time.Time
}

// httpClient returns c.Client, or http.DefaultClient if unset.
func (c *CloudWatch) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

// now returns c.Now(), or time.Now() if unset.
func (c *CloudWatch) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// endpoint returns c.Endpoint, or the default regional CloudWatch
// endpoint if unset.
func (c *CloudWatch) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return fmt.Sprintf("https://monitoring.%s.amazonaws.com/", c.Region)
}

// GetMetricData retrieves datapoints for queries over [start, end],
// keyed by MetricQuery.ID. Queries are chunked into batches of at most
// 500 per call, and each call is paginated via NextToken until
// exhausted.
func (c *CloudWatch) GetMetricData(ctx context.Context, queries []MetricQuery, start, end time.Time) (map[string][]Datapoint, error) {
	results := make(map[string][]Datapoint)

	for i := 0; i < len(queries); i += maxQueriesPerCall {
		chunk := queries[i:min(i+maxQueriesPerCall, len(queries))]
		if err := c.fetchChunk(ctx, chunk, start, end, results); err != nil {
			return nil, err
		}
	}

	return results, nil
}

// fetchChunk fetches all pages for a single batch of at most 500
// queries, merging datapoints into results.
func (c *CloudWatch) fetchChunk(ctx context.Context, chunk []MetricQuery, start, end time.Time, results map[string][]Datapoint) error {
	nextToken := ""
	for {
		resp, err := c.call(ctx, chunk, start, end, nextToken)
		if err != nil {
			return err
		}
		for _, mr := range resp.MetricDataResults {
			for i, ts := range mr.Timestamps {
				if i >= len(mr.Values) {
					break
				}
				results[mr.ID] = append(results[mr.ID], Datapoint{Timestamp: ts.Time, Value: mr.Values[i]})
			}
		}
		if resp.NextToken == "" {
			return nil
		}
		nextToken = resp.NextToken
	}
}

// call issues one GetMetricData POST request and decodes the XML
// response, translating ErrorResponse bodies and non-2xx statuses into
// *APIError.
func (c *CloudWatch) call(ctx context.Context, queries []MetricQuery, start, end time.Time, nextToken string) (*getMetricDataResult, error) {
	form := buildForm(queries, start, end, nextToken)
	body := []byte(form.Encode())

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cloud: cloudwatch: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")

	if err := SignV4(req, body, c.Creds, c.Region, "monitoring", c.now()); err != nil {
		return nil, fmt.Errorf("cloud: cloudwatch: sign request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: cloudwatch: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // best-effort close; body already fully read below

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloud: cloudwatch: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp errorResponse
		if xmlErr := xml.Unmarshal(respBody, &errResp); xmlErr == nil && errResp.Error.Code != "" {
			return nil, &APIError{StatusCode: resp.StatusCode, Code: errResp.Error.Code, Message: errResp.Error.Message}
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Code: "Unknown", Message: truncate(string(respBody), 512)}
	}

	var out getMetricDataResponse
	if err := xml.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("cloud: cloudwatch: decode response: %w", err)
	}
	return &out.Result, nil
}

// buildForm constructs the POST form body for a GetMetricData call.
func buildForm(queries []MetricQuery, start, end time.Time, nextToken string) url.Values {
	form := url.Values{}
	form.Set("Action", "GetMetricData")
	form.Set("Version", "2010-08-01")
	form.Set("StartTime", start.UTC().Format(time.RFC3339))
	form.Set("EndTime", end.UTC().Format(time.RFC3339))
	form.Set("ScanBy", "TimestampDescending")
	if nextToken != "" {
		form.Set("NextToken", nextToken)
	}

	for qi, q := range queries {
		prefix := fmt.Sprintf("MetricDataQueries.member.%d.", qi+1)
		form.Set(prefix+"Id", q.ID)
		form.Set(prefix+"MetricStat.Metric.Namespace", q.Metric.Namespace)
		form.Set(prefix+"MetricStat.Metric.MetricName", q.Metric.MetricName)
		for di, d := range q.Metric.Dimensions {
			dPrefix := fmt.Sprintf("%sMetricStat.Metric.Dimensions.member.%d.", prefix, di+1)
			form.Set(dPrefix+"Name", d.Name)
			form.Set(dPrefix+"Value", d.Value)
		}
		form.Set(prefix+"MetricStat.Period", strconv.Itoa(q.Period))
		form.Set(prefix+"MetricStat.Stat", q.Stat)
		form.Set(prefix+"ReturnData", "true")
	}

	return form
}

// truncate shortens s to at most n bytes, appending an ellipsis marker
// if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// getMetricDataResponse is the XML envelope for a GetMetricDataResult.
type getMetricDataResponse struct {
	XMLName xml.Name            `xml:"GetMetricDataResponse"`
	Result  getMetricDataResult `xml:"GetMetricDataResult"`
}

// getMetricDataResult holds the per-metric results and pagination
// token from a GetMetricData call.
type getMetricDataResult struct {
	MetricDataResults []metricDataResultXML `xml:"MetricDataResults>member"`
	NextToken         string                `xml:"NextToken"`
}

// metricDataResultXML is one <member> of MetricDataResults: the
// query's Id plus parallel Timestamps/Values slices.
type metricDataResultXML struct {
	ID         string    `xml:"Id"`
	Label      string    `xml:"Label"`
	StatusCode string    `xml:"StatusCode"`
	Timestamps []xmlTime `xml:"Timestamps>member"`
	Values     []float64 `xml:"Values>member"`
}

// xmlTime decodes an ISO 8601 CloudWatch timestamp element into a
// time.Time.
type xmlTime struct {
	time.Time
}

// UnmarshalText implements encoding.TextUnmarshaler for xmlTime,
// parsing RFC3339 timestamps as returned by CloudWatch.
func (t *xmlTime) UnmarshalText(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("cloud: cloudwatch: parse timestamp %q: %w", s, err)
	}
	t.Time = parsed
	return nil
}

// errorResponse is the XML shape of a CloudWatch error:
// <ErrorResponse><Error><Code>/<Message></Error></ErrorResponse>.
type errorResponse struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Error   struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}
