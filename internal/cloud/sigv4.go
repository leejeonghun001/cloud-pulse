// Package cloud implements cloud object storage bucket collectors
// (Amazon S3 via CloudWatch, Cloudflare R2 via GraphQL) and a
// hand-written AWS Signature Version 4 request signer.
package cloud

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Credentials holds the AWS access key pair (and optional session
// token for temporary credentials) used to sign requests.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// dateFormat is the ISO 8601 basic format used for the X-Amz-Date
// header and credential scope timestamps.
const amzDateFormat = "20060102T150405Z"

// dateStampFormat is the credential scope date (YYYYMMDD).
const dateStampFormat = "20060102"

// SignV4 signs req in place using AWS Signature Version 4: it sets
// X-Amz-Date, X-Amz-Security-Token (when c.SessionToken is set),
// X-Amz-Content-Sha256 (only when service is "s3"), and Authorization.
// body is the exact request payload used to compute the payload hash;
// req.Body is not read. t is the signing timestamp (normally
// time.Now().UTC()).
func SignV4(req *http.Request, body []byte, c Credentials, region, service string, t time.Time) error {
	if req == nil {
		return fmt.Errorf("cloud: sign v4: nil request")
	}
	t = t.UTC()
	amzDate := t.Format(amzDateFormat)
	dateStamp := t.Format(dateStampFormat)

	req.Header.Set("X-Amz-Date", amzDate)
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}

	payloadHash := hashHex(body)
	if service == "s3" {
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	}

	if req.Host == "" {
		req.Host = req.URL.Host
	}

	canonicalHeaders, signedHeaders := canonicalizeHeaders(req)
	canonicalURI := canonicalURIPath(req.URL.Path, service)
	canonicalQuery := canonicalQueryString(req.URL.RawQuery)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hashHex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(c.SecretAccessKey, dateStamp, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	authHeader := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.AccessKeyID, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", authHeader)

	return nil
}

// hashHex returns the lowercase hex SHA-256 digest of b.
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hmacSHA256 computes HMAC-SHA256(key, data).
func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// deriveSigningKey derives the SigV4 signing key from the secret
// access key, date stamp, region, and service.
func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

// canonicalizeHeaders builds the canonical headers block and the
// semicolon-separated signed headers list from req's headers plus the
// Host header. Header names are lowercased, sorted, and values are
// trimmed with internal whitespace runs collapsed to a single space;
// duplicate header values are joined with commas in their original
// order.
func canonicalizeHeaders(req *http.Request) (canonicalHeaders, signedHeaders string) {
	values := map[string][]string{}
	values["host"] = []string{req.Host}

	for name, vals := range req.Header {
		lower := strings.ToLower(name)
		for _, v := range vals {
			values[lower] = append(values[lower], collapseSpaces(strings.TrimSpace(v)))
		}
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	var headerLines []string
	for _, name := range names {
		headerLines = append(headerLines, name+":"+strings.Join(values[name], ",")+"\n")
	}

	return strings.Join(headerLines, ""), strings.Join(names, ";")
}

// collapseSpaces converts runs of whitespace into a single space,
// matching the SigV4 canonical header value normalization rule.
func collapseSpaces(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// canonicalURIPath returns the URI-encoded canonical path for use in
// the canonical request. For S3, the path is encoded once (object
// keys may legitimately contain characters like '/'); for all other
// services, it is double-encoded per the SigV4 spec.
func canonicalURIPath(path string, service string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		encoded := uriEncode(seg, false)
		if service != "s3" {
			encoded = uriEncode(encoded, false)
		}
		segments[i] = encoded
	}
	joined := strings.Join(segments, "/")
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return joined
}

// canonicalQueryString builds the canonical query string: parameters
// URI-encoded individually and sorted by encoded key, then by encoded
// value for duplicate keys.
func canonicalQueryString(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			continue
		}
		var k, v string
		if idx := strings.IndexByte(part, '='); idx >= 0 {
			k, v = part[:idx], part[idx+1:]
		} else {
			k, v = part, ""
		}
		dk, err := url.QueryUnescape(k)
		if err != nil {
			dk = k
		}
		dv, err := url.QueryUnescape(v)
		if err != nil {
			dv = v
		}
		pairs = append(pairs, kv{uriEncode(dk, true), uriEncode(dv, true)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

// uriEncode percent-encodes s per RFC 3986 as required by SigV4:
// unreserved characters (A-Z a-z 0-9 - _ . ~) pass through unescaped,
// everything else (including space, encoded as %20) is percent-encoded
// with uppercase hex digits. When encodeSlash is false, '/' is left
// unescaped (used for URI path segments, which are already
// slash-split).
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case isUnreserved(c):
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// isUnreserved reports whether c is an RFC 3986 unreserved character.
func isUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '-' || c == '_' || c == '.' || c == '~':
		return true
	default:
		return false
	}
}
