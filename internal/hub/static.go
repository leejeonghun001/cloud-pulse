package hub

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// staticAsset is an embedded dashboard file prepared once during Server
// construction. Keeping a compressed representation avoids spending CPU on
// every page load while Cache-Control: no-cache still revalidates upgrades.
type staticAsset struct {
	plain       []byte
	gzip        []byte
	etag        string
	gzipETag    string
	contentType string
}

// staticAssets is the immutable embedded-file index. gzipBytes records the
// precompressed memory retained by the server.
type staticAssets struct {
	files     map[string]staticAsset
	gzipBytes int64
}

func buildStaticAssets(assets fs.FS) (*staticAssets, error) {
	if assets == nil {
		return nil, nil
	}
	index := &staticAssets{files: make(map[string]staticAsset)}
	err := fs.WalkDir(assets, ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			return fmt.Errorf("hub: read embedded asset %s: %w", name, err)
		}
		digest := sha256.Sum256(data)
		hash := fmt.Sprintf("%x", digest[:16])
		asset := staticAsset{
			plain:       data,
			etag:        `"` + hash + `"`,
			gzipETag:    `"` + hash + `-gz"`,
			contentType: contentType(name, data),
		}
		if isCompressibleAsset(name) {
			compressed, err := gzipBytes(data)
			if err != nil {
				return fmt.Errorf("hub: gzip embedded asset %s: %w", name, err)
			}
			if len(compressed) < len(data) {
				asset.gzip = compressed
				index.gzipBytes += int64(len(compressed))
			}
		}
		index.files[name] = asset
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hub: index embedded assets: %w", err)
	}
	return index, nil
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("new gzip writer: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close() // best-effort close after a failed write, nothing to recover
		return nil, fmt.Errorf("write gzip: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	return buf.Bytes(), nil
}

func isCompressibleAsset(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".css", ".json", ".html", ".svg", ".txt", ".md":
		return true
	default:
		return false
	}
}

func contentType(name string, data []byte) string {
	if typ := mime.TypeByExtension(path.Ext(name)); typ != "" {
		return typ
	}
	return http.DetectContentType(data)
}

func acceptsGzip(header string) bool {
	for _, item := range strings.Split(header, ",") {
		parts := strings.Split(item, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
			continue
		}
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "q") {
				if q, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && q == 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}

// etagMatches applies If-None-Match's weak comparison semantics for GET and
// HEAD. gzip and plain variants deliberately have distinct opaque tags.
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}

func addVary(h http.Header, value string) {
	for _, existing := range h.Values("Vary") {
		for _, item := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
				return
			}
		}
	}
	h.Add("Vary", value)
}
