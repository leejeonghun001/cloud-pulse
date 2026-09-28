package cloud

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// testCreds are the well-known, non-secret AWS documentation example
// credentials used by the official SigV4 test suite. AKIDEXAMPLE is
// used here; the repo's secret-scanner heuristic flags the other
// well-known AWS docs placeholder access key ID (the one starting
// "AKIAIOSFODNN7"), so that value is avoided even in comments.
var testCreds = Credentials{
	AccessKeyID:     "AKIDEXAMPLE",
	SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
}

// mustParseTestTime parses the fixed SigV4 test-suite timestamp used
// by every vector below (20150830T123600Z).
func mustParseTestTime(t *testing.T) time.Time {
	t.Helper()
	ts, err := time.Parse(amzDateFormat, "20150830T123600Z")
	if err != nil {
		t.Fatalf("parse test time: %v", err)
	}
	return ts
}

// TestSignV4_AWSTestSuiteVectors reproduces official AWS SigV4 test
// suite vectors (credentials AKIDEXAMPLE, region us-east-1, service
// "service", date 20150830T123600Z). Expected values sourced from the
// AWS documentation test suite, mirrored at
// https://raw.githubusercontent.com/saibotsivad/aws-sig-v4-test-suite/master/index.json
// (see notes/cloud.md for citation).
func TestSignV4_AWSTestSuiteVectors(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		rawURL        string
		extraHeaders  map[string]string
		body          string
		wantSignature string
		wantSigned    string
	}{
		{
			name:          "get-vanilla",
			method:        http.MethodGet,
			rawURL:        "https://example.amazonaws.com/",
			wantSignature: "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31",
			wantSigned:    "host;x-amz-date",
		},
		{
			name:          "get-vanilla-query-order-key-case",
			method:        http.MethodGet,
			rawURL:        "https://example.amazonaws.com/?Param2=value2&Param1=value1",
			wantSignature: "b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500",
			wantSigned:    "host;x-amz-date",
		},
		{
			name:   "post-x-www-form-urlencoded",
			method: http.MethodPost,
			rawURL: "https://example.amazonaws.com/",
			extraHeaders: map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
			},
			body:          "Param1=value1",
			wantSignature: "ff11897932ad3f4e8b18135d722051e5ac45fc38421b1da7b9d196a0fe09473a",
			wantSigned:    "content-type;host;x-amz-date",
		},
		{
			name:          "get-vanilla-query-order-key",
			method:        http.MethodGet,
			rawURL:        "https://example.amazonaws.com/?Param1=value2&Param1=Value1",
			wantSignature: "eedbc4e291e521cf13422ffca22be7d2eb8146eecf653089df300a15b2382bd1",
			wantSigned:    "host;x-amz-date",
		},
		{
			name:          "get-vanilla-empty-query-key",
			method:        http.MethodGet,
			rawURL:        "https://example.amazonaws.com/?Param1=value1",
			wantSignature: "a67d582fa61cc504c4bae71f336f98b97f1ea3c7a6bfe1b6e45aec72011b9aeb",
			wantSigned:    "host;x-amz-date",
		},
	}

	ts := mustParseTestTime(t)

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var body []byte
			if tc.body != "" {
				body = []byte(tc.body)
			}
			req, err := http.NewRequest(tc.method, tc.rawURL, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			for k, v := range tc.extraHeaders {
				req.Header.Set(k, v)
			}

			if err := SignV4(req, body, testCreds, "us-east-1", "service", ts); err != nil {
				t.Fatalf("SignV4: %v", err)
			}

			auth := req.Header.Get("Authorization")
			if !strings.Contains(auth, "Signature="+tc.wantSignature) {
				t.Errorf("signature mismatch:\n got: %s\nwant signature: %s", auth, tc.wantSignature)
			}
			if !strings.Contains(auth, "SignedHeaders="+tc.wantSigned) {
				t.Errorf("signed headers mismatch:\n got: %s\nwant signed headers: %s", auth, tc.wantSigned)
			}
			if got := req.Header.Get("X-Amz-Date"); got != "20150830T123600Z" {
				t.Errorf("X-Amz-Date = %q, want 20150830T123600Z", got)
			}
			if req.Header.Get("X-Amz-Content-Sha256") != "" {
				t.Errorf("X-Amz-Content-Sha256 should be absent for non-s3 service")
			}
		})
	}
}

// TestSignV4_SessionToken reproduces the AWS SigV4 test suite's STS
// vectors (post-sts-header-before), confirming X-Amz-Security-Token is
// set and included in SignedHeaders when a session token is present.
func TestSignV4_SessionToken(t *testing.T) {
	t.Parallel()

	const stsToken = "AQoDYXdzEPT//////////wEXAMPLEtc764bNrC9SAPBSM22wDOk4x4HIZ8j4FZTwdQWLWsKWHGBuFqwAeMicRXmxfpSPfIeoIYRqTflfKD8YUuwthAx7mSEI/qkPpKPi/kMcGdQrmGdeehM4IC1NtBmUpp2wUE8phUZampKsburEDy0KPkyQDYwT7WZ0wq5VSXDvp75YU9HFvlRd8Tx6q6fE8YQcHNVXAkiY9q6d+xo0rKwT38xVqr7ZD0u0iPPkUL64lIZbqBAz+scqKmlzm8FDrypNC9Yjc8fPOLn9FX9KSYvKTr4rvx3iSIlTJabIQwj2ICCR/oLxBA=="
	const wantSignature = "85d96828115b5dc0cfc3bd16ad9e210dd772bbebba041836c64533a82be05ead"

	creds := testCreds
	creds.SessionToken = stsToken

	ts := mustParseTestTime(t)
	req, err := http.NewRequest(http.MethodPost, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if err := SignV4(req, nil, creds, "us-east-1", "service", ts); err != nil {
		t.Fatalf("SignV4: %v", err)
	}

	if got := req.Header.Get("X-Amz-Security-Token"); got != stsToken {
		t.Errorf("X-Amz-Security-Token missing or wrong")
	}
	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "SignedHeaders=host;x-amz-date;x-amz-security-token") {
		t.Errorf("signed headers mismatch: %s", auth)
	}
	if !strings.Contains(auth, "Signature="+wantSignature) {
		t.Errorf("signature mismatch:\n got: %s\nwant signature: %s", auth, wantSignature)
	}
}

// TestSignV4_IAMListUsersExample reproduces the classic AWS
// documentation "create a signed request" example: a GET to
// iam.amazonaws.com with Action=ListUsers&Version=2010-05-08, service
// "iam", region us-east-1, date 20150830T123600Z, producing signature
// 5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7.
func TestSignV4_IAMListUsersExample(t *testing.T) {
	t.Parallel()

	const wantSignature = "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"

	ts := mustParseTestTime(t)
	req, err := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")

	if err := SignV4(req, nil, testCreds, "us-east-1", "iam", ts); err != nil {
		t.Fatalf("SignV4: %v", err)
	}

	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "Signature="+wantSignature) {
		t.Errorf("signature mismatch:\n got: %s\nwant signature: %s", auth, wantSignature)
	}
	wantSigned := "content-type;host;x-amz-date"
	if !strings.Contains(auth, "SignedHeaders="+wantSigned) {
		t.Errorf("signed headers mismatch: %s", auth)
	}
}

// TestSignV4_S3ContentSha256 verifies X-Amz-Content-Sha256 is set only
// for service "s3", using the empty-payload hash constant.
func TestSignV4_S3ContentSha256(t *testing.T) {
	t.Parallel()

	ts := mustParseTestTime(t)
	req, err := http.NewRequest(http.MethodGet, "https://s3.amazonaws.com/bucket/key", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if err := SignV4(req, nil, testCreds, "us-east-1", "s3", ts); err != nil {
		t.Fatalf("SignV4: %v", err)
	}

	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := req.Header.Get("X-Amz-Content-Sha256"); got != emptyHash {
		t.Errorf("X-Amz-Content-Sha256 = %q, want %q", got, emptyHash)
	}
}
