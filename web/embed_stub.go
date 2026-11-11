//go:build !embedweb

// Package web holds the built user interface. Build with -tags embedweb after
// `npm --prefix web run build`; without the tag a placeholder page stands in,
// so the Go tests never depend on a front-end build.
package web

import (
	"io/fs"
	"testing/fstest"
)

const placeholder = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>tern</title></head>
<body><p>This tern binary was built without its web interface. Build it with <code>make build</code>.</p></body></html>
`

// Dist is a file system holding only a placeholder index.html.
func Dist() fs.FS {
	return fstest.MapFS{"index.html": {Data: []byte(placeholder)}}
}
