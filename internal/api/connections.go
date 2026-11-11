package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tern/internal/access"
	"tern/internal/auth"
	"tern/internal/model"
	"tern/internal/sshx"
	"tern/internal/store"
	"tern/internal/vault"
)

func (a *API) registerConnections() {
	g := []Guard{NeedSession, NeedUnlocked}
	a.Handle("POST /api/v1/connections/probe", a.handleProbe, g...)
	a.Handle("GET /api/v1/connections", a.handleListConnections, g...)
	a.Handle("POST /api/v1/connections", a.handleCreateConnection, g...)
	a.Handle("GET /api/v1/connections/{id}", a.handleGetConnection, g...)
	a.Handle("PUT /api/v1/connections/{id}", a.handleUpdateConnection, g...)
	a.Handle("DELETE /api/v1/connections/{id}", a.handleDeleteConnection, g...)
	a.Handle("POST /api/v1/connections/{id}/test", a.handleTestConnection, g...)
}

type connJSON struct {
	ID                 string `json:"id"`
	Version            int    `json:"version"`
	Type               string `json:"type"`
	Name               string `json:"name"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	SSHKeyID           string `json:"ssh_key_id"`
	HostKey            string `json:"host_key"`
	HostKeyFingerprint string `json:"host_key_fingerprint"`
	URL                string `json:"url"`
	HasPassword        bool   `json:"has_password"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

func connView(c model.Connection) connJSON {
	return connJSON{
		ID: c.ID, Version: c.Version, Type: c.Type, Name: c.Name, Host: c.Host, Port: c.Port, Username: c.Username,
		SSHKeyID: c.SSHKeyID, HostKey: c.HostKey, HostKeyFingerprint: c.HostKeyFingerprint, URL: c.URL,
		HasPassword: c.HasPassword, CreatedAt: rfc3339(c.CreatedAt), UpdatedAt: rfc3339(c.UpdatedAt),
	}
}

type connRequest struct {
	Type     string  `json:"type"`
	Name     string  `json:"name"`
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Username string  `json:"username"`
	SSHKeyID string  `json:"ssh_key_id"`
	HostKey  string  `json:"host_key"`
	URL      string  `json:"url"`
	Password *string `json:"password"` // write-only; absent on update means "keep"
	Version  int     `json:"version"`
}

// connection turns the request into a validated model. On failure it has
// already written the 400.
func (a *API) connection(w http.ResponseWriter, r *http.Request, req connRequest) (model.Connection, bool) {
	c := model.Connection{
		Type: req.Type, Name: req.Name, Host: req.Host, Port: req.Port, Username: req.Username,
		SSHKeyID: req.SSHKeyID, HostKey: strings.TrimSpace(req.HostKey), URL: req.URL,
	}
	c.ApplyDefaults()
	fields := c.Validate()
	switch c.Type {
	case model.ConnSSH:
		if req.Password != nil {
			fields["password"] = "not_allowed" // SSH is key-only
		}
		if c.HostKey != "" {
			if key, err := sshx.ParseAuthorized(c.HostKey); err != nil {
				fields["host_key"] = "invalid"
			} else {
				c.HostKey = sshx.AuthorizedKey(key)
			}
		}
		if c.SSHKeyID != "" {
			if _, err := a.d.Store.GetKey(r.Context(), c.SSHKeyID); errors.Is(err, store.ErrNotFound) {
				fields["ssh_key_id"] = "not_found"
			} else if err != nil {
				internalError(w, r, err)
				return c, false
			}
		}
	case model.ConnWebDAV:
		if req.Password != nil && len(*req.Password) > auth.MaxSecretLen {
			fields["password"] = "too_long"
		}
	}
	if len(fields) > 0 {
		writeError(w, http.StatusBadRequest, "validation", "invalid connection", fields)
		return c, false
	}
	return c, true
}

func hostKeyRequired(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "host_key_required",
		"probe the host and send back the host key you confirmed; tern never accepts one on its own", nil)
}

// sealPassword seals a WebDAV password for connection id; "" stays "".
func (a *API) sealPassword(id, password string) (string, error) {
	if password == "" {
		return "", nil
	}
	return a.d.Vault.Seal("connections/"+id+"/password", []byte(password))
}

func (a *API) handleProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}
	fields := map[string]string{}
	if c := model.CheckHost(req.Host); c != "" {
		fields["host"] = c
	}
	if c := model.CheckPort(req.Port); c != "" {
		fields["port"] = c
	}
	if len(fields) > 0 {
		writeError(w, http.StatusBadRequest, "validation", "invalid probe request", fields)
		return
	}
	hk, err := sshx.ProbeHostKey(r.Context(), req.Host, req.Port)
	if err != nil {
		writeError(w, http.StatusBadGateway, "unreachable", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"type": hk.Type, "authorized": hk.Authorized, "fingerprint": hk.Fingerprint})
}

func (a *API) handleListConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := a.d.Store.ListConnections(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := make([]connJSON, 0, len(conns))
	for _, c := range conns {
		out = append(out, connView(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetConnection(w http.ResponseWriter, r *http.Request) {
	a.writeConnection(w, r, r.PathValue("id"), http.StatusOK)
}

func (a *API) writeConnection(w http.ResponseWriter, r *http.Request, id string, status int) {
	c, err := a.d.Store.GetConnection(r.Context(), id)
	if err != nil {
		storeError(w, r, err, "connection")
		return
	}
	writeJSON(w, status, connView(c))
}

func (a *API) handleCreateConnection(w http.ResponseWriter, r *http.Request) {
	var req connRequest
	if !readJSON(w, r, &req) {
		return
	}
	c, ok := a.connection(w, r, req)
	if !ok {
		return
	}
	if c.Type == model.ConnSSH && c.HostKey == "" {
		hostKeyRequired(w)
		return
	}
	now := a.d.Now().UTC().Truncate(time.Second)
	c.ID, c.Version, c.CreatedAt, c.UpdatedAt = model.NewID(), 1, now, now
	var sealed string
	if req.Password != nil {
		var err error
		if sealed, err = a.sealPassword(c.ID, *req.Password); err != nil {
			internalError(w, r, err)
			return
		}
	}
	if err := a.d.Store.CreateConnection(r.Context(), c, sealed); err != nil {
		storeError(w, r, err, "connection")
		return
	}
	a.writeConnection(w, r, c.ID, http.StatusCreated)
}

// sameOrigin reports whether two validated URLs name the same server.
func sameOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && ua.Scheme == ub.Scheme && strings.EqualFold(ua.Host, ub.Host)
}

func (a *API) handleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	var req connRequest
	if !readJSON(w, r, &req) {
		return
	}
	old, err := a.d.Store.GetConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, r, err, "connection")
		return
	}
	if req.Type != old.Type {
		writeError(w, http.StatusBadRequest, "validation", "invalid connection", map[string]string{"type": "immutable"})
		return
	}
	c, ok := a.connection(w, r, req)
	if !ok {
		return
	}
	switch c.Type {
	case model.ConnSSH:
		// A key confirmed for one address says nothing about another: moving
		// the connection requires a newly probed and confirmed key.
		moved := c.Host != old.Host || c.Port != old.Port
		if c.HostKey == "" || (moved && c.HostKey == old.HostKey) {
			hostKeyRequired(w)
			return
		}
	case model.ConnWebDAV:
		// The stored password is write-only; it does not follow the connection
		// to a different server.
		if req.Password == nil && old.HasPassword && !sameOrigin(c.URL, old.URL) {
			writeError(w, http.StatusBadRequest, "validation", "invalid connection", map[string]string{"password": "required"})
			return
		}
	}
	c.ID, c.Version, c.UpdatedAt = old.ID, req.Version, a.d.Now().UTC().Truncate(time.Second)
	var sealed *string
	if req.Password != nil {
		s, err := a.sealPassword(c.ID, *req.Password)
		if err != nil {
			internalError(w, r, err)
			return
		}
		sealed = &s
	}
	if err := a.d.Store.UpdateConnection(r.Context(), c, sealed); err != nil {
		storeError(w, r, err, "connection")
		return
	}
	a.writeConnection(w, r, c.ID, http.StatusOK)
}

func (a *API) handleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	// Tasks keep their connection IDs inside their spec, where the database
	// cannot enforce the reference.
	users, err := a.d.Store.TasksUsingConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		internalError(w, r, err)
		return
	}
	if len(users) > 0 {
		writeError(w, http.StatusConflict, "in_use", "the connection is still used by: "+strings.Join(users, ", "), nil)
		return
	}
	if err := a.d.Store.DeleteConnection(r.Context(), r.PathValue("id")); err != nil {
		storeError(w, r, err, "connection")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestConnection runs the preflight. A connection that fails it is a
// normal answer ({"ok":false,…}), not an HTTP error.
func (a *API) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	type result struct {
		OK      bool   `json:"ok"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	err := access.Opener{Store: a.d.Store, Vault: a.d.Vault}.Check(r.Context(), r.PathValue("id"))
	var ke *model.KindError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, result{OK: true})
	case errors.As(err, &ke):
		writeJSON(w, http.StatusOK, result{Kind: string(ke.Kind), Message: ke.Error()})
	case errors.Is(err, vault.ErrLocked):
		// Locked between the guard and here.
		writeError(w, http.StatusServiceUnavailable, "vault_locked", "the credential vault is locked; unlock it with the passphrase", nil)
	default:
		storeError(w, r, err, "connection")
	}
}
