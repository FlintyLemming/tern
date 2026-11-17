package api

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"strconv"
	"time"

	"tern/internal/auth"
	"tern/internal/scheduler"
	"tern/internal/store"
	"tern/internal/sysinfo"
)

func (a *API) registerSettings() {
	g := []Guard{NeedSession, NeedUnlocked}
	a.Handle("GET /api/v1/settings", a.handleGetSettings, g...)
	a.Handle("PUT /api/v1/settings", a.handlePutSettings, g...)
	a.Handle("POST /api/v1/settings/password", a.handleChangePassword, g...)
	a.Handle("GET /api/v1/system", a.handleSystem, g...)
}

type settingsJSON struct {
	NodeName      string `json:"node_name"`
	MaxConcurrent int    `json:"max_concurrent"`
	LogKeepDays   int    `json:"log_keep_days"`
	LogKeepRuns   int    `json:"log_keep_runs"`
}

func (a *API) metaInt(ctx context.Context, key string, def int) (int, error) {
	raw, ok, err := a.d.Store.GetMeta(ctx, key)
	if err != nil || !ok {
		return def, err
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def, nil
	}
	return n, nil
}

func (a *API) loadSettings(ctx context.Context) (settingsJSON, error) {
	var s settingsJSON
	var err error
	if s.NodeName, _, err = a.d.Store.GetMeta(ctx, metaNodeName); err != nil {
		return s, err
	}
	if s.MaxConcurrent, err = a.metaInt(ctx, scheduler.MetaMaxConcurrent, scheduler.DefaultMaxConcurrent); err != nil {
		return s, err
	}
	if s.LogKeepDays, err = a.metaInt(ctx, scheduler.MetaLogKeepDays, scheduler.DefaultLogKeepDays); err != nil {
		return s, err
	}
	s.LogKeepRuns, err = a.metaInt(ctx, scheduler.MetaLogKeepRuns, scheduler.DefaultLogKeepRuns)
	return s, err
}

func (a *API) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := a.loadSettings(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *API) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeName      string `json:"node_name"`
		MaxConcurrent int    `json:"max_concurrent"`
		LogKeepDays   int    `json:"log_keep_days"`
		LogKeepRuns   int    `json:"log_keep_runs"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	fields := map[string]string{}
	if !nodeNameRE.MatchString(req.NodeName) {
		fields["node_name"] = "invalid"
	}
	if req.MaxConcurrent < 1 || req.MaxConcurrent > 8 {
		fields["max_concurrent"] = "invalid"
	}
	if req.LogKeepDays < 1 || req.LogKeepDays > 3650 {
		fields["log_keep_days"] = "invalid"
	}
	if req.LogKeepRuns < 1 || req.LogKeepRuns > 10000 {
		fields["log_keep_runs"] = "invalid"
	}
	if len(fields) > 0 {
		writeError(w, http.StatusBadRequest, "validation", "invalid settings", fields)
		return
	}
	ctx := r.Context()
	// The node name is only a label from here on: backup tasks keep the
	// host they were created with.
	err := a.d.Store.Tx(ctx, func(tx *sql.Tx) error {
		for key, value := range map[string]string{
			metaNodeName:                req.NodeName,
			scheduler.MetaMaxConcurrent: strconv.Itoa(req.MaxConcurrent),
			scheduler.MetaLogKeepDays:   strconv.Itoa(req.LogKeepDays),
			scheduler.MetaLogKeepRuns:   strconv.Itoa(req.LogKeepRuns),
		} {
			if err := store.SetMetaTx(ctx, tx, key, value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		internalError(w, r, err)
		return
	}
	a.d.Scheduler.Reload()
	a.handleGetSettings(w, r)
}

// confirmPassword checks the current admin password for a sensitive
// change. A wrong one counts against the login limit of the caller's
// address, like a failed login. On failure the response has been written.
func (a *API) confirmPassword(w http.ResponseWriter, r *http.Request, password string) bool {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	ip := a.clientIP(r)
	if ok, wait := a.d.Limiter.Allow(ip, a.d.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many wrong passwords; try again later", nil)
		return false
	}
	hash, ok, err := a.d.Store.GetMeta(r.Context(), metaPasswordHash)
	if err != nil {
		internalError(w, r, err)
		return false
	}
	if !ok || len(password) > auth.MaxSecretLen || !auth.VerifyPassword(hash, password) {
		a.d.Limiter.Fail(ip, a.d.Now())
		writeError(w, http.StatusForbidden, "forbidden", "wrong current password", nil)
		return false
	}
	a.d.Limiter.Reset(ip)
	return true
}

func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if code := checkSecret(req.NewPassword); code != "" {
		writeError(w, http.StatusBadRequest, "validation", "invalid password", map[string]string{"new_password": code})
		return
	}
	if !a.confirmPassword(w, r, req.CurrentPassword) {
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		internalError(w, r, err)
		return
	}
	// Not to be left half done by a client that goes away.
	ctx := context.WithoutCancel(r.Context())
	if err := a.d.Store.SetMeta(ctx, metaPasswordHash, hash); err != nil {
		internalError(w, r, err)
		return
	}
	// Every session ends, this one included: whoever knew the old password
	// is out.
	if err := a.d.Sessions.DeleteAll(ctx); err != nil {
		internalError(w, r, err)
		return
	}
	a.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  a.d.Version,
		"timezone": time.Local.String(),
		"bbr":      sysinfo.ReadBBR("/proc"),
		"tools":    sysinfo.Tools(context.WithoutCancel(r.Context())), // cached: must not record a canceled probe
		"mounts":   a.d.Mounts,
	})
}
