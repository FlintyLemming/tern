package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexHTML = `<!doctype html><title>tern</title><div id="root"></div>`

func staticAPI(t *testing.T) *API {
	t.Helper()
	h := newHarness(t)
	h.api.ServeStatic(fstest.MapFS{
		"index.html":    {Data: []byte(indexHTML)},
		"assets/app.js": {Data: []byte("console.log(1)")},
	})
	return h.api
}

func get(a *API, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestStaticServesIndex(t *testing.T) {
	w := get(staticAPI(t), "/")
	if w.Code != http.StatusOK || w.Body.String() != indexHTML {
		t.Fatalf("GET / = %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestStaticSPAFallback(t *testing.T) {
	a := staticAPI(t)
	for _, p := range []string{"/tasks/abc", "/runs", "/settings"} {
		w := get(a, p)
		if w.Code != http.StatusOK || w.Body.String() != indexHTML {
			t.Fatalf("GET %s = %d %q, want index.html", p, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("GET %s Cache-Control = %q", p, got)
		}
	}
}

func TestStaticAssetsCached(t *testing.T) {
	w := get(staticAPI(t), "/assets/app.js")
	if w.Code != http.StatusOK || w.Body.String() != "console.log(1)" {
		t.Fatalf("GET /assets/app.js = %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") || !strings.Contains(got, "max-age=31536000") {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestUnknownAPIIs404JSON(t *testing.T) {
	for _, p := range []string{"/api/v1/nope", "/api/", "/api/v2/tasks"} {
		w := get(staticAPI(t), p)
		if w.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", p, w.Code)
		}
		var body errorBody
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != "not_found" {
			t.Fatalf("GET %s body = %q (%v)", p, w.Body.String(), err)
		}
	}
}

func TestStaticHasCSP(t *testing.T) {
	a := staticAPI(t)
	for _, p := range []string{"/", "/tasks/abc", "/assets/app.js"} {
		if got := get(a, p).Header().Get("Content-Security-Policy"); got != csp {
			t.Fatalf("GET %s CSP = %q", p, got)
		}
	}
}

func TestStaticRejectsWrites(t *testing.T) {
	w := httptest.NewRecorder()
	staticAPI(t).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/tasks", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /tasks = %d, want 405", w.Code)
	}
}
