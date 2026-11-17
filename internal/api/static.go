package api

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// ServeStatic serves the web interface from fsys for every path outside
// /api/. A file that exists is served as is; anything else gets index.html,
// so the single-page app can route it. Files under /assets/ carry content
// hashes in their names and are cached for good; everything else is
// revalidated every time.
//
// index.html is adjusted to where tern is served (see indexFor).
func (a *API) ServeStatic(fsys fs.FS) {
	index, haveIndex := indexFor(fsys, a.d.Cfg.BasePath)
	a.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" && serveFile(w, r, fsys, name) {
			return
		}
		if !haveIndex {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}

// indexFor reads index.html and points it at base: asset references (the
// build writes them as "./assets/…") become base+"/assets/…", absolute from
// any page the app routes, and with a base a <meta name="tern-base"> tells
// the interface where it lives. base is validated by config, so it goes
// into HTML as is.
func indexFor(fsys fs.FS, base string) ([]byte, bool) {
	raw, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil, false
	}
	s := string(raw)
	s = strings.ReplaceAll(s, `="./assets/`, `="`+base+`/assets/`)
	if base != "" {
		s = strings.ReplaceAll(s, `="/assets/`, `="`+base+`/assets/`)
		meta := `<meta name="tern-base" content="` + base + `/">`
		at := 0 // never in front of the doctype: that is quirks mode
		if i := strings.Index(s, "<head>"); i >= 0 {
			at = i + len("<head>")
		} else if strings.HasPrefix(strings.ToLower(s), "<!doctype html>") {
			at = len("<!doctype html>")
		}
		s = s[:at] + meta + s[at:]
	}
	return []byte(s), true
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
