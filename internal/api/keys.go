package api

import (
	"errors"
	"net/http"
	"time"

	"tern/internal/auth"
	"tern/internal/model"
	"tern/internal/sshx"
	"tern/internal/store"
)

func (a *API) registerKeys() {
	a.Handle("GET /api/v1/ssh-keys", a.handleListKeys, NeedSession, NeedUnlocked)
	a.Handle("POST /api/v1/ssh-keys", a.handleCreateKey, NeedSession, NeedUnlocked)
	a.Handle("PUT /api/v1/ssh-keys/{id}", a.handleRenameKey, NeedSession, NeedUnlocked)
	a.Handle("DELETE /api/v1/ssh-keys/{id}", a.handleDeleteKey, NeedSession, NeedUnlocked)
}

type keyJSON struct {
	ID            string `json:"id"`
	Version       int    `json:"version"`
	Name          string `json:"name"`
	PublicKey     string `json:"public_key"`
	Fingerprint   string `json:"fingerprint"`
	HasPassphrase bool   `json:"has_passphrase"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func keyView(k model.SSHKey) keyJSON {
	return keyJSON{
		ID: k.ID, Version: k.Version, Name: k.Name, PublicKey: k.PublicKey, Fingerprint: k.Fingerprint,
		HasPassphrase: k.HasPassphrase, CreatedAt: rfc3339(k.CreatedAt), UpdatedAt: rfc3339(k.UpdatedAt),
	}
}

// storeError answers the store errors every resource handler shares.
func storeError(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such "+what, nil)
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, "conflict", "another "+what+" already has this name", nil)
	case errors.Is(err, store.ErrVersion):
		writeError(w, http.StatusConflict, "conflict", "the "+what+" was changed by someone else; reload and retry", nil)
	case errors.Is(err, store.ErrInUse):
		writeError(w, http.StatusConflict, "in_use", "the "+what+" is still in use", nil)
	default:
		internalError(w, r, err)
	}
}

func (a *API) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.d.Store.ListKeys(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := make([]keyJSON, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyView(k))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		Mode       string `json:"mode"`
		PrivateKey string `json:"private_key"`
		Passphrase string `json:"passphrase"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	fields := map[string]string{}
	if c := model.CheckName(req.Name); c != "" {
		fields["name"] = c
	}
	switch req.Mode {
	case "generate":
	case "import":
		if req.PrivateKey == "" {
			fields["private_key"] = "required"
		}
		if len(req.Passphrase) > auth.MaxSecretLen {
			fields["passphrase"] = "too_long"
		}
	default:
		fields["mode"] = "invalid"
	}
	if len(fields) > 0 {
		writeError(w, http.StatusBadRequest, "validation", "invalid ssh key request", fields)
		return
	}

	var (
		privPEM []byte
		parsed  sshx.Parsed
		err     error
	)
	if req.Mode == "generate" {
		privPEM, parsed, err = sshx.Generate()
		if err != nil {
			internalError(w, r, err)
			return
		}
	} else {
		privPEM = sshx.Normalize([]byte(req.PrivateKey))
		parsed, err = sshx.ParsePrivate(privPEM, []byte(req.Passphrase))
		switch {
		case errors.Is(err, sshx.ErrPassphraseRequired):
			writeError(w, http.StatusBadRequest, "passphrase_required", "the private key is encrypted; supply its passphrase", nil)
			return
		case errors.Is(err, sshx.ErrBadPassphrase):
			writeError(w, http.StatusBadRequest, "bad_passphrase", "the passphrase does not decrypt the private key", nil)
			return
		case err != nil:
			// The parser's own message is not echoed: it can quote key material.
			writeError(w, http.StatusBadRequest, "unsupported_key", "not a supported private key (ed25519, ecdsa or rsa in PEM form)", nil)
			return
		}
	}

	now := a.d.Now().UTC().Truncate(time.Second)
	k := model.SSHKey{
		ID: model.NewID(), Version: 1, Name: req.Name,
		PublicKey: parsed.Authorized, Fingerprint: parsed.Fingerprint, HasPassphrase: parsed.Encrypted,
		CreatedAt: now, UpdatedAt: now,
	}
	privSealed, err := a.d.Vault.Seal("ssh_keys/"+k.ID+"/private", privPEM)
	if err != nil {
		internalError(w, r, err)
		return
	}
	var passSealed string
	if parsed.Encrypted {
		if passSealed, err = a.d.Vault.Seal("ssh_keys/"+k.ID+"/passphrase", []byte(req.Passphrase)); err != nil {
			internalError(w, r, err)
			return
		}
	}
	if err := a.d.Store.CreateKey(r.Context(), k, privSealed, passSealed); err != nil {
		storeError(w, r, err, "ssh key")
		return
	}
	writeJSON(w, http.StatusCreated, keyView(k))
}

func (a *API) handleRenameKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if c := model.CheckName(req.Name); c != "" {
		writeError(w, http.StatusBadRequest, "validation", "invalid ssh key request", map[string]string{"name": c})
		return
	}
	id := r.PathValue("id")
	if err := a.d.Store.RenameKey(r.Context(), id, req.Version, req.Name); err != nil {
		storeError(w, r, err, "ssh key")
		return
	}
	k, err := a.d.Store.GetKey(r.Context(), id)
	if err != nil {
		storeError(w, r, err, "ssh key")
		return
	}
	writeJSON(w, http.StatusOK, keyView(k))
}

func (a *API) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Store.DeleteKey(r.Context(), r.PathValue("id")); err != nil {
		storeError(w, r, err, "ssh key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
