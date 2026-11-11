package api

import (
	"net/http"
	"os"
	"testing"

	"tern/internal/sysinfo"
)

// withMounts restarts the API as if the volumes were (not) mounted.
func (h *harness) withMounts(m sysinfo.Mounts) {
	h.t.Helper()
	h.mounts = m
	h.restart()
}

// nothingWritten checks that a refused setup left no trace.
func (h *harness) nothingWritten() {
	h.t.Helper()
	if n := h.metaRows(); n != 0 {
		h.t.Errorf("meta has %d rows after a refused setup", n)
	}
	entries, err := os.ReadDir(h.secrets)
	if err != nil {
		h.t.Fatal(err)
	}
	if len(entries) != 0 {
		h.t.Errorf("the secrets directory has %d entries after a refused setup", len(entries))
	}
	if s := h.status(); s["setup_required"] != true || s["vault_state"] != "uninitialized" {
		h.t.Errorf("status after a refused setup = %v", s)
	}
}

// Review focus 1: without /data on a volume everything would be lost with
// the container; the wizard refuses in either mode.
func TestSetupRefusedWithoutDataMount(t *testing.T) {
	h := newHarness(t)
	h.withMounts(sysinfo.Mounts{Data: false, Secrets: true})
	for _, mode := range []string{"keyfile", "passphrase"} {
		wantError(t, h.do("POST", "/api/v1/setup", setupBody(mode)), http.StatusBadRequest, "data_not_mounted")
	}
	h.nothingWritten()
}

// Review focus 1: the key file would land in the container's own layer.
func TestSetupKeyfileNeedsSecretsMount(t *testing.T) {
	h := newHarness(t)
	h.withMounts(sysinfo.Mounts{Data: true, Secrets: false})
	wantError(t, h.do("POST", "/api/v1/setup", setupBody("keyfile")), http.StatusBadRequest, "secrets_not_mounted")
	h.nothingWritten()
}

func TestSetupPassphraseWithoutSecretsMount(t *testing.T) {
	h := newHarness(t)
	h.withMounts(sysinfo.Mounts{Data: true, Secrets: false})
	if w := h.do("POST", "/api/v1/setup", setupBody("passphrase")); w.Code != http.StatusCreated {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
}

func TestStatusReportsMounts(t *testing.T) {
	h := newHarness(t)
	h.withMounts(sysinfo.Mounts{Data: true, Secrets: false})
	want := map[string]any{"data": true, "secrets": false}
	if got, _ := h.status()["mounts"].(map[string]any); !equalMaps(got, want) {
		t.Errorf("status mounts = %v, want %v", got, want)
	}
	h.setup("passphrase")
	w := h.do("GET", "/api/v1/system", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("system = %d %s", w.Code, w.Body)
	}
	if got, _ := decode(t, w)["mounts"].(map[string]any); !equalMaps(got, want) {
		t.Errorf("system mounts = %v, want %v", got, want)
	}
}

func TestSwitchToKeyfileNeedsSecretsMount(t *testing.T) {
	h := newHarness(t)
	h.withMounts(sysinfo.Mounts{Data: true, Secrets: false})
	h.setup("passphrase")
	wantError(t, h.do("POST", "/api/v1/settings/unlock-mode", map[string]any{"current_password": adminPass, "mode": "keyfile"}),
		http.StatusBadRequest, "secrets_not_mounted")
	entries, _ := os.ReadDir(h.secrets)
	if len(entries) != 0 {
		t.Errorf("the secrets directory has %d entries after a refused switch", len(entries))
	}
	w := h.do("GET", "/api/v1/settings", nil)
	if got := decode(t, w)["unlock_mode"]; got != "passphrase" {
		t.Errorf("unlock_mode = %v after a refused switch", got)
	}
}

func equalMaps(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return true
}
