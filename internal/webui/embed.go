// Package webui embeds the built frontend (web/ builds into ./dist).
package webui

import (
	"embed"
	"io/fs"
)

// dist always contains README.md so the embed pattern matches before the UI is built.
//
//go:embed all:dist
var dist embed.FS

// FS returns the built UI rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the directory is embedded at compile time, so this cannot fail
	}
	return sub
}
