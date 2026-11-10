package api

import (
	"context"
	"database/sql"
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
	_, authed, err := a.lookupSession(r)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        a.d.Version,
		"setup_required": !done,
		"vault_state":    a.d.Vault.State(),
		"authenticated":  authed,
	})
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

	a.setupMu.Lock()
	defer a.setupMu.Unlock()
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
	if err := a.d.Vault.Init(ctx, mode, req.Passphrase); err != nil {
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
	a.startSession(w, r, http.StatusCreated)
}

// startSession creates a session, sets its cookie and answers {csrf_token}.
func (a *API) startSession(w http.ResponseWriter, r *http.Request, status int) {
	token, csrf, err := a.d.Sessions.Create(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	setSessionCookie(w, r, token)
	writeJSON(w, status, map[string]string{"csrf_token": csrf})
}
