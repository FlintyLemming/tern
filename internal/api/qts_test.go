package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"tern/internal/config"
)

// fakeQTS answers QTS's session check: admins and users map session ids
// to user names; err makes every check fail.
type fakeQTS struct {
	mu     sync.Mutex
	admins map[string]string
	users  map[string]string
	err    error
}

func (f *fakeQTS) Admin(_ context.Context, sid string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", false, f.err
	}
	if u, ok := f.admins[sid]; ok {
		return u, true, nil
	}
	return f.users[sid], false, nil
}

func (f *fakeQTS) fail(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func nasSID(sid string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "NAS_SID", Value: sid}) }
}

// newQTSHarness is a node on the QTS platform below /tern, as the QNAP
// package runs it.
func newQTSHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.cfg = config.Config{Platform: config.PlatformQTS, BasePath: "/tern"}
	h.qts = &fakeQTS{admins: map[string]string{"adminsid": "alice"}, users: map[string]string{"usersid": "bob"}}
	h.restart()
	return h
}

func (h *harness) withStatic() {
	h.api.ServeStatic(fstest.MapFS{
		"index.html":    {Data: []byte(`<!doctype html><html><head><title>tern</title><script type="module" crossorigin src="./assets/app.js"></script></head><body></body></html>`)},
		"assets/app.js": {Data: []byte("console.log(1)")},
	})
}

func TestBasePathRouting(t *testing.T) {
	h := newHarness(t)
	h.cfg = config.Config{BasePath: "/tern"}
	h.restart()
	h.withStatic()

	if w := h.do("GET", "/tern/api/v1/status", nil); w.Code != http.StatusOK {
		t.Fatalf("GET /tern/api/v1/status = %d %s", w.Code, w.Body)
	}
	for _, p := range []string{"/api/v1/status", "/", "/assets/app.js", "/ternx", "/ternx/api/v1/status"} {
		if w := h.do("GET", p, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 outside the base path", p, w.Code)
		}
	}
	w := h.do("GET", "/tern?x=1", nil)
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/tern/?x=1" {
		t.Fatalf("GET /tern = %d Location %q, want 301 to /tern/?x=1", w.Code, w.Header().Get("Location"))
	}
	if w := h.do("GET", "/tern/api/v1/nope", nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "not_found") {
		t.Fatalf("unknown API path = %d %s, want the JSON 404", w.Code, w.Body)
	}
	if w := h.do("GET", "/tern/assets/app.js", nil); w.Code != http.StatusOK || w.Body.String() != "console.log(1)" {
		t.Fatalf("GET /tern/assets/app.js = %d %q", w.Code, w.Body)
	}
	for _, p := range []string{"/tern/", "/tern/tasks/abc", "/tern/index.html"} {
		w := h.do("GET", p, nil)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `<head><meta name="tern-base" content="/tern/">`) || !strings.Contains(body, `src="/tern/assets/app.js"`) {
			t.Errorf("GET %s = %d %q, want index.html pointed at /tern", p, w.Code, body)
		}
	}
}

func TestIndexAtRoot(t *testing.T) {
	h := newHarness(t)
	h.withStatic()
	body := h.do("GET", "/runs", nil).Body.String()
	if !strings.Contains(body, `src="/assets/app.js"`) || strings.Contains(body, "tern-base") {
		t.Fatalf("index at the root = %q, want absolute /assets/ and no base meta", body)
	}
}

func TestIndexForMetaPlacement(t *testing.T) {
	for in, want := range map[string]string{
		`<!doctype html><title>x</title>`: `<!doctype html><meta name="tern-base" content="/b/"><title>x</title>`,
		`<title>x</title>`:                `<meta name="tern-base" content="/b/"><title>x</title>`,
		`<html><head></head></html>`:      `<html><head><meta name="tern-base" content="/b/"></head></html>`,
	} {
		got, _ := indexFor(fstest.MapFS{"index.html": {Data: []byte(in)}}, "/b")
		if string(got) != want {
			t.Errorf("indexFor(%q) = %q, want %q", in, got, want)
		}
	}
	if _, ok := indexFor(fstest.MapFS{}, ""); ok {
		t.Fatal("indexFor without index.html reported one")
	}
}

func TestCookiePathFollowsBase(t *testing.T) {
	h := newHarness(t)
	h.cfg = config.Config{BasePath: "/tern"}
	h.restart()
	w := h.do("POST", "/tern/api/v1/setup", setupBody())
	if c := sessionCookie(w); w.Code != http.StatusCreated || c == nil || c.Path != "/tern/" {
		t.Fatalf("setup = %d, cookie %+v; want Path /tern/", w.Code, c)
	}

	root := newHarness(t)
	w = root.do("POST", "/api/v1/setup", setupBody())
	if c := sessionCookie(w); c == nil || c.Path != "/" {
		t.Fatalf("cookie at the root = %+v, want Path /", c)
	}
}

func TestCSPPerPlatform(t *testing.T) {
	h := newHarness(t)
	if got := h.do("GET", "/api/v1/status", nil).Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("docker CSP = %q, want frame-ancestors 'none'", got)
	}
	q := newQTSHarness(t)
	got := q.do("GET", "/tern/api/v1/status", nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(got, "frame-ancestors 'self'") || strings.Contains(got, "'none'; base") {
		t.Fatalf("QTS CSP = %q, want frame-ancestors 'self'", got)
	}
}

func TestQTSSetupNeedsQTSAdmin(t *testing.T) {
	h := newQTSHarness(t)
	for name, opts := range map[string][]reqOpt{
		"no QTS session": nil,
		"QTS user":       {nasSID("usersid")},
		"unknown sid":    {nasSID("nosuchsid")},
	} {
		w := h.do("POST", "/tern/api/v1/setup", setupBody(), opts...)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "qts_admin_required") {
			t.Fatalf("%s: setup = %d %s, want 401 qts_admin_required", name, w.Code, w.Body)
		}
	}
	h.nothingWritten()
	w := h.do("POST", "/tern/api/v1/setup", setupBody(), nasSID("adminsid"))
	if w.Code != http.StatusCreated {
		t.Fatalf("setup as QTS admin = %d %s", w.Code, w.Body)
	}
	// It goes on as the QTS sign-in: no tern session to outlive it.
	csrf, _ := decode(t, w)["csrf_token"].(string)
	if sessionCookie(w) != nil || csrf == "" {
		t.Fatalf("setup as QTS admin: cookie %v, token %q; want no cookie and a token", sessionCookie(w), csrf)
	}
	if got, _ := decode(t, h.do("GET", "/tern/api/v1/session", nil, nasSID("adminsid")))["csrf_token"].(string); got != csrf {
		t.Fatalf("setup token %q, session token %q: want the same", csrf, got)
	}
	if n := h.count("sessions"); n != 0 {
		t.Fatalf("%d tern sessions after a QTS setup, want 0", n)
	}
}

func TestQTSSignIn(t *testing.T) {
	h := newQTSHarness(t)
	h.do("POST", "/tern/api/v1/setup", setupBody(), nasSID("adminsid"))

	st := decode(t, h.do("GET", "/tern/api/v1/status", nil))
	if st["authenticated"] != false || st["platform"] != "qts" {
		t.Fatalf("status without any session = %v", st)
	}
	for _, sid := range []string{"usersid", "nosuchsid"} {
		if st := decode(t, h.do("GET", "/tern/api/v1/status", nil, nasSID(sid))); st["authenticated"] != false {
			t.Fatalf("status with QTS sid %q = %v, want not authenticated", sid, st)
		}
		wantError(t, h.do("GET", "/tern/api/v1/session", nil, nasSID(sid)), http.StatusUnauthorized, "unauthorized")
	}

	st = decode(t, h.do("GET", "/tern/api/v1/status", nil, nasSID("adminsid")))
	if st["authenticated"] != true || st["signed_in_via"] != "qts" || st["qts_user"] != "alice" {
		t.Fatalf("status as QTS admin = %v", st)
	}
	w := h.do("GET", "/tern/api/v1/session", nil, nasSID("adminsid"))
	csrf, _ := decode(t, w)["csrf_token"].(string)
	if w.Code != http.StatusOK || csrf == "" || sessionCookie(w) != nil {
		t.Fatalf("session as QTS admin = %d %s, cookie %v; want a CSRF token and no tern cookie", w.Code, w.Body, sessionCookie(w))
	}

	// Writes need the token, like with a tern session.
	body := map[string]any{"name": "k1", "mode": "generate"}
	wantError(t, h.do("POST", "/tern/api/v1/ssh-keys", body, nasSID("adminsid")), http.StatusForbidden, "csrf")
	wantError(t, h.do("POST", "/tern/api/v1/ssh-keys", body, nasSID("adminsid"), header("X-CSRF-Token", "x"+csrf[1:])), http.StatusForbidden, "csrf")
	if w := h.do("POST", "/tern/api/v1/ssh-keys", body, nasSID("adminsid"), header("X-CSRF-Token", csrf)); w.Code != http.StatusCreated {
		t.Fatalf("write as QTS admin = %d %s", w.Code, w.Body)
	}
	// Another QTS session gets another token.
	h.qts.admins["othersid"] = "carol"
	other, _ := decode(t, h.do("GET", "/tern/api/v1/session", nil, nasSID("othersid")))["csrf_token"].(string)
	if other == "" || other == csrf {
		t.Fatalf("token of another QTS session = %q, want a different one", other)
	}

	// The token survives a restart: the open window keeps working.
	h.restart()
	if got, _ := decode(t, h.do("GET", "/tern/api/v1/session", nil, nasSID("adminsid")))["csrf_token"].(string); got != csrf {
		t.Fatalf("token after restart = %q, want %q", got, csrf)
	}

	// Signing out of tern is QTS's business: nothing happens.
	if w := h.do("POST", "/tern/api/v1/logout", nil, nasSID("adminsid"), header("X-CSRF-Token", csrf)); w.Code != http.StatusNoContent || sessionCookie(w) != nil {
		t.Fatalf("logout as QTS admin = %d, cookie %v", w.Code, sessionCookie(w))
	}
	if st := decode(t, h.do("GET", "/tern/api/v1/status", nil, nasSID("adminsid"))); st["authenticated"] != true {
		t.Fatalf("status after logout = %v, still signed in to QTS", st)
	}

	// Signed out of QTS (or demoted): access ends.
	delete(h.qts.admins, "adminsid")
	wantError(t, h.do("GET", "/tern/api/v1/ssh-keys", nil, nasSID("adminsid")), http.StatusUnauthorized, "unauthorized")
}

func TestQTSUnreachableFallsBackToPassword(t *testing.T) {
	h := newQTSHarness(t)
	h.do("POST", "/tern/api/v1/setup", setupBody(), nasSID("adminsid"))
	h.cookie, h.csrf = "", ""
	h.qts.fail(errors.New("connection refused"))

	if st := decode(t, h.do("GET", "/tern/api/v1/status", nil, nasSID("adminsid"))); st["authenticated"] != false {
		t.Fatalf("status while QTS cannot be asked = %v", st)
	}
	w := h.do("POST", "/tern/api/v1/login", map[string]any{"password": adminPass})
	if w.Code != http.StatusOK {
		t.Fatalf("password login = %d %s", w.Code, w.Body)
	}
	h.adopt(w)
	st := decode(t, h.do("GET", "/tern/api/v1/status", nil, nasSID("adminsid")))
	if st["authenticated"] != true || st["signed_in_via"] != "password" {
		t.Fatalf("status after password login = %v", st)
	}
}

func TestDockerIgnoresQTSCookie(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.cookie, h.csrf = "", ""
	if st := h.status(); st["platform"] != "docker" {
		t.Fatalf("platform = %v, want docker", st["platform"])
	}
	if st := decode(t, h.do("GET", "/api/v1/status", nil, nasSID("adminsid"))); st["authenticated"] != false {
		t.Fatalf("docker node honoured a QTS cookie: %v", st)
	}
}

// Behind QTS's proxy every request comes from loopback; the login limit
// goes by the address the proxy reports.
func TestQTSLoginLimitPerForwardedClient(t *testing.T) {
	h := newQTSHarness(t)
	h.do("POST", "/tern/api/v1/setup", setupBody(), nasSID("adminsid"))
	h.cookie, h.csrf = "", ""
	from := func(ip string) reqOpt {
		return func(r *http.Request) {
			r.RemoteAddr = "127.0.0.1:40000"
			r.Header.Set("X-Forwarded-For", "10.9.9.9, "+ip)
		}
	}
	for range 6 {
		h.do("POST", "/tern/api/v1/login", map[string]any{"password": "wrong-password"}, from("192.168.5.20"))
	}
	wantError(t, h.do("POST", "/tern/api/v1/login", map[string]any{"password": adminPass}, from("192.168.5.20")), http.StatusTooManyRequests, "rate_limited")
	if w := h.do("POST", "/tern/api/v1/login", map[string]any{"password": adminPass}, from("192.168.5.21")); w.Code != http.StatusOK {
		t.Fatalf("login from another client = %d %s, want 200", w.Code, w.Body)
	}

	// Elsewhere the header is anyone's to forge and is ignored.
	d := newHarness(t)
	d.setup()
	d.cookie, d.csrf = "", ""
	for i := range 6 {
		d.do("POST", "/api/v1/login", map[string]any{"password": "wrong-password"}, from("192.168.5."+string(rune('0'+i))))
	}
	wantError(t, d.do("POST", "/api/v1/login", map[string]any{"password": adminPass}, from("192.168.5.99")), http.StatusTooManyRequests, "rate_limited")
}

// QTS's proxy maps "/tern" to ".../tern/", so tern gets "/tern//api/…".
func TestUncleanPathsServedInPlace(t *testing.T) {
	h := newHarness(t)
	h.cfg = config.Config{BasePath: "/tern"}
	h.restart()
	h.withStatic()
	for _, p := range []string{"/tern//api/v1/status", "/tern/api//v1/status", "/tern/./api/v1/status", "/tern/x/../api/v1/status"} {
		w := h.do("GET", p, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "setup_required") {
			t.Errorf("GET %s = %d %s, want the status", p, w.Code, w.Body)
		}
	}
	if w := h.do("GET", "/tern//", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "tern-base") {
		t.Errorf("GET /tern// = %d, want index.html", w.Code)
	}
	if w := h.do("GET", "/tern/../api/v1/status", nil); w.Code != http.StatusNotFound {
		t.Errorf("GET /tern/../api/v1/status = %d, want 404: it is outside the base path", w.Code)
	}
	// A write through the doubled slash reaches its handler too.
	if w := h.do("POST", "/tern//api/v1/setup", setupBody()); w.Code != http.StatusCreated {
		t.Errorf("POST /tern//api/v1/setup = %d %s", w.Code, w.Body)
	}

	root := newHarness(t)
	if w := root.do("GET", "//api/v1/status", nil); w.Code != http.StatusOK {
		t.Errorf("GET //api/v1/status at the root = %d, want 200", w.Code)
	}
}
