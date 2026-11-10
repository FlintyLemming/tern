package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"tern/internal/model"
	"tern/internal/sshx/sshtest"
)

const davPass = "dav-s3cret-token"

func (h *harness) probe(srv *sshtest.Server) string {
	h.t.Helper()
	p := wantStatus(h.t, h.do("POST", "/api/v1/connections/probe", map[string]any{"host": srv.Host(), "port": srv.Port()}), http.StatusOK)
	return p["authorized"].(string)
}

func sshBody(name string, srv *sshtest.Server, keyID, hostKey string) map[string]any {
	b := map[string]any{"type": "ssh", "name": name, "host": srv.Host(), "port": srv.Port(), "username": "backup", "ssh_key_id": keyID}
	if hostKey != "" {
		b["host_key"] = hostKey
	}
	return b
}

func davBody(name string) map[string]any {
	return map[string]any{"type": "webdav", "name": name, "url": "https://dav.example/remote.php/dav", "username": "me@example.com", "password": davPass}
}

// newSSHConn creates a key and an SSH connection to srv, returning the connection.
func (h *harness) newSSHConn(srv *sshtest.Server) map[string]any {
	h.t.Helper()
	k := h.genKey("key-" + srv.Addr())
	return wantStatus(h.t, h.do("POST", "/api/v1/connections", sshBody("conn-"+srv.Addr(), srv, k["id"].(string), h.probe(srv))), http.StatusCreated)
}

func (h *harness) column(query string, args ...any) string {
	h.t.Helper()
	var s string
	if err := h.st.DB().QueryRow(query, args...).Scan(&s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

func TestProbeEndpoint(t *testing.T) {
	h := readyHarness(t)
	srv := sshtest.Start(t)
	p := wantStatus(t, h.do("POST", "/api/v1/connections/probe", map[string]any{"host": srv.Host(), "port": srv.Port()}), http.StatusOK)
	if p["fingerprint"] != ssh.FingerprintSHA256(srv.HostPublicKey()) || p["type"] != "ssh-ed25519" {
		t.Errorf("probe = %v", p)
	}
	if a, _ := p["authorized"].(string); !strings.HasPrefix(a, "ssh-ed25519 AAAA") || len(p) != 3 {
		t.Errorf("probe = %v", p)
	}
}

func TestProbeUnreachable(t *testing.T) {
	h := readyHarness(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	wantError(t, h.do("POST", "/api/v1/connections/probe", map[string]any{"host": "127.0.0.1", "port": port}), http.StatusBadGateway, "unreachable")
}

func TestProbeValidates(t *testing.T) {
	h := readyHarness(t)
	wantField(t, wantError(t, h.do("POST", "/api/v1/connections/probe", map[string]any{"host": "-oProxyCommand=x", "port": 22}), http.StatusBadRequest, "validation"), "host")
	wantField(t, wantError(t, h.do("POST", "/api/v1/connections/probe", map[string]any{"host": "h", "port": 70000}), http.StatusBadRequest, "validation"), "port")
}

func TestCreateSSHRequiresHostKey(t *testing.T) {
	h := readyHarness(t)
	srv := sshtest.Start(t)
	k := h.genKey("k")
	wantError(t, h.do("POST", "/api/v1/connections", sshBody("c", srv, k["id"].(string), "")), http.StatusBadRequest, "host_key_required")
	if n := h.count("connections"); n != 0 {
		t.Errorf("connections has %d rows", n)
	}
}

func TestCreateSSHOK(t *testing.T) {
	h := readyHarness(t)
	srv := sshtest.Start(t)
	k := h.genKey("k")
	hostKey := h.probe(srv)
	c := wantStatus(t, h.do("POST", "/api/v1/connections", sshBody("nas2", srv, k["id"].(string), hostKey)), http.StatusCreated)
	if c["host_key_fingerprint"] != ssh.FingerprintSHA256(srv.HostPublicKey()) || c["host_key"] != hostKey {
		t.Errorf("host key = %v / %v", c["host_key"], c["host_key_fingerprint"])
	}
	if c["type"] != "ssh" || c["name"] != "nas2" || c["host"] != srv.Host() || c["port"] != float64(srv.Port()) ||
		c["username"] != "backup" || c["ssh_key_id"] != k["id"] || c["version"] != float64(1) ||
		c["url"] != "" || c["has_password"] != false || c["created_at"] != "2026-11-10T12:00:00Z" || len(c) != 14 {
		t.Errorf("connection = %v", c)
	}
	got := wantStatus(t, h.do("GET", "/api/v1/connections/"+c["id"].(string), nil), http.StatusOK)
	if got["host_key_fingerprint"] != c["host_key_fingerprint"] || got["id"] != c["id"] {
		t.Errorf("GET = %v", got)
	}
	if w := h.do("GET", "/api/v1/connections", nil); w.Code != http.StatusOK || strings.Count(w.Body.String(), `"id"`) != 1 || !strings.HasPrefix(w.Body.String(), "[") {
		t.Errorf("list = %d %s", w.Code, w.Body)
	}
	wantError(t, h.do("GET", "/api/v1/connections/nope", nil), http.StatusNotFound, "not_found")
}

func TestCreateSSHDefaultsPort(t *testing.T) {
	h := readyHarness(t)
	srv := sshtest.Start(t)
	k := h.genKey("k")
	b := sshBody("c", srv, k["id"].(string), h.probe(srv))
	delete(b, "port")
	c := wantStatus(t, h.do("POST", "/api/v1/connections", b), http.StatusCreated)
	if c["port"] != float64(22) {
		t.Errorf("port = %v", c["port"])
	}
}

func TestCreateSSHValidation(t *testing.T) {
	h := readyHarness(t)
	srv := sshtest.Start(t)
	k := h.genKey("k")
	hostKey := h.probe(srv)
	mut := func(field string, v any) map[string]any {
		b := sshBody("c", srv, k["id"].(string), hostKey)
		b[field] = v
		return b
	}
	wantField(t, wantError(t, h.do("POST", "/api/v1/connections", mut("host", "a\nProxyCommand x")), http.StatusBadRequest, "validation"), "host")
	wantField(t, wantError(t, h.do("POST", "/api/v1/connections", mut("username", "-oProxyCommand=x")), http.StatusBadRequest, "validation"), "username")
	wantField(t, wantError(t, h.do("POST", "/api/v1/connections", mut("ssh_key_id", "nope")), http.StatusBadRequest, "validation"), "ssh_key_id")
	wantField(t, wantError(t, h.do("POST", "/api/v1/connections", mut("host_key", "not a key")), http.StatusBadRequest, "validation"), "host_key")
	wantField(t, wantError(t, h.do("POST", "/api/v1/connections", mut("host_key", hostKey+"\nssh-ed25519 AAAA")), http.StatusBadRequest, "validation"), "host_key")
	wantField(t, wantError(t, h.do("POST", "/api/v1/connections", mut("type", "ftp")), http.StatusBadRequest, "validation"), "type")
	wantField(t, wantError(t, h.do("POST", "/api/v1/connections", mut("password", "x")), http.StatusBadRequest, "validation"), "password")
	if n := h.count("connections"); n != 0 {
		t.Errorf("connections has %d rows", n)
	}
}

func TestConnectionNameDuplicate(t *testing.T) {
	h := readyHarness(t)
	wantStatus(t, h.do("POST", "/api/v1/connections", davBody("same")), http.StatusCreated)
	wantError(t, h.do("POST", "/api/v1/connections", davBody("same")), http.StatusConflict, "conflict")
}

func TestUpdateHostNeedsNewHostKey(t *testing.T) {
	h := readyHarness(t)
	srv := sshtest.Start(t)
	c := h.newSSHConn(srv)
	path := "/api/v1/connections/" + c["id"].(string)
	body := func(host string, port any, hostKey any) map[string]any {
		b := map[string]any{"type": "ssh", "name": c["name"], "host": host, "port": port, "username": "backup",
			"ssh_key_id": c["ssh_key_id"], "version": 1}
		if hostKey != nil {
			b["host_key"] = hostKey
		}
		return b
	}
	wantError(t, h.do("PUT", path, body("other.example", c["port"], c["host_key"])), http.StatusBadRequest, "host_key_required")
	wantError(t, h.do("PUT", path, body(c["host"].(string), 2200, c["host_key"])), http.StatusBadRequest, "host_key_required")
	wantError(t, h.do("PUT", path, body(c["host"].(string), c["port"], nil)), http.StatusBadRequest, "host_key_required")
	if v := h.column("SELECT version FROM connections WHERE id = ?", c["id"]); v != "1" {
		t.Errorf("version = %s after refused updates", v)
	}

	// A new host with a freshly confirmed key goes through.
	other := sshtest.Start(t)
	b := body(other.Host(), other.Port(), h.probe(other))
	got := wantStatus(t, h.do("PUT", path, b), http.StatusOK)
	if got["version"] != float64(2) || got["host_key_fingerprint"] != ssh.FingerprintSHA256(other.HostPublicKey()) || got["port"] != float64(other.Port()) {
		t.Errorf("updated = %v", got)
	}
}

func TestUpdateSSHSameHostKeepsKey(t *testing.T) {
	h := readyHarness(t)
	srv := sshtest.Start(t)
	c := h.newSSHConn(srv)
	b := sshBody("renamed", srv, c["ssh_key_id"].(string), c["host_key"].(string))
	b["version"] = 1
	b["username"] = "other"
	got := wantStatus(t, h.do("PUT", "/api/v1/connections/"+c["id"].(string), b), http.StatusOK)
	if got["name"] != "renamed" || got["username"] != "other" || got["version"] != float64(2) || got["host_key"] != c["host_key"] {
		t.Errorf("updated = %v", got)
	}
	b["type"], b["version"] = "webdav", 2
	wantField(t, wantError(t, h.do("PUT", "/api/v1/connections/"+c["id"].(string), b), http.StatusBadRequest, "validation"), "type")
}

func TestWebDAVPasswordWriteOnly(t *testing.T) {
	h := readyHarness(t)
	w := h.do("POST", "/api/v1/connections", davBody("dav"))
	c := wantStatus(t, w, http.StatusCreated)
	if strings.Contains(w.Body.String(), davPass) || c["has_password"] != true {
		t.Errorf("create response: %s", w.Body)
	}
	if _, has := c["password"]; has {
		t.Error("response has a password field")
	}
	id := c["id"].(string)
	for _, path := range []string{"/api/v1/connections", "/api/v1/connections/" + id} {
		if w := h.do("GET", path, nil); w.Code != http.StatusOK || strings.Contains(w.Body.String(), davPass) || strings.Contains(w.Body.String(), "v1.") {
			t.Errorf("GET %s = %d %s", path, w.Code, w.Body)
		}
	}
	sealed := h.column("SELECT password FROM connections WHERE id = ?", id)
	if !strings.HasPrefix(sealed, "v1.") {
		t.Fatalf("stored password = %.12q", sealed)
	}
	if plain, err := h.vault.Open("connections/"+id+"/password", sealed); err != nil || string(plain) != davPass {
		t.Errorf("password does not open under its AAD: %v", err)
	}
}

func TestWebDAVRejectsEmbeddedCredentials(t *testing.T) {
	h := readyHarness(t)
	b := davBody("dav")
	b["url"] = "https://me:" + davPass + "@dav.example/"
	w := h.do("POST", "/api/v1/connections", b)
	e := wantError(t, w, http.StatusBadRequest, "validation")
	if f, _ := e["fields"].(map[string]any); f["url"] != "embedded_credentials" {
		t.Errorf("fields = %v", f)
	}
	if strings.Contains(w.Body.String(), davPass) {
		t.Errorf("error echoes the credential: %s", w.Body)
	}
}

func TestWebDAVUpdateKeepsPassword(t *testing.T) {
	h := readyHarness(t)
	c := wantStatus(t, h.do("POST", "/api/v1/connections", davBody("dav")), http.StatusCreated)
	id := c["id"].(string)
	before := h.column("SELECT password FROM connections WHERE id = ?", id)

	b := davBody("dav renamed")
	delete(b, "password")
	b["version"] = 1
	got := wantStatus(t, h.do("PUT", "/api/v1/connections/"+id, b), http.StatusOK)
	if got["name"] != "dav renamed" || got["has_password"] != true || got["version"] != float64(2) {
		t.Errorf("updated = %v", got)
	}
	if after := h.column("SELECT password FROM connections WHERE id = ?", id); after != before {
		t.Error("password ciphertext changed by an update without a password field")
	}

	b["password"], b["version"] = "a-new-passw0rd", 2
	wantStatus(t, h.do("PUT", "/api/v1/connections/"+id, b), http.StatusOK)
	after := h.column("SELECT password FROM connections WHERE id = ?", id)
	if plain, err := h.vault.Open("connections/"+id+"/password", after); err != nil || string(plain) != "a-new-passw0rd" {
		t.Errorf("new password not stored: %v", err)
	}
}

// Pointing a WebDAV connection at another server must not carry the stored
// password along: it is write-only, and the new server would receive it.
func TestWebDAVNewHostNeedsPassword(t *testing.T) {
	h := readyHarness(t)
	c := wantStatus(t, h.do("POST", "/api/v1/connections", davBody("dav")), http.StatusCreated)
	path := "/api/v1/connections/" + c["id"].(string)
	b := davBody("dav")
	delete(b, "password")
	b["version"] = 1
	for _, u := range []string{"https://evil.example/remote.php/dav", "http://dav.example/remote.php/dav", "https://dav.example:8443/remote.php/dav"} {
		b["url"] = u
		wantField(t, wantError(t, h.do("PUT", path, b), http.StatusBadRequest, "validation"), "password")
	}
	b["url"] = "https://dav.example/another/path" // same server, different path: fine
	wantStatus(t, h.do("PUT", path, b), http.StatusOK)
	b["url"], b["password"], b["version"] = "https://new.example/dav", davPass, 2
	wantStatus(t, h.do("PUT", path, b), http.StatusOK)
}

func TestDeleteKeyInUse(t *testing.T) {
	h := readyHarness(t)
	srv := sshtest.Start(t)
	c := h.newSSHConn(srv)
	keyPath := "/api/v1/ssh-keys/" + c["ssh_key_id"].(string)
	wantError(t, h.do("DELETE", keyPath, nil), http.StatusConflict, "in_use")
	if w := h.do("DELETE", "/api/v1/connections/"+c["id"].(string), nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete connection = %d %s", w.Code, w.Body)
	}
	wantError(t, h.do("DELETE", "/api/v1/connections/"+c["id"].(string), nil), http.StatusNotFound, "not_found")
	if w := h.do("DELETE", keyPath, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete key = %d %s", w.Code, w.Body)
	}
}

func TestConnectionVersionConflict(t *testing.T) {
	h := readyHarness(t)
	c := wantStatus(t, h.do("POST", "/api/v1/connections", davBody("dav")), http.StatusCreated)
	path := "/api/v1/connections/" + c["id"].(string)
	b := davBody("dav2")
	b["version"] = 1
	wantStatus(t, h.do("PUT", path, b), http.StatusOK)
	b["name"] = "dav3"
	wantError(t, h.do("PUT", path, b), http.StatusConflict, "conflict")
	wantError(t, h.do("PUT", "/api/v1/connections/nope", b), http.StatusNotFound, "not_found")
}

func TestConnectionsNeedUnlockedVault(t *testing.T) {
	h := newHarness(t)
	h.setup("passphrase")
	h.restart()
	h.login()
	wantError(t, h.do("GET", "/api/v1/connections", nil), http.StatusServiceUnavailable, "vault_locked")
	wantError(t, h.do("POST", "/api/v1/connections/probe", map[string]any{"host": "h", "port": 22}), http.StatusServiceUnavailable, "vault_locked")
}

// ---- test connection ----

// keyAndServer generates a key through the API and starts an SSH server that
// authorizes it (or nobody, when authorize is false).
func (h *harness) keyAndServer(authorize bool, opts ...sshtest.Option) (keyID string, pub ssh.PublicKey, srv *sshtest.Server) {
	h.t.Helper()
	k := h.genKey("key-" + model.NewID())
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k["public_key"].(string)))
	if err != nil {
		h.t.Fatal(err)
	}
	if authorize {
		opts = append(opts, sshtest.WithAuthorized(pub))
	}
	return k["id"].(string), pub, sshtest.Start(h.t, opts...)
}

func (h *harness) testConn(id string) map[string]any {
	h.t.Helper()
	return wantStatus(h.t, h.do("POST", "/api/v1/connections/"+id+"/test", nil), http.StatusOK)
}

func TestConnectionTestOK(t *testing.T) {
	h := readyHarness(t)
	keyID, _, srv := h.keyAndServer(true)
	c := wantStatus(t, h.do("POST", "/api/v1/connections", sshBody("c", srv, keyID, h.probe(srv))), http.StatusCreated)
	res := h.testConn(c["id"].(string))
	if res["ok"] != true || res["kind"] != "" || res["message"] != "" || len(res) != 3 {
		t.Errorf("test = %v", res)
	}
}

func TestConnectionTestAuth(t *testing.T) {
	h := readyHarness(t)
	keyID, _, srv := h.keyAndServer(false)
	c := wantStatus(t, h.do("POST", "/api/v1/connections", sshBody("c", srv, keyID, h.probe(srv))), http.StatusCreated)
	res := h.testConn(c["id"].(string))
	if res["ok"] != false || res["kind"] != "auth" || res["message"] == "" {
		t.Errorf("test = %v", res)
	}
}

func TestConnectionTestHostKeyChanged(t *testing.T) {
	h := readyHarness(t)
	keyID, pub, srv := h.keyAndServer(true)
	c := wantStatus(t, h.do("POST", "/api/v1/connections", sshBody("c", srv, keyID, h.probe(srv))), http.StatusCreated)
	if res := h.testConn(c["id"].(string)); res["ok"] != true {
		t.Fatalf("before the change: %v", res)
	}
	addr := srv.Addr()
	srv.Close()
	sshtest.Start(t, sshtest.WithListen(addr), sshtest.WithAuthorized(pub)) // same address, new host key
	res := h.testConn(c["id"].(string))
	if res["ok"] != false || res["kind"] != "host_key" {
		t.Errorf("test = %v", res)
	}
}

func TestConnectionTestWebDAV(t *testing.T) {
	h := readyHarness(t)
	var gotPass string
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotPass, _ = r.BasicAuth()
		if gotPass != davPass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer dav.Close()
	b := davBody("dav")
	b["url"] = dav.URL + "/dav/"
	c := wantStatus(t, h.do("POST", "/api/v1/connections", b), http.StatusCreated)
	if res := h.testConn(c["id"].(string)); res["ok"] != true {
		t.Errorf("test = %v", res)
	}
	b["password"], b["version"] = "wrong-password", 1
	wantStatus(t, h.do("PUT", "/api/v1/connections/"+c["id"].(string), b), http.StatusOK)
	w := h.do("POST", "/api/v1/connections/"+c["id"].(string)+"/test", nil)
	res := wantStatus(t, w, http.StatusOK)
	if res["ok"] != false || res["kind"] != "auth" || strings.Contains(w.Body.String(), "wrong-password") {
		t.Errorf("test = %s", w.Body)
	}
}

func TestConnectionTestUnknown(t *testing.T) {
	h := readyHarness(t)
	wantError(t, h.do("POST", "/api/v1/connections/nope/test", nil), http.StatusNotFound, "not_found")
}
