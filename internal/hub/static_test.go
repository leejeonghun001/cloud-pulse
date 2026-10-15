package hub

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/leejeonghun001/cloud-pulse/web"
)

func TestStaticETagGzipAndSPAFallback(t *testing.T) {
	t.Parallel()
	assets := fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("<html>" + strings.Repeat("dashboard ", 300) + "</html>")},
		"assets/app.css":   &fstest.MapFile{Data: []byte(strings.Repeat("body { color: black; }\n", 200))},
		"assets/icon.png":  &fstest.MapFile{Data: []byte{0x89, 0x50, 0x4e, 0x47}},
		"assets/readme.md": &fstest.MapFile{Data: []byte(strings.Repeat("content\n", 300))},
	}
	s := New(testOptions(), newFakeStore(), nil, nil, assets, testLogger())

	req := newStaticRequest(t, http.MethodGet, "/assets/app.css", "gzip")
	rec := serveStatic(t, s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("gzip status = %d, want 200", rec.Code)
	}
	gzipETag := rec.Header().Get("ETag")
	if gzipETag == "" || !strings.HasSuffix(gzipETag, `-gz"`) {
		t.Errorf("gzip ETag = %q, want a distinct -gz ETag", gzipETag)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	reader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if !strings.Contains(string(decoded), "color") {
		t.Error("gzip body did not decode to CSS")
	}

	plain := serveStatic(t, s, newStaticRequest(t, http.MethodGet, "/assets/app.css", ""))
	if plain.Code != http.StatusOK || plain.Header().Get("Content-Encoding") != "" {
		t.Errorf("plain response = status %d encoding %q, want 200 without encoding", plain.Code, plain.Header().Get("Content-Encoding"))
	}
	if got := plain.Header().Get("ETag"); got == gzipETag || got == "" {
		t.Errorf("plain ETag = %q, want a distinct non-empty tag", got)
	}

	conditional := newStaticRequest(t, http.MethodGet, "/assets/app.css", "gzip")
	conditional.Header.Set("If-None-Match", gzipETag)
	if rec := serveStatic(t, s, conditional); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("conditional gzip = status %d body %d bytes, want 304 empty", rec.Code, rec.Body.Len())
	}

	deepLink := serveStatic(t, s, newStaticRequest(t, http.MethodGet, "/settings/network", ""))
	if deepLink.Code != http.StatusOK || deepLink.Header().Get("ETag") == "" || !strings.Contains(deepLink.Body.String(), "dashboard") {
		t.Errorf("SPA fallback = status %d etag %q body %q", deepLink.Code, deepLink.Header().Get("ETag"), deepLink.Body.String())
	}
	if rec := serveStatic(t, s, newStaticRequest(t, http.MethodGet, "/assets/missing.js", "")); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset status = %d, want 404", rec.Code)
	}
	if rec := serveStatic(t, s, newStaticRequest(t, http.MethodHead, "/assets/app.css", "")); rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("ETag") == "" {
		t.Errorf("HEAD = status %d body %d etag %q", rec.Code, rec.Body.Len(), rec.Header().Get("ETag"))
	}
}

func TestEmbeddedAssetIndexReportsPrecompressedBytes(t *testing.T) {
	t.Parallel()
	index, err := buildStaticAssets(web.Assets())
	if err != nil {
		t.Fatalf("build embedded asset index: %v", err)
	}
	if index.gzipBytes == 0 {
		t.Fatal("embedded asset index retained no gzip variants")
	}
	t.Logf("precompressed embedded assets retained: %d bytes", index.gzipBytes)
}

func TestAPIGzipThresholdAndAgentExclusion(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testOptions(), newFakeStore())
	large := map[string]string{"payload": strings.Repeat("compressible JSON value ", 100)}

	h := s.apiGzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/time" {
			writeJSON(w, http.StatusOK, large)
			return
		}
		if r.URL.Path == "/api/v1/small" {
			writeJSON(w, http.StatusOK, map[string]string{"payload": "small"})
			return
		}
		writeJSON(w, http.StatusOK, large)
	}))

	for _, tc := range []struct {
		name         string
		path         string
		accept       string
		wantEncoding string
		wantVary     bool
	}{
		{name: "large accepted", path: "/api/v1/hosts", accept: "br, gzip", wantEncoding: "gzip", wantVary: true},
		{name: "large not accepted", path: "/api/v1/hosts", wantVary: true},
		{name: "small", path: "/api/v1/small", accept: "gzip", wantVary: true},
		{name: "agent", path: "/api/v1/agent/time", accept: "gzip"},
		{name: "agent token reveal", path: "/api/v1/settings/agent-token", accept: "gzip"},
		{name: "gzip refused as q=0.0", path: "/api/v1/hosts", accept: "gzip;q=0.0, br", wantVary: true},
		{name: "gzip refused as q=0.000", path: "/api/v1/hosts", accept: "gzip; q=0.000", wantVary: true},
		{name: "gzip with q=0.5", path: "/api/v1/hosts", accept: "gzip;q=0.5", wantEncoding: "gzip", wantVary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Accept-Encoding", tc.accept)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if got := rec.Header().Get("Content-Encoding"); got != tc.wantEncoding {
				t.Errorf("Content-Encoding = %q, want %q", got, tc.wantEncoding)
			}
			if got := strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding"); got != tc.wantVary {
				t.Errorf("Vary = %q, want contains Accept-Encoding = %t", rec.Header().Get("Vary"), tc.wantVary)
			}
		})
	}
}

func newStaticRequest(t *testing.T, method, target, encoding string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if encoding != "" {
		req.Header.Set("Accept-Encoding", encoding)
	}
	return req
}

func serveStatic(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleStatic(rec, req)
	return rec
}
