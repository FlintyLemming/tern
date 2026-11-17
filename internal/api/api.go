// Package api exposes /api/v1. The web UI uses nothing else.
package api

import (
	"context"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"tern/internal/access"
	"tern/internal/auth"
	"tern/internal/clock"
	"tern/internal/config"
	"tern/internal/engine"
	"tern/internal/events"
	"tern/internal/mounts"
	"tern/internal/qts"
	"tern/internal/runner"
	"tern/internal/scheduler"
	"tern/internal/store"
	"tern/internal/sysinfo"
	"tern/internal/vault"
)

// Deps is everything the API needs from the rest of the process.
type Deps struct {
	Cfg      config.Config
	Store    *store.Store
	Vault    *vault.Vault
	Sessions *auth.Sessions
	Limiter  *auth.Limiter
	Version  string
	Now      func() time.Time

	// The execution core. Clock is the time source for everything about
	// tasks and runs; it defaults to the real clock.
	Clock     clock.Clock
	Scheduler *scheduler.Scheduler
	Runner    *runner.Runner
	Registry  *engine.Registry
	Events    *events.Hub

	// What the API itself does with repositories (attach on task
	// creation, list snapshots). Access defaults to the real opener,
	// ResticBin to "restic". RcloneBin is what lists the folders at a
	// connection's remote end; it defaults to "rclone".
	Access    engine.AccessOpener
	ResticBin string
	RcloneBin string
	// Mounted tells whether a path is on a mounted volume; restores only
	// go to such paths. Defaults to mounts.IsMounted.
	Mounted func(path string) (bool, error)
	// Mounts says whether the data and secrets directories were on
	// mounted volumes at startup. Setup refuses to write where a
	// recreated container would lose it.
	Mounts sysinfo.Mounts

	// QTS checks QTS sign-ins on the QTS platform. Defaults to a
	// qts.Checker asking Cfg.QTSAuthURL; unused elsewhere.
	QTS QTSChecker
}

// QTSChecker tells whether a QTS session id belongs to a signed-in
// administrator (see package qts).
type QTSChecker interface {
	Admin(ctx context.Context, sid string) (user string, ok bool, err error)
}

// API is the HTTP handler for the whole process.
type API struct {
	d    Deps
	mux  *http.ServeMux
	root http.Handler // mux, below Cfg.BasePath
	csp  string

	qtsKeyMu sync.Mutex
	qtsKey   []byte // CSRF key for QTS sign-ins, loaded on first use

	setupMu sync.Mutex // one setup at a time
	loginMu sync.Mutex // check-limit, verify and count as one step

	closing   chan struct{} // closed by Close: event streams end
	closeOnce sync.Once
}

// Close ends the long-lived event streams so the HTTP server can drain.
// Ordinary requests are not affected.
func (a *API) Close() { a.closeOnce.Do(func() { close(a.closing) }) }

// syncPaused pauses scheduling exactly while the vault is not unlocked:
// without it no run could get at its credentials.
func (a *API) syncPaused() {
	a.d.Scheduler.SetPaused(a.d.Vault.State() != vault.StateUnlocked)
}

// Guard is a precondition a route declares in Handle.
type Guard int

const (
	// NeedSession requires a valid session cookie and, for methods other than
	// GET and HEAD, a matching X-CSRF-Token header.
	NeedSession Guard = iota + 1
	// NeedUnlocked requires the vault to be unlocked.
	NeedUnlocked
)

func New(d Deps) *API {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Clock == nil {
		d.Clock = clock.Real
	}
	if d.Access == nil {
		d.Access = access.Opener{Store: d.Store, Vault: d.Vault}
	}
	if d.Mounted == nil {
		d.Mounted = mounts.IsMounted
	}
	if d.Cfg.Platform == "" {
		d.Cfg.Platform = config.PlatformDocker
	}
	if d.QTS == nil && d.Cfg.Platform == config.PlatformQTS {
		d.QTS = qts.New(d.Cfg.QTSAuthURL, nil)
	}
	a := &API{d: d, mux: http.NewServeMux(), closing: make(chan struct{}), csp: cspDocker}
	a.root = a.mux
	if d.Cfg.BasePath != "" {
		a.root = http.StripPrefix(d.Cfg.BasePath, a.mux)
	}
	if d.Cfg.Platform == config.PlatformQTS {
		a.csp = cspQTS
	}

	a.Handle("GET /api/v1/status", a.handleStatus)
	a.Handle("POST /api/v1/setup", a.handleSetup)
	a.Handle("POST /api/v1/login", a.handleLogin)
	a.Handle("POST /api/v1/logout", a.handleLogout, NeedSession)
	a.Handle("GET /api/v1/session", a.handleSession, NeedSession)
	a.Handle("POST /api/v1/unlock", a.handleUnlock, NeedSession)
	a.registerKeys()
	a.registerConnections()
	a.registerTasks()
	a.registerBackup()
	a.registerSnapshots()
	a.registerRuns()
	a.registerSettings()
	a.registerBrowse()

	// Anything else under /api/, including a known path with the wrong method.
	a.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint", nil)
	})
	return a
}

// Handle registers a route. pattern is a ServeMux pattern such as
// "POST /api/v1/tasks"; guards run in order before h.
func (a *API) Handle(pattern string, h http.HandlerFunc, guards ...Guard) {
	a.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		for _, g := range guards {
			var ok bool
			switch g {
			case NeedSession:
				r, ok = a.requireSession(w, r)
			case NeedUnlocked:
				ok = a.requireUnlocked(w)
			}
			if !ok {
				return
			}
		}
		h(w, r)
	})
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic(w, r)
	a.setSecurityHeaders(w)
	if c := canonicalPath(r.URL.Path); c != r.URL.Path {
		// Served as the clean path, not redirected: ServeMux's redirect
		// would leave out the base path, and QTS's proxy turns "/tern/x"
		// into "/tern//x" on every request, so a redirect would loop.
		u := *r.URL
		u.Path, u.RawPath = c, ""
		r2 := *r
		r2.URL = &u
		r = &r2
	}
	if base := a.d.Cfg.BasePath; base != "" {
		switch p := r.URL.Path; {
		case p == base:
			to := base + "/"
			if r.URL.RawQuery != "" {
				to += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, to, http.StatusMovedPermanently)
			return
		case !strings.HasPrefix(p, base+"/"):
			http.NotFound(w, r)
			return
		}
	}
	a.root.ServeHTTP(w, r)
}

// canonicalPath resolves duplicate slashes and dot segments, keeping a
// trailing slash.
func canonicalPath(p string) string {
	if p == "" {
		return "/"
	}
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c
}

// cookiePath scopes tern's cookie to where tern is served.
func (a *API) cookiePath() string { return a.d.Cfg.BasePath + "/" }
