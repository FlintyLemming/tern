package api

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// ServeStatic serves the web interface from fsys for every path outside
// /api/. A file that exists is served as is; anything else gets index.html,
// so the single-page app can route it. Files under /assets/ carry content
// hashes in their names and are cached for good; everything else is
// revalidated every time.
func (a *API) ServeStatic(fsys fs.FS) {
	a.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && serveFile(w, r, fsys, name) {
			return
		}
		if !serveFile(w, r, fsys, "index.html") {
			http.NotFound(w, r)
		}
	})
}

// serveFile writes the regular file name from fsys and reports whether it
// existed.
func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	f, err := fsys.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return false
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, st.ModTime(), rs)
	return true
}
