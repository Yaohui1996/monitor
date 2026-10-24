// Package web holds the dashboard assets that are embedded into the binary.
package web

import "embed"

// Assets contains the static dashboard files.
//
//go:embed static
var Assets embed.FS
