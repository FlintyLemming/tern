package api

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"

	"tern/internal/auth"
	"tern/internal/vault"
)

const (
	cookieName = "tern_session"
	csrfHeader = "X-CSRF-Token"
	csp        = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
)

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func recoverPanic(w http.ResponseWriter, r *http.Request) {
	if v := recover(); v != nil {
		slog.Error("panic in handler", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
		writeError(w, http.StatusInternalServerError, "internal", "internal error", nil)
	}
}

// internalError logs err and answers 500 without exposing it.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error", nil)
}

type session struct {
	token string
	csrf  string
}

type sessionKey struct{}

func sessionFrom(r *http.Request) session {
	s, _ := r.Context().Value(sessionKey{}).(session)
	return s
}

// lookupSession resolves the request's cookie; ok is false without a live session.
func (a *API) lookupSession(r *http.Request) (session, bool, error) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return session{}, false, nil
	}
	csrf, ok, err := a.d.Sessions.Lookup(r.Context(), c.Value)
	if err != nil || !ok {
		return session{}, false, err
	}
	return session{token: c.Value, csrf: csrf}, true, nil
}

func (a *API) requireSession(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	s, ok, err := a.lookupSession(r)
	if err != nil {
		internalError(w, r, err)
		return r, false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "login required", nil)
		return r, false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(csrfHeader)), []byte(s.csrf)) != 1 {
			writeError(w, http.StatusForbidden, "csrf", "missing or wrong "+csrfHeader+" header", nil)
			return r, false
		}
	}
	return r.WithContext(context.WithValue(r.Context(), sessionKey{}, s)), true
}

func (a *API) requireUnlocked(w http.ResponseWriter) bool {
	switch st := a.d.Vault.State(); st {
	case vault.StateUnlocked:
		return true
	case vault.StateLocked:
		writeError(w, http.StatusServiceUnavailable, "vault_locked", "the credential vault is locked; unlock it with the passphrase", nil)
	default:
		writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "the credential vault cannot be opened: "+string(st), nil)
	}
	return false
}

// clientIP is the peer address. X-Forwarded-For is not trusted: anyone can set it.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(auth.SessionMaxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPS(r),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPS(r),
	})
}
