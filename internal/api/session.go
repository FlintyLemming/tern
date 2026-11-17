package api

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"tern/internal/auth"
	"tern/internal/vault"
)

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Password) > auth.MaxSecretLen {
		writeError(w, http.StatusBadRequest, "validation", "invalid login request", map[string]string{"password": "too_long"})
		return
	}

	// One login at a time, so parallel guesses cannot all slip past Allow
	// before the first failure is counted.
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	ip := a.clientIP(r)

	if ok, wait := a.d.Limiter.Allow(ip, a.d.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many failed logins; try again later", nil)
		return
	}
	hash, ok, err := a.d.Store.GetMeta(r.Context(), metaPasswordHash)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if !ok || !auth.VerifyPassword(hash, req.Password) {
		a.d.Limiter.Fail(ip, a.d.Now())
		writeError(w, http.StatusUnauthorized, "unauthorized", "wrong password", nil)
		return
	}
	a.d.Limiter.Reset(ip)
	a.startSession(w, r, http.StatusOK)
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	if s.token == "" {
		// A QTS sign-in: QTS owns that session; signing out is done there.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.d.Sessions.Delete(r.Context(), s.token); err != nil {
		internalError(w, r, err)
		return
	}
	a.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": sessionFrom(r).csrf})
}

func (a *API) handleUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Passphrase) > auth.MaxSecretLen {
		writeError(w, http.StatusBadRequest, "validation", "invalid unlock request", map[string]string{"passphrase": "too_long"})
		return
	}
	switch a.d.Vault.State() {
	case vault.StateUnlocked:
		w.WriteHeader(http.StatusNoContent)
		return
	case vault.StateLocked:
	case vault.StateUninitialized:
		writeError(w, http.StatusConflict, "conflict", "the vault has not been set up", nil)
		return
	default:
		a.requireUnlocked(w) // writes vault_unavailable
		return
	}
	err := a.d.Vault.Unlock(r.Context(), req.Passphrase)
	switch {
	case err == nil:
		a.syncPaused() // scheduling resumes
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, vault.ErrWrongPassphrase):
		writeError(w, http.StatusForbidden, "forbidden", "wrong passphrase", nil)
	default:
		internalError(w, r, err)
	}
}
