package api

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"tern/internal/model"
)

// Whoever copies the database gets no credential out of it: every secret
// tern keeps is sealed before it reaches the store, and the one password is
// only a hash. This scans the raw files (the database with its WAL) for
// each secret, as it was typed and as the vault opens it again. The
// acceptance test TestNoSecretLeak checks the same for a real container,
// with its logs, API answers and process list.
func TestCredentialsAtRestAreSealed(t *testing.T) {
	h := newHarness(t)
	canary := func(name string) string { return "canary-" + name + "-" + model.NewID() }
	secrets := map[string]string{
		"admin password":      canary("admin"),
		"key passphrase":      canary("keypass"),
		"WebDAV password":     canary("dav"),
		"repository password": canary("repo"),
	}

	w := h.do("POST", "/api/v1/setup", map[string]any{"password": secrets["admin password"], "node_name": "nas-1"})
	if w.Code != http.StatusCreated {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	h.adopt(w)
	secrets["session cookie"] = h.cookie

	// A generated key, an imported encrypted one and an imported plain one.
	gen := h.genKey("generated")
	enc := wantStatus(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "encrypted", "mode": "import",
		"private_key": newKeyPEM(t, secrets["key passphrase"]), "passphrase": secrets["key passphrase"]}), http.StatusCreated)
	plainPEM := newKeyPEM(t, "")
	plain := wantStatus(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "plain", "mode": "import", "private_key": plainPEM}), http.StatusCreated)
	secrets["imported private key"] = pemMiddle(plainPEM)

	dav := davBody("dav")
	dav["password"] = secrets["WebDAV password"]
	davConn := wantStatus(t, h.do("POST", "/api/v1/connections", dav), http.StatusCreated)

	backup := backupBody("docs", h.nasConnID())
	backup["repo_password"] = secrets["repository password"]
	task := h.newTask(backup)

	// What the vault opens again is plaintext too, generated keys included.
	open := func(what, aad, sealed string) string {
		t.Helper()
		if !strings.HasPrefix(sealed, "v1.") {
			t.Fatalf("%s is stored as %q, not sealed", what, sealed)
		}
		p, err := h.vault.Open(aad, sealed)
		if err != nil {
			t.Fatalf("open %s: %v", what, err)
		}
		return string(p)
	}
	for _, k := range []map[string]any{gen, enc, plain} {
		id := k["id"].(string)
		pemText := open("private key", "ssh_keys/"+id+"/private", h.column("SELECT private_key FROM ssh_keys WHERE id = ?", id))
		secrets["stored private key "+k["name"].(string)] = pemMiddle(pemText)
	}
	if got := open("key passphrase", "ssh_keys/"+enc["id"].(string)+"/passphrase", h.column("SELECT passphrase FROM ssh_keys WHERE id = ?", enc["id"])); got != secrets["key passphrase"] {
		t.Fatalf("stored key passphrase opens to %q", got)
	}
	if got := open("WebDAV password", "connections/"+davConn["id"].(string)+"/password", h.column("SELECT password FROM connections WHERE id = ?", davConn["id"])); got != secrets["WebDAV password"] {
		t.Fatalf("stored WebDAV password opens to %q", got)
	}
	if got := open("repository password", "tasks/"+task["id"].(string)+"/repo_password", h.column("SELECT repo_password FROM tasks WHERE id = ?", task["id"])); got != secrets["repository password"] {
		t.Fatalf("stored repository password opens to %q", got)
	}

	scanned := 0
	for _, dir := range []string{h.dbDir, h.secrets} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			scanned++
			var hit []string
			for name, v := range secrets {
				if bytes.Contains(b, []byte(v)) {
					hit = append(hit, name)
				}
			}
			sort.Strings(hit)
			if len(hit) > 0 {
				t.Errorf("%s holds %v in plaintext", e.Name(), hit)
			}
		}
	}
	if scanned < 3 { // tern.db, its WAL, master.key at least
		t.Fatalf("scanned only %d files", scanned)
	}
}

// pemMiddle is the middle line of a PEM body: long and unique enough to
// stand for the whole key.
func pemMiddle(pemText string) string {
	lines := strings.Split(strings.TrimSpace(pemText), "\n")
	body := lines[1 : len(lines)-1]
	return body[len(body)/2]
}
