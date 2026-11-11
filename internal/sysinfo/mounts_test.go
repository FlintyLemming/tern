package sysinfo

import (
	"errors"
	"testing"

	"tern/internal/config"
)

func TestCheckMounts(t *testing.T) {
	cfg := config.Config{DataDir: "/data", SecretsDir: "/secrets"}
	only := func(mounted string) func(string) (bool, error) {
		return func(p string) (bool, error) { return p == mounted, nil }
	}
	broken := func(string) (bool, error) { return true, errors.New("stat failed") }

	if got := CheckMounts(cfg, only("/data")); got != (Mounts{Data: true, Secrets: false}) {
		t.Errorf("data mounted: %+v", got)
	}
	if got := CheckMounts(cfg, only("/secrets")); got != (Mounts{Data: false, Secrets: true}) {
		t.Errorf("secrets mounted: %+v", got)
	}
	if got := CheckMounts(cfg, broken); got != (Mounts{}) {
		t.Errorf("a failing check counts as not mounted: %+v", got)
	}
	cfg.AllowUnmounted = true
	if got := CheckMounts(cfg, only("")); got != (Mounts{Data: true, Secrets: true}) {
		t.Errorf("TERN_ALLOW_UNMOUNTED: %+v", got)
	}
}
