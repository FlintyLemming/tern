//go:build embedweb

// Package web holds the built user interface. Build with -tags embedweb after
// `npm --prefix web run build`; without the tag a placeholder page stands in,
// so the Go tests never depend on a front-end build.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built interface, rooted at web/dist.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: " + err.Error()) // dist is embedded at compile time
	}
	return sub
}
