// Package web embeds the cloud-pulse hub's static frontend assets
// (index.html and assets/) so the hub binary serves them without any
// external files at runtime.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html assets
var files embed.FS

// Assets returns the embedded frontend filesystem, rooted so that
// "index.html" and "assets/..." are top-level entries.
func Assets() fs.FS {
	return files
}
