package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"tern/internal/vault"
)

func (h *harness) settings() map[string]any {
	h.t.Helper()
	return wantStatus(h.t, h.do("GET", "/api/v1/settings", nil), http.StatusOK)
}

func settingsBody(change func(map[string]any)) map[string]any {
	b := map[string]any{"node_name": "nas-1", "max_concurrent": 1, "log_keep_days": 90, "log_keep_runs": 200}
	if change != nil {
		change(b)
	}
	return b
}

func TestSettingsDefaults(t *testing.T) {
	h := readyHarness(t)
	s := h.settings()
	if s["node_name"] != "nas-1" || s["max_concurrent"] != float64(1) || s["log_keep_days"] != float64(90) ||
		s["log_keep_runs"] != float64(200) || len(s) != 4 {
		t.Errorf("settings = %v", s)
	}
}

func TestSettingsUpdate(t *testing.T) {
	h := readyHarness(t)
	got := wantStatus(t, h.do("PUT", "/api/v1/settings", map[string]any{"node_name": "attic", "max_concurrent": 3, "log_keep_days": 30, "log_keep_runs": 50}), http.StatusOK)
	if got["node_name"] != "attic" || got["max_concurrent"] != float64(3) || got["log_keep_days"] != float64(30) || got["log_keep_runs"] != float64(50) {
		t.Errorf("PUT answered %v", got)
	}
	h.restart()
	h.login()
	if s := h.settings(); s["node_name"] != "attic" || s["max_concurrent"] != float64(3) {
		t.Errorf("after restart: %v", s)
	}
}

func TestSettingsValidation(t *testing.T) {
	h := readyHarness(t)
	cases := map[string][]any{
		"max_concurrent": {0, 9},
		"log_keep_days":  {0, 3651},
		"log_keep_runs":  {0, 10001},
		"node_name":      {"", "Has Spaces", "-leading"},
	}
	for field, values := range cases {
		for _, v := range values {
			e := wantError(t, h.do("PUT", "/api/v1/settings", settingsBody(func(b map[string]any) { b[field] = v })), http.StatusBadRequest, "validation")
			wantField(t, e, field)
		}
	}
	if s := h.settings(); s["max_concurrent"] != float64(1) || s["node_name"] != "nas-1" {
		t.Errorf("a rejected update changed something: %v", s)
	}
}

func TestSettingsApplyToScheduler(t *testing.T) {
	h := readyHarness(t)
	a, b := h.newSyncTask("a"), h.newSyncTask("b")
	engA, engB := h.blocked("a"), h.blocked("b")
	wantStatus(t, h.do("PUT", "/api/v1/settings", settingsBody(func(b map[string]any) { b["max_concurrent"] = 2 })), http.StatusOK)
	idA, idB := h.runNow(a, nil), h.runNow(b, nil)
	engA.WaitCalls(2) // both are in their unit at the same time
	engB.WaitCalls(2)
	close(engA.Block)
	close(engB.Block)
	h.rec.final(t, idA)
	h.rec.final(t, idB)
}

func TestNodeRenameKeepsBackupHost(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	wantStatus(t, h.do("PUT", "/api/v1/settings", settingsBody(func(b map[string]any) { b["node_name"] = "attic" })), http.StatusOK)
	got := wantStatus(t, h.do("GET", taskPath(task), nil), http.StatusOK)
	if host := got["backup"].(map[string]any)["host"]; host != "nas-1" {
		t.Errorf("backup.host = %v after renaming the node, want it unchanged", host)
	}
}

const newPass = "a brand new password"

func TestChangePassword(t *testing.T) {
	h := readyHarness(t)
	if w := h.do("POST", "/api/v1/settings/password", map[string]any{"current_password": adminPass, "new_password": newPass}); w.Code != http.StatusNoContent {
		t.Fatalf("change password = %d %s", w.Code, w.Body)
	}
	wantError(t, h.do("GET", "/api/v1/settings", nil), http.StatusUnauthorized, "unauthorized") // old session
	h.cookie, h.csrf = "", ""
	wantError(t, h.do("POST", "/api/v1/login", map[string]any{"password": adminPass}), http.StatusUnauthorized, "unauthorized")
	w := h.do("POST", "/api/v1/login", map[string]any{"password": newPass})
	if w.Code != http.StatusOK {
		t.Fatalf("login with the new password = %d %s", w.Code, w.Body)
	}
}

func TestChangePasswordValidation(t *testing.T) {
	h := readyHarness(t)
	e := wantError(t, h.do("POST", "/api/v1/settings/password", map[string]any{"current_password": adminPass, "new_password": "short"}), http.StatusBadRequest, "validation")
	wantField(t, e, "new_password")
	h.settings() // session still valid
}

func TestChangePasswordWrongCurrent(t *testing.T) {
	h := readyHarness(t)
	bad := map[string]any{"current_password": "not the password", "new_password": newPass}
	for i := 0; i < 5; i++ {
		wantError(t, h.do("POST", "/api/v1/settings/password", bad), http.StatusForbidden, "forbidden")
	}
	wantError(t, h.do("POST", "/api/v1/settings/password", bad), http.StatusTooManyRequests, "rate_limited")
	// Locked means locked, right password or not; and nothing was changed.
	wantError(t, h.do("POST", "/api/v1/settings/password", map[string]any{"current_password": adminPass, "new_password": newPass}),
		http.StatusTooManyRequests, "rate_limited")
	h.settings() // the session survives
}

// What tern 0.1 sealed under a passphrase-mode vault still opens after the
// vault moved to a key file: the data key does not change.
func TestOldPassphraseVaultKeepsCredentials(t *testing.T) {
	var gotPass string
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotPass, _ = r.BasicAuth()
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer dav.Close()

	h := newHarness(t)
	h.mounts.Secrets = false // keeps the vault in passphrase mode while the connection is made
	h.setupLegacy()
	h.unlock()
	b := davBody("dav")
	b["url"] = dav.URL + "/dav/"
	conn := wantStatus(t, h.do("POST", "/api/v1/connections", b), http.StatusCreated)

	h.mounts.Secrets = true
	h.restart()
	h.login()
	h.unlock()
	if h.vault.Mode() != vault.ModeKeyfile {
		t.Fatalf("vault mode after unlock = %s", h.vault.Mode())
	}
	h.restart()
	if h.vault.State() != vault.StateUnlocked {
		t.Fatalf("vault state after restart = %s, want unlocked", h.vault.State())
	}
	h.login()
	if res := h.testConn(conn["id"].(string)); res["ok"] != true || gotPass != davPass {
		t.Errorf("connection test = %v, password reached the server: %v", res, gotPass == davPass)
	}
}

func TestUnlockModeEndpointIsGone(t *testing.T) {
	h := readyHarness(t)
	w := h.do("POST", "/api/v1/settings/unlock-mode", map[string]any{"current_password": adminPass, "mode": "passphrase", "passphrase": vaultPass})
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /settings/unlock-mode = %d %s", w.Code, w.Body)
	}
}

func TestSystemEndpoint(t *testing.T) {
	h := readyHarness(t)
	s := wantStatus(t, h.do("GET", "/api/v1/system", nil), http.StatusOK)
	if len(s) != 5 || s["version"] != "test" || s["timezone"] != "UTC" { // version, timezone, bbr, tools, mounts
		t.Errorf("system = %v", s)
	}
	bbr, ok := s["bbr"].(map[string]any)
	if !ok {
		t.Fatalf("bbr = %v", s["bbr"])
	}
	for _, k := range []string{"available", "current", "supported", "active"} {
		if _, has := bbr[k]; !has {
			t.Errorf("bbr has no %q: %v", k, bbr)
		}
	}
	tools, ok := s["tools"].([]any)
	if !ok || len(tools) != 4 {
		t.Fatalf("tools = %v", s["tools"])
	}
	if first := tools[0].(map[string]any); first["name"] != "restic" {
		t.Errorf("tools[0] = %v", first)
	}
}

func TestSettingsNeedSession(t *testing.T) {
	h := readyHarness(t)
	h.cookie = ""
	for _, req := range [][2]string{{"GET", "/api/v1/settings"}, {"GET", "/api/v1/system"}} {
		wantError(t, h.do(req[0], req[1], nil), http.StatusUnauthorized, "unauthorized")
	}
}
