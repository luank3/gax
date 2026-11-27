//go:build webui

// Package ui embeds the Vite-built Preact frontend assets when built with -tags webui.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded static web UI filesystem, or nil when built without webui.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil
	}
	return sub
}
