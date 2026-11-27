// Package webdist embeds the compiled Vue 3 frontend assets when present.
package webdist

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var rawFS embed.FS

// Assets returns the sub-filesystem rooted at dist/.
func Assets() (fs.FS, error) {
	return fs.Sub(rawFS, "dist")
}
