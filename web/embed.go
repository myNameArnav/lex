// Package web embeds the browser UI.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// FS returns the UI files rooted at the static directory.
func FS() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
