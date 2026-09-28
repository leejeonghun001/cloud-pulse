package web

import (
	"io/fs"
	"testing"
)

// TestAssetsContainsPlaceholderFiles asserts that the embedded filesystem
// exposes index.html and assets/app.js, which cmd/hub relies on to serve
// the placeholder frontend.
func TestAssetsContainsPlaceholderFiles(t *testing.T) {
	t.Parallel()

	assets := Assets()

	for _, name := range []string{"index.html", "assets/app.js", "assets/app.css"} {
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
