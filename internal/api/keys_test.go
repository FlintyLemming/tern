package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

const keyPass = "pp-123456"

// newKeyPEM returns a fresh ed25519 private key, encrypted when pass is set.
func newKeyPEM(t *testing.T, pass string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var blk *pem.Block
	if pass == "" {
		blk, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		blk, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(pass))
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(blk))
}

func readyHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.setup()
	return h
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body %s", w.Code, status, w.Body)
	}
	if w.Body.Len() == 0 {
		return nil
	}
	return decode(t, w)
}

func (h *harness) genKey(name string) map[string]any {
	h.t.Helper()
	return wantStatus(h.t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": name, "mode": "generate"}), http.StatusCreated)
}

func (h *harness) count(table string) int {
	h.t.Helper()
	var n int
	if err := h.st.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func TestCreateKeyGenerate(t *testing.T) {
	h := readyHarness(t)
	w := h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "nas", "mode": "generate"})
	k := wantStatus(t, w, http.StatusCreated)
	if pk, _ := k["public_key"].(string); !strings.HasPrefix(pk, "ssh-ed25519 ") {
		t.Errorf("public_key = %v", k["public_key"])
	}
	if fp, _ := k["fingerprint"].(string); !strings.HasPrefix(fp, "SHA256:") {
		t.Errorf("fingerprint = %v", k["fingerprint"])
	}
	if k["name"] != "nas" || k["version"] != float64(1) || k["has_passphrase"] != false || len(k["id"].(string)) != 32 {
		t.Errorf("key = %v", k)
	}
	if k["created_at"] != "2026-11-10T12:00:00Z" || k["updated_at"] != "2026-11-10T12:00:00Z" {
		t.Errorf("times = %v %v", k["created_at"], k["updated_at"])
	}
	if strings.Contains(w.Body.String(), "PRIVATE KEY") || len(k) != 8 {
		t.Errorf("response leaks or has extra fields: %s", w.Body)
	}
}

func TestCreateKeyImport(t *testing.T) {
	h := readyHarness(t)
	w := h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "imp", "mode": "import", "private_key": newKeyPEM(t, keyPass), "passphrase": keyPass})
	k := wantStatus(t, w, http.StatusCreated)
	if k["has_passphrase"] != true {
		t.Errorf("has_passphrase = %v", k["has_passphrase"])
	}
	if strings.Contains(w.Body.String(), "PRIVATE KEY") || strings.Contains(w.Body.String(), keyPass) {
		t.Errorf("response leaks: %s", w.Body)
	}
	// An unencrypted key imported with a stray passphrase keeps no passphrase.
	k = wantStatus(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "plain", "mode": "import", "private_key": newKeyPEM(t, ""), "passphrase": keyPass}), http.StatusCreated)
	if k["has_passphrase"] != false {
		t.Errorf("plain key has_passphrase = %v", k["has_passphrase"])
	}
}

func TestImportNeedsPassphrase(t *testing.T) {
	h := readyHarness(t)
	wantError(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "imp", "mode": "import", "private_key": newKeyPEM(t, keyPass)}), http.StatusBadRequest, "passphrase_required")
	if n := h.count("ssh_keys"); n != 0 {
		t.Errorf("ssh_keys has %d rows", n)
	}
}

func TestImportBadPassphrase(t *testing.T) {
	h := readyHarness(t)
	wantError(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "imp", "mode": "import", "private_key": newKeyPEM(t, keyPass), "passphrase": "wrong-one"}), http.StatusBadRequest, "bad_passphrase")
}

func TestImportGarbage(t *testing.T) {
	h := readyHarness(t)
	wantError(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "imp", "mode": "import", "private_key": "not a key"}), http.StatusBadRequest, "unsupported_key")
}

func TestKeyValidation(t *testing.T) {
	h := readyHarness(t)
	wantField(t, wantError(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "", "mode": "generate"}), http.StatusBadRequest, "validation"), "name")
	wantField(t, wantError(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": strings.Repeat("x", 65), "mode": "generate"}), http.StatusBadRequest, "validation"), "name")
	wantField(t, wantError(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "a\nb", "mode": "generate"}), http.StatusBadRequest, "validation"), "name")
	wantField(t, wantError(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "ok", "mode": "steal"}), http.StatusBadRequest, "validation"), "mode")
	wantField(t, wantError(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "ok", "mode": "import"}), http.StatusBadRequest, "validation"), "private_key")
	if n := h.count("ssh_keys"); n != 0 {
		t.Errorf("ssh_keys has %d rows", n)
	}
}

func TestKeysNeverReturnPrivate(t *testing.T) {
	h := readyHarness(t)
	h.genKey("gen")
	wantStatus(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "imp", "mode": "import", "private_key": newKeyPEM(t, keyPass), "passphrase": keyPass}), http.StatusCreated)
	w := h.do("GET", "/api/v1/ssh-keys", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Contains(body, "PRIVATE KEY") || strings.Contains(body, keyPass) || strings.Contains(body, "v1.") {
		t.Errorf("list leaks: %s", body)
	}
	if strings.Count(body, `"public_key"`) != 2 {
		t.Errorf("list = %s", body)
	}
}

func TestKeysListEmptyIsArray(t *testing.T) {
	h := readyHarness(t)
	if w := h.do("GET", "/api/v1/ssh-keys", nil); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("empty list = %d %q", w.Code, w.Body)
	}
}

func TestKeyStoredSealed(t *testing.T) {
	h := readyHarness(t)
	wantStatus(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "imp", "mode": "import", "private_key": newKeyPEM(t, keyPass), "passphrase": keyPass}), http.StatusCreated)
	var id, priv, pass string
	if err := h.st.DB().QueryRow("SELECT id, private_key, passphrase FROM ssh_keys").Scan(&id, &priv, &pass); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(priv, "v1.") || !strings.HasPrefix(pass, "v1.") {
		t.Fatalf("stored columns: %.12q %.12q", priv, pass)
	}
	plain, err := h.vault.Open("ssh_keys/"+id+"/private", priv)
	if err != nil || !strings.Contains(string(plain), "OPENSSH PRIVATE KEY") {
		t.Errorf("private does not open under its AAD: %v", err)
	}
	if pp, err := h.vault.Open("ssh_keys/"+id+"/passphrase", pass); err != nil || string(pp) != keyPass {
		t.Errorf("passphrase does not open under its AAD: %v", err)
	}
	if _, err := h.vault.Open("ssh_keys/"+id+"/passphrase", priv); err == nil {
		t.Error("private key opened under the passphrase AAD")
	}
}

func TestKeyNameDuplicate(t *testing.T) {
	h := readyHarness(t)
	h.genKey("same")
	wantError(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "same", "mode": "generate"}), http.StatusConflict, "conflict")
}

func TestRenameKey(t *testing.T) {
	h := readyHarness(t)
	k := h.genKey("old")
	path := "/api/v1/ssh-keys/" + k["id"].(string)
	got := wantStatus(t, h.do("PUT", path, map[string]any{"name": "new", "version": 1}), http.StatusOK)
	if got["name"] != "new" || got["version"] != float64(2) || got["public_key"] != k["public_key"] {
		t.Errorf("renamed = %v", got)
	}
	wantError(t, h.do("PUT", "/api/v1/ssh-keys/nope", map[string]any{"name": "x", "version": 1}), http.StatusNotFound, "not_found")
	wantField(t, wantError(t, h.do("PUT", path, map[string]any{"name": "", "version": 2}), http.StatusBadRequest, "validation"), "name")
}

func TestRenameVersionConflict(t *testing.T) {
	h := readyHarness(t)
	k := h.genKey("old")
	path := "/api/v1/ssh-keys/" + k["id"].(string)
	wantStatus(t, h.do("PUT", path, map[string]any{"name": "new", "version": 1}), http.StatusOK)
	wantError(t, h.do("PUT", path, map[string]any{"name": "newer", "version": 1}), http.StatusConflict, "conflict")
}

func TestDeleteKey(t *testing.T) {
	h := readyHarness(t)
	k := h.genKey("gone")
	path := "/api/v1/ssh-keys/" + k["id"].(string)
	if w := h.do("DELETE", path, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	wantError(t, h.do("DELETE", path, nil), http.StatusNotFound, "not_found")
	if n := h.count("ssh_keys"); n != 0 {
		t.Errorf("ssh_keys has %d rows", n)
	}
}

func TestKeysNeedUnlockedVault(t *testing.T) {
	h := newHarness(t)
	h.setupLegacy() // locked
	h.cookie, h.csrf = "", ""
	wantError(t, h.do("GET", "/api/v1/ssh-keys", nil), http.StatusUnauthorized, "unauthorized")
	h.login()
	wantError(t, h.do("GET", "/api/v1/ssh-keys", nil), http.StatusServiceUnavailable, "vault_locked")
	wantError(t, h.do("POST", "/api/v1/ssh-keys", map[string]any{"name": "k", "mode": "generate"}), http.StatusServiceUnavailable, "vault_locked")
}
