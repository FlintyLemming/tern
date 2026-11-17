package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"unicode/utf8"

	"tern/internal/auth"
	"tern/internal/model"
	"tern/internal/store"
	"tern/internal/vault"
)

const (
	metaPasswordHash = "admin.password_hash"
	metaNodeID       = "node.id"
	metaNodeName     = "node.name"
)

var nodeNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// checkSecret validates a new password or passphrase: at least
// auth.MinPasswordLen characters and at most auth.MaxSecretLen bytes.
// It returns a field error code, or "" when the value is acceptable.
func checkSecret(s string) string {
	switch {
	case s == "":
		return "required"
	case len(s) > auth.MaxSecretLen:
		return "too_long"
	case utf8.RuneCountInString(s) < auth.MinPasswordLen:
		return "too_short"
	}
	return ""
}

func (a *API) setupDone(ctx context.Context) (bool, error) {
	_, ok, err := a.d.Store.GetMeta(ctx, metaPasswordHash)
	return ok, err
}

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	done, err := a.setupDone(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	s, authed, err := a.lookupSession(r)
	if err != nil {
		internalError(w, r, err)
		return
	}
	body := map[string]any{
		"version":        a.d.Version,
		"setup_required": !done,
		"vault_state":    a.d.Vault.State(),
		"authenticated":  authed,
		"mounts":         a.d.Mounts,
		"platform":       a.d.Cfg.Platform,
	}
	if authed {
		body["signed_in_via"] = "password"
		if s.qtsUser != "" {
			body["signed_in_via"] = "qts"
			body["qts_user"] = s.qtsUser
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// Refusals for writes that would land in the container's own layer.
func writeDataNotMounted(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "data_not_mounted",
		"the data directory is not a mounted volume; everything would be lost when the container is recreated", nil)
}

func writeSecretsNotMounted(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "secrets_not_mounted",
		"the secrets directory is not a mounted volume; the key file would be lost when the container is recreated", nil)
}

func (a *API) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password   string `json:"password"`
		NodeName   string `json:"node_name"`
		UnlockMode string `json:"unlock_mode"`
		Passphrase string `json:"passphrase"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var qtsSession session
	if a.d.QTS != nil {
		// tern is reachable through QTS's port without signing in to QTS;
		// only a QTS administrator may claim a new node.
		s, ok, err := a.lookupQTS(r)
		if err != nil {
			internalError(w, r, err)
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "qts_admin_required", "sign in to QTS as an administrator to set up tern", nil)
			return
		}
		qtsSession = s
	}
	if !a.d.Mounts.Data {
		writeDataNotMounted(w)
		return
	}

	fields := map[string]string{}
	if code := checkSecret(req.Password); code != "" {
		fields["password"] = code
	}
	if !nodeNameRE.MatchString(req.NodeName) {
		fields["node_name"] = "invalid"
	}
	mode := vault.Mode(req.UnlockMode)
	switch mode {
	case vault.ModeKeyfile:
		req.Passphrase = "" // not used in this mode
	case vault.ModePassphrase:
		if code := checkSecret(req.Passphrase); code != "" {
			fields["passphrase"] = code
		}
	default:
		fields["unlock_mode"] = "invalid"
	}
	if len(fields) > 0 {
		writeError(w, http.StatusBadRequest, "validation", "invalid setup request", fields)
		return
	}
	if mode == vault.ModeKeyfile && !a.d.Mounts.Secrets {
		writeSecretsNotMounted(w)
		return
	}

	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	// From here on the request runs to completion even if the client goes
	// away: setup is several writes, and stopping between them strands it.
	r = r.WithContext(context.WithoutCancel(r.Context()))
	ctx := r.Context()

	done, err := a.setupDone(ctx)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if done {
		writeError(w, http.StatusConflict, "already_setup", "this node has already been set up", nil)
		return
	}
	// Hash first: it is the one step that cannot leave anything behind.
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		internalError(w, r, err)
		return
	}
	err = a.d.Vault.Init(ctx, mode, req.Passphrase)
	if errors.Is(err, vault.ErrAlreadyInitialized) {
		// An earlier attempt initialized the vault and died before the
		// password was stored. Nothing can have been sealed yet: finish it.
		if conflict := a.resumeVault(ctx, mode, req.Passphrase); conflict != "" {
			writeError(w, http.StatusConflict, "conflict", conflict, nil)
			return
		}
		err = nil
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	err = a.d.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.SetMetaTx(ctx, tx, metaNodeID, model.NewID()); err != nil {
			return err
		}
		if err := store.SetMetaTx(ctx, tx, metaNodeName, req.NodeName); err != nil {
			return err
		}
		return store.SetMetaTx(ctx, tx, metaPasswordHash, hash)
	})
	if err != nil {
		internalError(w, r, err)
		return
	}
	a.syncPaused() // the vault exists and is open now: scheduling may begin
	if qtsSession.qtsUser != "" {
		// The QTS sign-in carries on; a tern session of its own would
		// outlive signing out of QTS.
		writeJSON(w, http.StatusCreated, map[string]string{"csrf_token": qtsSession.csrf})
		return
	}
	a.startSession(w, r, http.StatusCreated)
}

// startSession creates a session, sets its cookie and answers {csrf_token}.
func (a *API) startSession(w http.ResponseWriter, r *http.Request, status int) {
	token, csrf, err := a.d.Sessions.Create(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	a.setSessionCookie(w, r, token)
	writeJSON(w, status, map[string]string{"csrf_token": csrf})
}

// resumeVault brings a vault left behind by an interrupted setup to the mode
// and passphrase of this attempt. It returns "" on success, otherwise an
// English explanation of why this attempt cannot take the vault over.
func (a *API) resumeVault(ctx context.Context, mode vault.Mode, passphrase string) string {
	v := a.d.Vault
	const startOver = "to start over, stop tern and delete tern.db from the data directory"
	switch v.State() {
	case vault.StateUnlocked:
	case vault.StateLocked:
		if mode != vault.ModePassphrase {
			return "an interrupted setup already chose passphrase mode; retry with unlock_mode passphrase and the same passphrase; " + startOver
		}
		if err := v.Unlock(ctx, passphrase); err != nil {
			return "an interrupted setup already set a different passphrase; retry with that passphrase; " + startOver
		}
	default:
		return "an interrupted setup created a vault whose key file is now " + string(v.State()) + "; " + startOver
	}
	// Unlocked: re-wrap the (still unused) data key for this attempt's choice.
	if err := v.SwitchMode(ctx, mode, passphrase); err != nil {
		slog.Error("resuming interrupted setup failed", "err", err)
		return "could not finish the interrupted setup; " + startOver
	}
	return ""
}
