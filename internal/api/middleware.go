package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"

	"tern/internal/auth"
	"tern/internal/config"
	"tern/internal/qts"
	"tern/internal/vault"
)

const (
	cookieName = "tern_session"
	csrfHeader = "X-CSRF-Token"
	cspDocker  = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	// On QTS the interface opens in a QTS desktop window: an iframe on
	// QTS's own origin, which tern shares through QTS's proxy.
	cspQTS = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'self'; base-uri 'none'; form-action 'self'"

	metaQTSCSRFKey = "qts.csrf_key"
)

func (a *API) setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", a.csp)
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

// session is who a request is from: a tern session (token set) or, on
// QTS, a QTS administrator (qtsUser set, no token).
type session struct {
	token   string
	csrf    string
	qtsUser string
}

type sessionKey struct{}

func sessionFrom(r *http.Request) session {
	s, _ := r.Context().Value(sessionKey{}).(session)
	return s
}

// lookupSession resolves the request's cookie; ok is false without a live
// session. On QTS, a request without a tern session is from whoever its
// QTS session belongs to, if that is an administrator.
func (a *API) lookupSession(r *http.Request) (session, bool, error) {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		csrf, ok, err := a.d.Sessions.Lookup(r.Context(), c.Value)
		if err != nil {
			return session{}, false, err
		}
		if ok {
			return session{token: c.Value, csrf: csrf}, true, nil
		}
	}
	return a.lookupQTS(r)
}

// lookupQTS checks the QTS session cookie. When QTS cannot be asked the
// request is simply not signed in, so tern's own login still works.
func (a *API) lookupQTS(r *http.Request) (session, bool, error) {
	if a.d.QTS == nil {
		return session{}, false, nil
	}
	c, err := r.Cookie(qts.SessionCookie)
	if err != nil || c.Value == "" {
		return session{}, false, nil
	}
	user, ok, err := a.d.QTS.Admin(r.Context(), c.Value)
	if err != nil {
		slog.Warn("cannot check the QTS sign-in; only tern's own login works until QTS answers", "err", err)
		return session{}, false, nil
	}
	if !ok {
		return session{}, false, nil
	}
	csrf, err := a.qtsCSRF(r.Context(), c.Value)
	if err != nil {
		return session{}, false, err
	}
	return session{csrf: csrf, qtsUser: user}, true, nil
}

// qtsCSRF is the CSRF token of a QTS session: an HMAC of its id under a key
// kept in the database, so it survives restarts like a tern session's.
func (a *API) qtsCSRF(ctx context.Context, sid string) (string, error) {
	a.qtsKeyMu.Lock()
	defer a.qtsKeyMu.Unlock()
	if a.qtsKey == nil {
		v, ok, err := a.d.Store.GetMeta(ctx, metaQTSCSRFKey)
		if err != nil {
			return "", err
		}
		if !ok {
			b := make([]byte, 32)
			rand.Read(b)
			v = hex.EncodeToString(b)
			if err := a.d.Store.SetMeta(context.WithoutCancel(ctx), metaQTSCSRFKey, v); err != nil {
				return "", err
			}
		}
		key, err := hex.DecodeString(v)
		if err != nil {
			return "", err
		}
		a.qtsKey = key
	}
	m := hmac.New(sha256.New, a.qtsKey)
	m.Write([]byte("qts-csrf\x00" + sid))
	return hex.EncodeToString(m.Sum(nil)), nil
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

// clientIP is the peer address. X-Forwarded-For is not trusted: anyone can
// set it. On QTS, where tern listens on loopback behind QTS's proxy, the
// last address the proxy appended is the client.
func (a *API) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if a.d.Cfg.Platform == config.PlatformQTS {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
				parts := strings.Split(xff[len(xff)-1], ",")
				if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
					return last
				}
			}
		}
	}
	return host
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (a *API) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     a.cookiePath(),
		MaxAge:   int(auth.SessionMaxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPS(r),
	})
}

func (a *API) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Path:     a.cookiePath(),
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPS(r),
	})
}
