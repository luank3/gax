//go:build !webui

// Package ui provides a stub when built without -tags webui (ADR-0010).
package ui

import "io/fs"

// FS returns nil when built without -tags webui.
func FS() fs.FS { return nil }
