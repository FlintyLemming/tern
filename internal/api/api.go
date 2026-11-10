// Package api exposes /api/v1. The web UI uses nothing else.
package api

import (
	"net/http"
	"sync"
	"time"

	"tern/internal/auth"
	"tern/internal/config"
	"tern/internal/store"
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
}

// API is the HTTP handler for the whole process.
type API struct {
	d   Deps
	mux *http.ServeMux

	setupMu sync.Mutex // one setup at a time
	loginMu sync.Mutex // check-limit, verify and count as one step
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
	a := &API{d: d, mux: http.NewServeMux()}

	a.Handle("GET /api/v1/status", a.handleStatus)
	a.Handle("POST /api/v1/setup", a.handleSetup)
	a.Handle("POST /api/v1/login", a.handleLogin)
	a.Handle("POST /api/v1/logout", a.handleLogout, NeedSession)
	a.Handle("GET /api/v1/session", a.handleSession, NeedSession)
	a.Handle("POST /api/v1/unlock", a.handleUnlock, NeedSession)
	a.registerKeys()

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
	setSecurityHeaders(w)
	a.mux.ServeHTTP(w, r)
}
