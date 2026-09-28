package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// requiredEntryPoints are files the hub's HTML/JS explicitly references
// and that must exist in the embedded FS for the dashboard to function.
var requiredEntryPoints = []string{
	"index.html",
	"assets/app.css",
	"assets/js/main.js",
	"assets/js/api.js",
	"assets/js/format.js",
	"assets/js/components.js",
	"assets/js/charts.js",
	"assets/js/egress.js",
	"assets/js/buckets.js",
	"assets/vendor/uplot/uPlot.esm.js",
	"assets/vendor/uplot/uPlot.min.css",
	"assets/vendor/uplot/LICENSE",
}

// TestAssetsContainsRequiredEntryPoints asserts that every file the
// dashboard's HTML/JS references locally is present (non-empty) in the
// embedded filesystem.
func TestAssetsContainsRequiredEntryPoints(t *testing.T) {
	t.Parallel()

	assets := Assets()

	for _, name := range requiredEntryPoints {
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("%q is empty", name)
		}
	}
}

// TestAssetsIsValidFS asserts the embedded filesystem passes fstest's
// structural validation.
func TestAssetsIsValidFS(t *testing.T) {
	t.Parallel()

	if err := fs.WalkDir(Assets(), ".", func(path string, d fs.DirEntry, err error) error {
		return err
	}); err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
}

// localScriptOrLinkRefPattern finds src="..."/href="..." attribute values
// referencing http:// or https:// (i.e. a CDN/external origin), which the
// dashboard must never do per its CSP (script-src 'self').
var localScriptOrLinkRefPattern = regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*["'](https?://[^"']*)["']`)

// inlineScriptWithoutSrcPattern finds an opening <script ...> tag that has
// no src="..." attribute, i.e. an inline script block, which the CSP
// (script-src 'self', no 'unsafe-inline') forbids.
var scriptTagPattern = regexp.MustCompile(`(?is)<script\b([^>]*)>`)
var srcAttrPattern = regexp.MustCompile(`(?i)\bsrc\s*=`)

// onEventAttrPattern finds an inline event handler attribute like
// onclick="...", onload="...", etc.
var onEventAttrPattern = regexp.MustCompile(`(?i)\bon[a-z]+\s*=\s*["']`)

// TestIndexHTMLHasNoCDNOrInlineScripts asserts index.html contains no
// external (http/https) script or link references, no inline <script>
// blocks lacking a src attribute, and no inline on*= event handler
// attributes — all required for the dashboard's CSP
// (script-src 'self'; no 'unsafe-inline', no eval) to hold.
func TestIndexHTMLHasNoCDNOrInlineScripts(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(Assets(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	html := string(data)

	if m := localScriptOrLinkRefPattern.FindAllString(html, -1); len(m) > 0 {
		t.Errorf("index.html contains external (http/https) script/link references, want none (CDN forbidden): %v", m)
	}

	for _, tag := range scriptTagPattern.FindAllStringSubmatch(html, -1) {
		attrs := tag[1]
		if !srcAttrPattern.MatchString(attrs) {
			t.Errorf("index.html contains an inline <script> without src=, want all scripts external: <script%s>", attrs)
		}
	}

	if m := onEventAttrPattern.FindAllString(html, -1); len(m) > 0 {
		t.Errorf("index.html contains inline event handler attributes (on*=), want none: %v", m)
	}
}

// TestIndexHTMLReferencesOnlyLocalAssets walks index.html's src=/href=
// references to local paths (rooted at "/") and asserts each resolves to
// a file present in the embedded FS.
func TestIndexHTMLReferencesOnlyLocalAssets(t *testing.T) {
	t.Parallel()

	assets := Assets()
	data, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	html := string(data)

	localRefPattern := regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*["'](/[^"']*)["']`)
	for _, m := range localRefPattern.FindAllStringSubmatch(html, -1) {
		ref := strings.TrimPrefix(m[1], "/")
		if ref == "" {
			continue
		}
		if _, err := fs.Stat(assets, ref); err != nil {
			t.Errorf("index.html references %q, not found in embedded FS: %v", m[1], err)
		}
	}
}

// TestNoJSFilesReferenceExternalOrigins asserts none of the embedded JS
// files import from or fetch an http(s):// URL (vendored deps only,
// loaded via relative import paths).
func TestNoJSFilesReferenceExternalOrigins(t *testing.T) {
	t.Parallel()

	assets := Assets()
	externalPattern := regexp.MustCompile(`(?i)(?:from\s+["']|import\(\s*["']|fetch\(\s*["'])https?://`)

	err := fs.WalkDir(assets, "assets/js", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".js") {
			return nil
		}
		data, readErr := fs.ReadFile(assets, path)
		if readErr != nil {
			return readErr
		}
		if m := externalPattern.FindAllString(string(data), -1); len(m) > 0 {
			t.Errorf("%s references an external http(s) origin, want vendored/local only: %v", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(assets/js): %v", err)
	}
}

// TestVendorLicensePresent asserts the vendored uPlot LICENSE file is
// embedded and non-empty (CODING_CONVENTIONS.md "Embedded frontend
// standards": every vendored third-party asset ships its license).
func TestVendorLicensePresent(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(Assets(), "assets/vendor/uplot/LICENSE")
	if err != nil {
		t.Fatalf("ReadFile(assets/vendor/uplot/LICENSE): %v", err)
	}
	if !strings.Contains(string(data), "MIT License") {
		t.Errorf("assets/vendor/uplot/LICENSE does not look like an MIT license text")
	}
}
